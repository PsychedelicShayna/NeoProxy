package openaiwire

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/neopiretry"
	"github.com/tidwall/gjson"
)

const (
	// maxAttempts is postOpenAIStream's DEFAULT_MAX_ATTEMPTS.
	maxAttempts = 6
	// maxTransientStreamRetries and transientStreamRetryDelay are
	// OPENAI_RESPONSES_MAX_TRANSIENT_STREAM_RETRIES and its delay.
	maxTransientStreamRetries = 1
	transientStreamRetryDelay = 500 * time.Millisecond
	// firstEventTimeout is the default first-event watchdog.
	firstEventTimeout = 300 * time.Second
)

var (
	concurrencyAdmissionBody = regexp.MustCompile(`"rate_limit_type"\s*:\s*"max_parallel_requests"`)
	bodyReadTimeoutPattern   = regexp.MustCompile(`(?i)\btimed out reading request body\b`)
)

// HTTPError is OpenAIHttpError: a non-2xx terminal response.
type HTTPError struct {
	Status int
	Header http.Header
	Body   []byte
}

func (e *HTTPError) Error() string {
	detail := gjson.GetBytes(e.Body, "error.message").String()
	if detail == "" {
		detail = strings.TrimSpace(string(e.Body))
	}
	if detail == "" {
		return fmt.Sprintf("%d status code (no body)", e.Status)
	}
	return fmt.Sprintf("%d %s", e.Status, detail)
}

// StreamError is a failure the stream itself reported or a stream that ended
// before a terminal response event.
type StreamError struct {
	Message string
	// Event is the failure frame, when the server sent one.
	Event      []byte
	incomplete bool
}

func (e *StreamError) Error() string { return e.Message }

// IsIncompleteStream reports a stream that ended before a terminal event.
func IsIncompleteStream(err error) bool {
	var streamErr *StreamError
	return errors.As(err, &streamErr) && streamErr.incomplete
}

// Turn sends one shaped request with NeoPi's transport policy.
type Turn struct {
	Request *Request
	// Finalize applies the proxy's payload rules to the final body once.
	Finalize func([]byte) []byte
	// Do sends one HTTP attempt.
	Do   func(*http.Request) (*http.Response, error)
	Wait func(ctx context.Context, delay time.Duration) error
}

// replayUnsafe is isOpenAIResponsesReplayUnsafeEvent: once such an event
// reaches the client, a failed stream can no longer be retried silently.
func replayUnsafe(event gjson.Result) bool {
	switch event.Get("type").String() {
	case "response.output_text.delta", "response.refusal.delta", "response.reasoning_summary_text.delta",
		"response.reasoning_text.delta", "response.function_call_arguments.delta", "response.custom_tool_call_input.delta":
		delta := event.Get("delta")
		return delta.Type == gjson.String && delta.String() != ""
	case "response.reasoning_summary_part.done", "response.output_item.done":
		return true
	}
	return false
}

func terminalEvent(kind string) bool {
	switch kind {
	case "response.completed", "response.incomplete", "response.done":
		return true
	}
	return false
}

// Run streams the turn's events to emit. Events before the first
// replay-unsafe event are held so one transient stream failure can be retried
// without the client seeing a duplicate, as NeoPi buffers its attempt stream.
func (t *Turn) Run(ctx context.Context, emit func([]byte) error) error {
	if t.Wait == nil {
		t.Wait = neopiretry.Sleep
	}
	body := t.Request.Body
	if t.Finalize != nil {
		body = t.Finalize(body)
	}
	for attempt := 0; ; attempt++ {
		live, err := t.attempt(ctx, body, emit)
		var streamErr *StreamError
		retryable := err != nil && !live && attempt < maxTransientStreamRetries && ctx.Err() == nil &&
			(errors.As(err, &streamErr) && streamErr.incomplete || isTransportFailure(err))
		if !retryable {
			return err
		}
		if errWait := t.Wait(ctx, transientStreamRetryDelay); errWait != nil {
			return errWait
		}
	}
}

func isTransportFailure(err error) bool {
	var httpErr *HTTPError
	var streamErr *StreamError
	return !errors.As(err, &httpErr) && !errors.As(err, &streamErr) && !errors.Is(err, context.Canceled)
}

// attempt opens the stream and relays it; live reports whether any event was
// handed to emit.
func (t *Turn) attempt(ctx context.Context, body []byte, emit func([]byte) error) (live bool, err error) {
	attemptCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	watchdog := time.AfterFunc(firstEventTimeout, cancel)
	defer watchdog.Stop()
	response, err := neopiretry.Do(attemptCtx, func(int) (*http.Response, error) {
		request, errRequest := http.NewRequestWithContext(attemptCtx, http.MethodPost, t.Request.URL, bytes.NewReader(body))
		if errRequest != nil {
			return nil, errRequest
		}
		request.Header = t.Request.Header.Clone()
		request.Header.Set("Accept-Encoding", "gzip, deflate, br, zstd")
		request.Header.Set("Connection", "keep-alive")
		return t.Do(request)
	}, neopiretry.Options{
		MaxAttempts: maxAttempts,
		Wait:        t.Wait,
		ShouldRetry: func(response *http.Response, text string) bool {
			if strings.TrimSpace(response.Header.Get("rate_limit_type")) == "max_parallel_requests" || concurrencyAdmissionBody.MatchString(text) {
				return false
			}
			return !(response.StatusCode == http.StatusRequestTimeout && bodyReadTimeoutPattern.MatchString(text))
		},
	})
	if err != nil {
		return false, err
	}
	defer func() { _ = response.Body.Close() }()
	// Connected: no deadline applies to the stream from here on.
	watchdog.Stop()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		data, _ := io.ReadAll(response.Body)
		return false, &HTTPError{Status: response.StatusCode, Header: response.Header, Body: data}
	}
	var held [][]byte
	flush := func() error {
		for _, event := range held {
			if errEmit := emit(event); errEmit != nil {
				return errEmit
			}
		}
		held = nil
		return nil
	}
	sawTerminal := false
	reader := bufio.NewReaderSize(response.Body, 64<<10)
	var data bytes.Buffer
	dispatch := func() error {
		if data.Len() == 0 {
			return nil
		}
		frame := bytes.Clone(data.Bytes())
		data.Reset()
		if string(frame) == "[DONE]" {
			// NeoPi stops reading here; the sentinel is relayed so a client
			// that expects it after the terminal event still receives it.
			if errFlush := flush(); errFlush != nil {
				return errFlush
			}
			live = true
			if errEmit := emit(frame); errEmit != nil {
				return errEmit
			}
			return io.EOF
		}
		if !json.Valid(frame) {
			if inBand := classifyInBandText(string(frame)); inBand != nil {
				return inBand
			}
			return &StreamError{Message: "provider sent a non-JSON stream frame: " + string(frame)}
		}
		if text := gjson.ParseBytes(frame); text.Type == gjson.String {
			// A double-encoded proxy page: a throttle fails, anything else is no event.
			if inBand := classifyInBandText(text.String()); inBand != nil {
				return inBand
			}
			return nil
		}
		watchdog.Stop()
		event := gjson.ParseBytes(frame)
		kind := event.Get("type").String()
		if kind == "error" || kind == "response.failed" {
			if errFlush := flush(); errFlush != nil {
				return errFlush
			}
			live = true
			message := event.Get("message").String()
			if message == "" {
				message = event.Get("response.error.message").String()
			}
			if message == "" {
				message = "An unknown error occurred"
			}
			return &StreamError{Message: message, Event: frame}
		}
		if terminalEvent(kind) {
			sawTerminal = true
		}
		if !live && (replayUnsafe(event) || sawTerminal) {
			live = true
		}
		held = append(held, frame)
		if live {
			return flush()
		}
		return nil
	}
	for {
		line, errRead := reader.ReadBytes('\n')
		trimmed := bytes.TrimRight(line, "\r\n")
		switch {
		case len(trimmed) == 0 && len(line) > 0:
			if errDispatch := dispatch(); errDispatch != nil {
				if errDispatch == io.EOF {
					errRead = io.EOF
					break
				}
				return live, errDispatch
			}
		case bytes.HasPrefix(trimmed, []byte("data:")):
			value := bytes.TrimPrefix(trimmed[len("data:"):], []byte(" "))
			if data.Len() > 0 {
				data.WriteByte('\n')
			}
			data.Write(value)
		}
		if errRead != nil {
			if errRead == io.EOF {
				if errDispatch := dispatch(); errDispatch != nil && errDispatch != io.EOF {
					return live, errDispatch
				}
				if !sawTerminal {
					return live, &StreamError{Message: "OpenAI responses stream closed before a terminal response event was received", incomplete: true}
				}
				return live, flush()
			}
			if ctx.Err() != nil {
				return live, ctx.Err()
			}
			return live, errRead
		}
	}
}

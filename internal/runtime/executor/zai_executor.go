package executor

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/jsjson"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/openaiwire"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/thinking"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
)

// ZaiExecutor serves a Z.ai API key the way NeoPi's `zai` provider does: each
// catalog row on the API NeoPi routes it to, Anthropic Messages at
// api.z.ai/api/anthropic or Chat Completions on the coding plan endpoint.
type ZaiExecutor struct {
	*ClaudeExecutor
	cfg *config.Config
	// counter estimates tokens locally; NeoPi never asks Z.ai to count.
	counter *OpenAICompatExecutor
}

// NewZaiExecutor binds the Z.ai executor to an openai-compatibility provider key.
func NewZaiExecutor(provider string, cfg *config.Config) *ZaiExecutor {
	messages := NewClaudeExecutor(cfg)
	messages.provider = provider
	messages.thirdParty = &neoPiThirdPartyProvider{
		catalog: func(_ cliproxyexecutor.Request, model string) *registry.NeoPiModel {
			return registry.ZaiNeoPiModel(model)
		},
		bearer: zaiAPIKey,
	}
	return &ZaiExecutor{ClaudeExecutor: messages, cfg: cfg, counter: NewOpenAICompatExecutor(provider, cfg)}
}

// UsesConfig reports whether the executor was built for cfg.
func (e *ZaiExecutor) UsesConfig(cfg *config.Config) bool { return e != nil && e.cfg == cfg }

// SupportsNativeToolSearch keeps Z.ai out of Anthropic's native tool search.
func (e *ZaiExecutor) SupportsNativeToolSearch(*cliproxyauth.Auth, string) bool { return false }

// RequestToFormat reports the dialect the selected catalog row is sent in.
func (e *ZaiExecutor) RequestToFormat(req cliproxyexecutor.Request, _ cliproxyexecutor.Options) sdktranslator.Format {
	if row := e.row(req.Model); row != nil && row.API == "openai-completions" {
		return sdktranslator.FormatOpenAI
	}
	return sdktranslator.FormatClaude
}

func (e *ZaiExecutor) row(model string) *registry.NeoPiModel {
	return registry.ZaiNeoPiModel(thinking.ParseSuffix(model).ModelName)
}

func zaiAPIKey(auth *cliproxyauth.Auth) string {
	if auth == nil {
		return ""
	}
	if key := strings.TrimSpace(auth.Attributes["api_key"]); key != "" {
		return key
	}
	key, _ := auth.Metadata["api_key"].(string)
	return strings.TrimSpace(key)
}

// Refresh is a no-op: a Z.ai key (pasted or minted at sign-in) never expires.
func (e *ZaiExecutor) Refresh(_ context.Context, auth *cliproxyauth.Auth) (*cliproxyauth.Auth, error) {
	return auth, nil
}

// HttpRequest sends an arbitrary request with the account's bearer key.
func (e *ZaiExecutor) HttpRequest(ctx context.Context, auth *cliproxyauth.Auth, request *http.Request) (*http.Response, error) {
	if request == nil {
		return nil, fmt.Errorf("zai executor: request is nil")
	}
	if key := zaiAPIKey(auth); key != "" {
		request.Header.Set("Authorization", "Bearer "+key)
	}
	if auth != nil {
		util.ApplyCustomHeadersFromAttrs(request, auth.Attributes)
	}
	return helps.NewProxyAwareHTTPClient(ctx, e.cfg, auth, 0).Do(request)
}

// CountTokens estimates locally from the Chat Completions form.
func (e *ZaiExecutor) CountTokens(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return e.counter.CountTokens(ctx, auth, req, opts)
}

func (e *ZaiExecutor) Execute(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	row := e.row(req.Model)
	if row == nil {
		return cliproxyexecutor.Response{}, statusErr{code: http.StatusBadRequest, msg: fmt.Sprintf("zai: model %s is not in NeoPi's Z.ai catalog", req.Model)}
	}
	if row.API == "openai-completions" {
		return e.executeCompletions(ctx, auth, req, opts, row)
	}
	return e.ClaudeExecutor.Execute(ctx, auth, req, opts)
}

func (e *ZaiExecutor) ExecuteStream(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	row := e.row(req.Model)
	if row == nil {
		return nil, statusErr{code: http.StatusBadRequest, msg: fmt.Sprintf("zai: model %s is not in NeoPi's Z.ai catalog", req.Model)}
	}
	if row.API == "openai-completions" {
		return e.streamCompletions(ctx, auth, req, opts, row)
	}
	return e.ClaudeExecutor.ExecuteStream(ctx, auth, req, opts)
}

type zaiCompletionsPrepared struct {
	body     []byte
	headers  http.Header
	url      string
	model    string
	original []byte
}

func (e *ZaiExecutor) prepareCompletions(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options, row *registry.NeoPiModel) (*zaiCompletionsPrepared, error) {
	key := zaiAPIKey(auth)
	if key == "" {
		return nil, statusErr{code: http.StatusUnauthorized, msg: e.Identifier() + ": account has no API key"}
	}
	original := req.Payload
	if len(opts.OriginalRequest) > 0 {
		original = opts.OriginalRequest
	}
	originalTranslated, translated, err := helps.TranslateRequestPairReturningError(ctx, opts.Headers, e.cfg, opts.SourceFormat, sdktranslator.FormatOpenAI, row.ID, original, req.Payload, true, helps.APIKeyModelIsCompat(req))
	if err != nil {
		return nil, err
	}
	if translated, err = helps.ApplyRequestThinking(translated, req, opts, opts.SourceFormat.String(), "openai", e.Identifier()); err != nil {
		return nil, err
	}
	body, err := neoPiZaiCompletionsRequest(translated, row)
	if err != nil {
		return nil, err
	}
	body = helps.NewPayloadFinalizer(e.cfg, e.Identifier(), row.ID, "openai", "", originalTranslated, req, opts)(body)
	headers := neoPiZaiCompletionsHeaders(ctx, auth, opts.Headers, key)
	return &zaiCompletionsPrepared{body: body, headers: headers, url: strings.TrimRight(row.BaseURL, "/") + "/chat/completions", model: row.ID, original: original}, nil
}

// neoPiZaiCompletionsHeaders is the OpenAI client's request headers as NeoPi
// sends them to Z.ai: Bearer key, NeoPi's user agent, the SDK timeout header.
// Configured per-account headers apply first; the enforced ones win.
func neoPiZaiCompletionsHeaders(ctx context.Context, auth *cliproxyauth.Auth, incoming http.Header, key string) http.Header {
	headers := make(http.Header)
	if auth != nil {
		request := (&http.Request{Header: headers}).WithContext(ctx)
		util.ApplyCustomHeadersFromAttrs(request, auth.Attributes, incoming)
	}
	headers.Set("Accept", "text/event-stream")
	headers.Set("Accept-Encoding", "gzip, deflate, br, zstd")
	headers.Set("Authorization", "Bearer "+key)
	headers.Set("Connection", "keep-alive")
	headers.Set("Content-Type", "application/json")
	if headers.Get("User-Agent") == "" {
		headers.Set("User-Agent", openaiwire.NeoPiUserAgent)
	}
	headers.Set("X-Stainless-Timeout", "600")
	return headers
}

// openCompletions posts the shaped request and returns the SSE body, with
// NeoPi's provider retry for failures before any response byte.
func (e *ZaiExecutor) openCompletions(ctx context.Context, auth *cliproxyauth.Auth, prepared *zaiCompletionsPrepared, client *http.Client) (*http.Response, error) {
	for attempt := 0; ; attempt++ {
		request, err := http.NewRequestWithContext(ctx, http.MethodPost, prepared.url, bytes.NewReader(prepared.body))
		if err != nil {
			return nil, err
		}
		request.Header = prepared.headers.Clone()
		info := helps.UpstreamRequestLog{URL: request.URL.String(), Method: request.Method, Headers: request.Header, Body: prepared.body, Provider: e.Identifier()}
		if auth != nil {
			info.AuthID, info.AuthLabel = auth.ID, auth.Label
			info.AuthType, info.AuthValue = auth.AccountInfo()
		}
		helps.RecordAPIRequest(ctx, e.cfg, info)
		response, err := client.Do(request)
		if err != nil {
			helps.RecordAPIResponseError(ctx, e.cfg, err)
			if retryErr := e.retry(ctx, err, attempt); retryErr != nil {
				return nil, retryErr
			}
			continue
		}
		helps.RecordAPIResponseMetadata(ctx, e.cfg, response.StatusCode, response.Header)
		if response.StatusCode >= 200 && response.StatusCode < 300 {
			decoded, errDecode := util.DecodeResponseBody(response.Body, response.Header.Get("Content-Encoding"))
			if errDecode != nil {
				response.Body.Close()
				return nil, errDecode
			}
			response.Body = decoded
			return response, nil
		}
		decoded, errDecode := util.DecodeResponseBody(response.Body, response.Header.Get("Content-Encoding"))
		var data []byte
		if errDecode == nil {
			data, _ = io.ReadAll(decoded)
			decoded.Close()
		} else {
			response.Body.Close()
		}
		helps.AppendAPIResponseChunk(ctx, e.cfg, data)
		err = &neoPiUpstreamError{code: response.StatusCode, body: string(data), headers: response.Header}
		if retryErr := e.retry(ctx, err, attempt); retryErr != nil {
			return nil, retryErr
		}
	}
}

func (e *ZaiExecutor) executeCompletions(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options, row *registry.NeoPiModel) (result cliproxyexecutor.Response, err error) {
	prepared, err := e.prepareCompletions(ctx, auth, req, opts, row)
	if err != nil {
		return result, err
	}
	reporter := helps.NewExecutorUsageReporter(ctx, e, prepared.model, auth)
	defer reporter.TrackFailure(ctx, &err)
	response, err := e.openCompletions(ctx, auth, prepared, reporter.TrackHTTPClient(helps.NewProxyAwareHTTPClient(ctx, e.cfg, auth, 0)))
	if err != nil {
		return result, err
	}
	defer response.Body.Close()
	var usage helps.StreamUsageBuffer
	var lines [][]byte
	err = neoPiScanCompletions(ctx, e.cfg, e.Identifier(), response.Body, func(line []byte) error {
		usage.ObserveOpenAIStream(line)
		reporter.ObserveResponseModel(line)
		lines = append(lines, line)
		return nil
	})
	if err != nil {
		usage.PublishFailure(ctx, reporter, err)
		return result, err
	}
	format := cliproxyexecutor.ResponseFormatOrSource(opts)
	data := neoPiAggregateCompletionStream(lines)
	if format != sdktranslator.FormatOpenAI {
		var state any
		data = sdktranslator.TranslateNonStream(ctx, sdktranslator.FormatOpenAI, format, req.Model, helps.ApplyPatchOriginalRequest(req, opts), prepared.body, data, &state)
		if helps.ApplyPatchTranslationError(state) != nil {
			return result, statusErr{code: http.StatusBadGateway, msg: helps.ApplyPatchUpstreamErrorMessage}
		}
		if len(data) == 0 {
			return result, errors.New("zai: response translation failed")
		}
	}
	usage.Publish(ctx, reporter)
	log.WithFields(log.Fields{"provider": e.Identifier(), "model": prepared.model}).Info("provider request completed")
	return cliproxyexecutor.Response{Payload: data, Headers: response.Header.Clone()}, nil
}

func (e *ZaiExecutor) streamCompletions(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options, row *registry.NeoPiModel) (_ *cliproxyexecutor.StreamResult, err error) {
	prepared, err := e.prepareCompletions(ctx, auth, req, opts, row)
	if err != nil {
		return nil, err
	}
	reporter := helps.NewExecutorUsageReporter(ctx, e, prepared.model, auth)
	defer reporter.TrackFailure(ctx, &err)
	response, err := e.openCompletions(ctx, auth, prepared, reporter.TrackHTTPClient(helps.NewProxyAwareHTTPClient(ctx, e.cfg, auth, 0)))
	if err != nil {
		return nil, err
	}
	out := make(chan cliproxyexecutor.StreamChunk, 1)
	go func() {
		defer close(out)
		defer response.Body.Close()
		var usage helps.StreamUsageBuffer
		var state any
		format := cliproxyexecutor.ResponseFormatOrSource(opts)
		original := helps.ApplyPatchOriginalRequest(req, opts)
		helps.InitializeApplyPatchStream(ctx, sdktranslator.FormatOpenAI, format, req.Model, original, prepared.body, &state)
		send := func(chunk []byte) error {
			select {
			case out <- cliproxyexecutor.StreamChunk{Payload: bytes.Clone(chunk)}:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		consumeErr := neoPiScanCompletions(ctx, e.cfg, e.Identifier(), response.Body, func(line []byte) error {
			usage.ObserveOpenAIStream(line)
			reporter.ObserveResponseModel(line)
			for _, chunk := range sdktranslator.TranslateStream(ctx, sdktranslator.FormatOpenAI, format, req.Model, original, prepared.body, line, &state) {
				if errSend := send(chunk); errSend != nil {
					return errSend
				}
			}
			if helps.ApplyPatchTranslationError(state) != nil {
				return statusErr{code: http.StatusBadGateway, msg: helps.ApplyPatchUpstreamErrorMessage}
			}
			return nil
		})
		for _, chunk := range helps.FinalizeApplyPatchStream(state) {
			if send(chunk) != nil {
				return
			}
		}
		if consumeErr != nil {
			usage.PublishFailure(ctx, reporter, consumeErr)
			select {
			case out <- cliproxyexecutor.StreamChunk{Err: consumeErr}:
			case <-ctx.Done():
			}
			return
		}
		usage.Publish(ctx, reporter)
		log.WithFields(log.Fields{"provider": e.Identifier(), "model": prepared.model}).Info("provider request completed")
	}()
	return &cliproxyexecutor.StreamResult{Headers: response.Header.Clone(), Chunks: out}, nil
}

// neoPiScanCompletions relays each `data:` line of a Chat Completions stream
// through [DONE], as NeoPi's client does for an openai-completions host.
func neoPiScanCompletions(ctx context.Context, cfg *config.Config, provider string, body io.Reader, emit func([]byte) error) error {
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 64<<10), 32<<20)
	finished := false
	for scanner.Scan() {
		raw := scanner.Bytes()
		helps.AppendAPIResponseChunk(ctx, cfg, raw)
		data := bytes.TrimSpace(raw)
		if !bytes.HasPrefix(data, []byte("data:")) {
			continue
		}
		payload := bytes.TrimSpace(data[len("data:"):])
		if bytes.Equal(payload, []byte("[DONE]")) {
			return emit([]byte("data: [DONE]"))
		}
		if !gjson.ValidBytes(payload) {
			continue
		}
		if message := gjson.GetBytes(payload, "error"); message.Exists() {
			return statusErr{code: http.StatusBadGateway, msg: provider + ": " + message.Raw}
		}
		if reason := gjson.GetBytes(payload, "choices.0.finish_reason"); reason.Type == gjson.String && reason.String() != "" {
			finished = true
		}
		if err := emit(append([]byte("data: "), payload...)); err != nil {
			return err
		}
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	if finished {
		return emit([]byte("data: [DONE]"))
	}
	return errors.New(provider + ": stream ended before [DONE]")
}

// neoPiAggregateCompletionStream folds Chat Completions stream lines into the
// non-streaming chat.completion object a non-streaming client expects.
func neoPiAggregateCompletionStream(lines [][]byte) []byte {
	type call struct {
		id, kind, name string
		arguments      strings.Builder
	}
	var id, model, finish string
	var created int64
	var content, reasoning strings.Builder
	var calls []*call
	var usage json.RawMessage
	for _, line := range lines {
		payload := bytes.TrimSpace(bytes.TrimPrefix(line, []byte("data:")))
		if !gjson.ValidBytes(payload) {
			continue
		}
		chunk := gjson.ParseBytes(payload)
		if id == "" {
			id, model, created = chunk.Get("id").String(), chunk.Get("model").String(), chunk.Get("created").Int()
		}
		if value := chunk.Get("usage"); value.IsObject() {
			usage = json.RawMessage(value.Raw)
		}
		for _, choice := range chunk.Get("choices").Array() {
			delta := choice.Get("delta")
			content.WriteString(delta.Get("content").String())
			reasoning.WriteString(delta.Get("reasoning_content").String())
			for _, part := range delta.Get("tool_calls").Array() {
				index := int(part.Get("index").Int())
				for len(calls) <= index {
					calls = append(calls, &call{kind: "function"})
				}
				if value := part.Get("id").String(); value != "" {
					calls[index].id = value
				}
				if value := part.Get("function.name").String(); value != "" {
					calls[index].name = value
				}
				calls[index].arguments.WriteString(part.Get("function.arguments").String())
			}
			if value := choice.Get("finish_reason").String(); value != "" {
				finish = value
			}
		}
	}
	message := []jsjson.Field{{Name: "role", Value: jsjson.Marshal("assistant")}, {Name: "content", Value: jsjson.Marshal(content.String())}}
	if reasoning.Len() > 0 {
		message = append(message, jsjson.Field{Name: "reasoning_content", Value: jsjson.Marshal(reasoning.String())})
	}
	if len(calls) > 0 {
		wire := make([]json.RawMessage, 0, len(calls))
		for _, c := range calls {
			wire = append(wire, jsjson.Object(jsjson.Field{Name: "id", Value: jsjson.Marshal(c.id)}, jsjson.Field{Name: "type", Value: jsjson.Marshal(c.kind)}, jsjson.Field{Name: "function", Value: jsjson.Object(
				jsjson.Field{Name: "name", Value: jsjson.Marshal(c.name)}, jsjson.Field{Name: "arguments", Value: jsjson.Marshal(c.arguments.String())})}))
		}
		message = append(message, jsjson.Field{Name: "tool_calls", Value: jsjson.Marshal(wire)})
	}
	var finishRaw json.RawMessage = jsjson.Marshal(finish)
	if finish == "" {
		finishRaw = json.RawMessage("null")
	}
	choice := jsjson.Object(jsjson.Field{Name: "index", Value: jsjson.Marshal(0)}, jsjson.Field{Name: "message", Value: jsjson.Object(message...)}, jsjson.Field{Name: "finish_reason", Value: finishRaw})
	fields := []jsjson.Field{{Name: "id", Value: jsjson.Marshal(id)}, {Name: "object", Value: jsjson.Marshal("chat.completion")}, {Name: "created", Value: jsjson.Marshal(created)}, {Name: "model", Value: jsjson.Marshal(model)}, {Name: "choices", Value: jsjson.Marshal([]json.RawMessage{choice})}}
	if usage != nil {
		fields = append(fields, jsjson.Field{Name: "usage", Value: usage})
	}
	return jsjson.Marshal(jsjson.Object(fields...))
}

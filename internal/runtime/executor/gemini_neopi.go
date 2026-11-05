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
	neturl "net/url"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/googlewire"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/jsjson"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/openaiwire"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	internalsignature "github.com/router-for-me/CLIProxyAPI/v8/internal/signature"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/thinking"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
)

// NeoPiGoogleHooks bind the shared NeoPi Google surface to one provider: which
// catalog row a model id resolves to, the API key, the base URL and any extra
// identity headers (a gateway row's session header).
type NeoPiGoogleHooks struct {
	Row     func(req cliproxyexecutor.Request, model string) *registry.NeoPiModel
	Key     func(auth *cliproxyauth.Auth) string
	BaseURL func(auth *cliproxyauth.Auth, row *registry.NeoPiModel) string
	// Headers returns extra identity headers (a gateway row's x-opencode-session);
	// sessionID is NeoPi's options.sessionId for the request.
	Headers func(ctx context.Context, auth *cliproxyauth.Auth, incoming http.Header, sessionID string) http.Header
}

// NeoPiGoogleExecutor serves the Gemini API the way NeoPi's `google` provider
// does: the request an OpenAI Chat body describes is shaped exactly as NeoPi's
// streamSimple -> streamGoogle shapes it (googlewire), then POSTed to
// :streamGenerateContent?alt=sse with NeoPi's headers.
type NeoPiGoogleExecutor struct {
	cfg      *config.Config
	provider string
	hooks    NeoPiGoogleHooks
	// counter estimates tokens locally; NeoPi never asks Google to count.
	counter *OpenAICompatExecutor
}

// NewNeoPiGoogleExecutor builds the NeoPi Google executor for a provider id.
func NewNeoPiGoogleExecutor(provider string, cfg *config.Config, hooks NeoPiGoogleHooks) *NeoPiGoogleExecutor {
	if hooks.Row == nil {
		hooks.Row = geminiNeoPiRow
	}
	if hooks.Key == nil {
		hooks.Key = geminiAPIKey
	}
	if hooks.BaseURL == nil {
		hooks.BaseURL = geminiNeoPiBaseURL
	}
	return &NeoPiGoogleExecutor{cfg: cfg, provider: provider, hooks: hooks, counter: NewOpenAICompatExecutor(provider, cfg)}
}

// geminiNeoPiRow is the NeoPi record the account's registration produced for
// model, else NeoPi's bundled row, else the row NeoPi's rules would build.
func geminiNeoPiRow(req cliproxyexecutor.Request, model string) *registry.NeoPiModel {
	if info, ok := cliproxyauth.ResolvedModelInfo(req); ok && info != nil && info.NeoPi != nil {
		return info.NeoPi
	}
	return registry.GoogleNeoPiModelFor(model)
}

// geminiNeoPiBaseURL is NeoPi's model.baseUrl: a configured base replaces it,
// taking the API version segment with it.
func geminiNeoPiBaseURL(auth *cliproxyauth.Auth, row *registry.NeoPiModel) string {
	if auth != nil && auth.Attributes != nil {
		if custom := strings.TrimRight(strings.TrimSpace(auth.Attributes["base_url"]), "/"); custom != "" {
			last := custom[strings.LastIndex(custom, "/")+1:]
			if last == "v1" || last == "v1beta" || last == "v1alpha" {
				return custom
			}
			return custom + "/v1beta"
		}
	}
	if row != nil && strings.TrimSpace(row.BaseURL) != "" {
		return strings.TrimRight(row.BaseURL, "/")
	}
	return registry.GoogleBaseURL
}

// Identifier returns the executor identifier.
func (e *NeoPiGoogleExecutor) Identifier() string {
	if e == nil || strings.TrimSpace(e.provider) == "" {
		return "gemini"
	}
	return e.provider
}

// UsesConfig reports whether the executor was built for cfg.
func (e *NeoPiGoogleExecutor) UsesConfig(cfg *config.Config) bool { return e != nil && e.cfg == cfg }

// SupportsApplyPatch keeps NeoProxy's apply_patch bridge for this provider.
func (e *NeoPiGoogleExecutor) SupportsApplyPatch() bool { return e != nil }

// RequestToFormat reports the upstream wire format: NeoPi always speaks
// generateContent, and the request is shaped from the OpenAI Chat form.
func (e *NeoPiGoogleExecutor) RequestToFormat(cliproxyexecutor.Request, cliproxyexecutor.Options) sdktranslator.Format {
	return sdktranslator.FormatGemini
}

// PrepareRequest injects the API key the way NeoPi's headers carry it.
func (e *NeoPiGoogleExecutor) PrepareRequest(req *http.Request, auth *cliproxyauth.Auth) error {
	if req == nil {
		return nil
	}
	key := e.hooks.Key(auth)
	if key != "" {
		req.Header.Set("x-goog-api-key", key)
	}
	req.Header.Del("Authorization")
	applyGeminiHeaders(req, auth)
	return nil
}

// HttpRequest sends an arbitrary request with the account's key.
func (e *NeoPiGoogleExecutor) HttpRequest(ctx context.Context, auth *cliproxyauth.Auth, req *http.Request) (*http.Response, error) {
	if req == nil {
		return nil, fmt.Errorf("gemini executor: request is nil")
	}
	if ctx == nil {
		ctx = req.Context()
	}
	httpReq := req.WithContext(ctx)
	if err := e.PrepareRequest(httpReq, auth); err != nil {
		return nil, err
	}
	return helps.NewProxyAwareHTTPClient(ctx, e.cfg, auth, 0).Do(httpReq)
}

// Refresh is a no-op: a Gemini key never expires.
func (e *NeoPiGoogleExecutor) Refresh(ctx context.Context, auth *cliproxyauth.Auth) (*cliproxyauth.Auth, error) {
	if refreshed, handled, err := helps.RefreshAuthViaHome(ctx, e.cfg, auth); handled {
		return refreshed, err
	}
	return auth, nil
}

// CountTokens estimates locally from the Chat Completions form; NeoPi never
// calls Google's countTokens.
func (e *NeoPiGoogleExecutor) CountTokens(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return e.counter.CountTokens(ctx, auth, req, opts)
}

// neoPiGoogleWireModel is the googlewire view of a NeoPi catalog row.
func neoPiGoogleWireModel(row *registry.NeoPiModel) *googlewire.Model {
	model := &googlewire.Model{
		ID: row.ID, Provider: row.Provider, API: row.API, Input: row.Input,
		Reasoning: row.Reasoning, MaxTokens: row.MaxTokens, NameMaxLength: 128,
		NormalizeSignature: func(signature string) (string, bool) {
			return internalsignature.CompatibleSignatureForProviderBlock(internalsignature.SignatureProviderGemini, signature, internalsignature.SignatureBlockKindGeminiFunctionCall)
		},
	}
	if model.Provider == "" {
		model.Provider = "google"
	}
	if row.Thinking != nil {
		model.Thinking = &googlewire.Thinking{Efforts: row.Thinking.Efforts, Mode: row.Thinking.Mode,
			RequiresEffort: row.Thinking.RequiresEffort, SuppressWhenOff: row.Thinking.SuppressWhenOff,
			EffortBudgets: row.Thinking.EffortBudgets}
	}
	if compat := row.GoogleCompat; compat != nil {
		model.Compat = googlewire.Compat{
			MultimodalFunctionResponse:                      compat.MultimodalFunctionResponse,
			RequiresSkipThoughtSignature:                    compat.RequiresSkipThoughtSignature,
			RequiresSkipThoughtSignatureOnFirstFunctionCall: compat.RequiresSkipThoughtSignatureOnFirstFunctionCall,
			SupportsFunctionPartID:                          compat.SupportsFunctionPartID,
			DropUnsignedThinking:                            compat.DropUnsignedThinking,
			CCALegacyParametersSchema:                       compat.CCALegacyParametersSchema,
		}
	}
	return model
}

type geminiPrepared struct {
	body     []byte
	headers  http.Header
	url      string
	model    string
	original []byte
}

// prepare shapes one NeoPi Google request: the downstream body becomes the
// OpenAI Chat form NeoPi's model sees, then NeoPi's own request.
func (e *NeoPiGoogleExecutor) prepare(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options, stream bool) (*geminiPrepared, error) {
	baseModel := thinking.ParseSuffix(req.Model).ModelName
	row := e.hooks.Row(req, baseModel)
	if row == nil {
		return nil, statusErr{code: http.StatusBadRequest, msg: fmt.Sprintf("gemini: model %s is not in NeoPi's Google catalog", baseModel)}
	}
	key := e.hooks.Key(auth)
	if key == "" {
		return nil, statusErr{code: http.StatusUnauthorized, msg: e.Identifier() + ": account has no API key"}
	}
	original := req.Payload
	if len(opts.OriginalRequest) > 0 {
		original = opts.OriginalRequest
	}
	originalTranslated, translated, err := helps.TranslateRequestPairReturningError(ctx, opts.Headers, e.cfg, opts.SourceFormat, sdktranslator.FormatOpenAI, row.ID, original, req.Payload, stream, helps.APIKeyModelIsCompat(req))
	if err != nil {
		return nil, err
	}
	translated, err = helps.ApplyRequestThinking(translated, req, opts, opts.SourceFormat.String(), "openai", e.Identifier())
	if err != nil {
		return nil, err
	}
	wire := neoPiGoogleWireModel(row)
	neoPiContext, options, err := googlewire.FromOpenAIChat(translated, wire)
	if err != nil {
		return nil, statusErr{code: http.StatusBadRequest, msg: err.Error()}
	}
	resolved, err := googlewire.MapOptions(wire, options)
	if err != nil {
		return nil, statusErr{code: http.StatusBadRequest, msg: err.Error()}
	}
	body, err := googlewire.BuildBody(wire, neoPiContext, resolved, nil)
	if err != nil {
		return nil, err
	}
	body = helps.NewPayloadFinalizer(e.cfg, e.Identifier(), row.ID, "gemini", "", originalTranslated, req, opts)(body)
	headers := neoPiGoogleHeaders(ctx, auth, opts.Headers, key)
	if e.hooks.Headers != nil {
		session := neoPiSession(opts, translated)
		if session == "" {
			session = helps.ProviderSessionUUID(e.Identifier(), req.Metadata, opts.Metadata)
		}
		for name, values := range e.hooks.Headers(ctx, auth, opts.Headers, session) {
			headers[name] = values
		}
	}
	url := fmt.Sprintf("%s/models/%s:streamGenerateContent?alt=sse", e.hooks.BaseURL(auth, row), row.ID)
	return &geminiPrepared{body: body, headers: headers, url: url, model: row.ID, original: original}, nil
}

// neoPiGoogleHeaders is what NeoPi's fetch sends to the Gemini API: the API
// key, NeoPi's identity, and the model/option headers; Accept-Encoding stays
// the client default the runtime sets for every request.
func neoPiGoogleHeaders(ctx context.Context, auth *cliproxyauth.Auth, incoming http.Header, key string) http.Header {
	headers := make(http.Header)
	if auth != nil {
		request := (&http.Request{Header: headers}).WithContext(ctx)
		util.ApplyCustomHeadersFromAttrs(request, auth.Attributes, incoming)
	}
	headers.Set("Accept", "text/event-stream")
	headers.Set("Accept-Encoding", "gzip, deflate, br, zstd")
	headers.Set("Content-Type", "application/json")
	headers.Set("x-goog-api-key", key)
	if headers.Get("User-Agent") == "" {
		headers.Set("User-Agent", openaiwire.NeoPiUserAgent)
	}
	return headers
}

// open posts one attempt and returns the decoded SSE body.
func (e *NeoPiGoogleExecutor) open(ctx context.Context, auth *cliproxyauth.Auth, prepared *geminiPrepared, client *http.Client) (*http.Response, error) {
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
		return nil, err
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
	helps.LogWithRequestID(ctx).Debugf("gemini: upstream error %d: %s", response.StatusCode, googlewire.ExtractErrorMessage(string(data), response.StatusCode))
	var retryAfter *time.Duration
	if delay, ok := neoPiRetryAfter(response.Header, time.Now()); ok {
		retryAfter = &delay
	}
	return nil, statusErr{code: response.StatusCode, msg: string(data), retryAfter: retryAfter}
}

// geminiChunk is one decoded generateContent stream chunk, reduced to what the
// empty-response retry needs.
type geminiChunk struct {
	payload    []byte
	meaningful bool
	finish     string
	err        error
}

// readGeminiChunk inspects one SSE data frame.
func readGeminiChunk(payload []byte) geminiChunk {
	chunk := geminiChunk{payload: payload}
	if len(payload) == 0 {
		return chunk
	}
	root := gjson.ParseBytes(payload)
	if inBand := root.Get("error"); inBand.Exists() {
		chunk.err = googlewire.StreamChunkError(payload)
		return chunk
	}
	if candidate := root.Get("candidates.0"); candidate.Exists() {
		chunk.finish = candidate.Get("finishReason").String()
		for _, part := range candidate.Get("content.parts").Array() {
			if part.Get("functionCall").Exists() {
				chunk.meaningful = true
			}
			if text := part.Get("text"); text.Type == gjson.String && part.Get("thought").Type != gjson.True && strings.TrimSpace(text.String()) != "" {
				chunk.meaningful = true
			}
		}
		return chunk
	}
	if block := root.Get("promptFeedback.blockReason"); block.Type == gjson.String {
		detail := root.Get("promptFeedback.blockReasonMessage").String()
		message := "Request blocked by Google (" + block.String() + ")"
		if detail != "" {
			message += ": " + detail
		}
		chunk.err = statusErr{code: http.StatusBadGateway, msg: message}
	}
	return chunk
}

// scanGeminiStream relays every SSE data frame of one attempt.
func scanGeminiStream(ctx context.Context, cfg *config.Config, body io.Reader, emit func(geminiChunk) error) (bool, error) {
	scanner := bufio.NewScanner(body)
	scanner.Buffer(nil, streamScannerBuffer)
	meaningful := false
	for scanner.Scan() {
		line := scanner.Bytes()
		helps.AppendAPIResponseChunk(ctx, cfg, line)
		payload := helps.JSONPayload(line)
		if len(payload) == 0 {
			continue
		}
		chunk := readGeminiChunk(payload)
		if chunk.meaningful {
			meaningful = true
		}
		if errEmit := emit(chunk); errEmit != nil {
			return meaningful, errEmit
		}
	}
	if err := scanner.Err(); err != nil {
		return meaningful, err
	}
	return meaningful, nil
}

// emptyResponseAttempts is MAX_EMPTY_STREAM_RETRIES + 1.
const (
	maxEmptyStreamRetries = 2
	emptyStreamBaseDelay  = 500 * time.Millisecond
)

// Execute performs a non-streaming request: NeoPi is stream-only, so the
// stream is aggregated into one generateContent response.
func (e *NeoPiGoogleExecutor) Execute(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (result cliproxyexecutor.Response, err error) {
	ctx = helps.EnsureSessionContext(ctx, opts, req.Payload)
	if opts.Alt == "responses/compact" {
		return result, statusErr{code: http.StatusNotImplemented, msg: "/responses/compact not supported"}
	}
	prepared, err := e.prepare(ctx, auth, req, opts, false)
	if err != nil {
		return result, err
	}
	reporter := helps.NewExecutorUsageReporter(ctx, e, prepared.model, auth)
	defer reporter.TrackFailure(ctx, &err)
	client := reporter.TrackHTTPClient(helps.NewProxyAwareHTTPClient(ctx, e.cfg, auth, 0))
	var chunks [][]byte
	var streamUsage helps.StreamUsageBuffer
	for attempt := 0; ; attempt++ {
		chunks = nil
		finish := ""
		response, errOpen := e.open(ctx, auth, prepared, client)
		if errOpen != nil {
			return result, errOpen
		}
		_, errScan := scanGeminiStream(ctx, e.cfg, response.Body, func(chunk geminiChunk) error {
			if len(chunk.payload) > 0 {
				if detail, ok := helps.ParseGeminiStreamUsage(chunk.payload); ok {
					streamUsage.Observe(detail, true)
				}
			}
			if chunk.err != nil {
				return chunk.err
			}
			if len(chunk.payload) > 0 {
				chunks = append(chunks, bytes.Clone(chunk.payload))
			}
			if chunk.finish != "" {
				finish = chunk.finish
			}
			return nil
		})
		response.Body.Close()
		if errScan == nil {
			errScan = geminiFinishError(finish)
		}
		if errScan != nil {
			streamUsage.PublishFailure(ctx, reporter, errScan)
			return result, errScan
		}
		if finish == "MAX_TOKENS" || geminiChunksMeaningful(chunks) {
			break
		}
		if attempt >= maxEmptyStreamRetries {
			return result, statusErr{code: http.StatusBadGateway, msg: fmt.Sprintf("Google API returned an empty response (finishReason STOP with no content) after %d attempts", maxEmptyStreamRetries+1)}
		}
		if errWait := sleepContext(ctx, emptyStreamBaseDelay<<attempt); errWait != nil {
			return result, errWait
		}
	}
	data := geminiAggregateChunks(chunks)
	reporter.ObserveResponseModel(data)
	format := cliproxyexecutor.ResponseFormatOrSource(opts)
	var param any
	out := sdktranslator.TranslateNonStream(ctx, sdktranslator.FormatGemini, format, req.Model, helps.ApplyPatchOriginalRequest(req, opts), prepared.body, data, &param)
	if helps.ApplyPatchTranslationError(param) != nil || len(out) == 0 {
		return result, statusErr{code: http.StatusBadGateway, msg: helps.ApplyPatchUpstreamErrorMessage}
	}
	if errPayload, okErr := helps.TranslatedClaudeErrorPayload(format, out); okErr {
		err = statusErr{code: http.StatusBadGateway, msg: errPayload}
		reporter.PublishFailureWithDetail(ctx, helps.ParseGeminiUsage(data), err)
		return result, err
	}
	reporter.Publish(ctx, helps.ParseGeminiUsage(data))
	if format == sdktranslator.FormatOpenAIResponse {
		out = helps.EnsureResponsesUsageDetails(out)
	}
	log.WithFields(log.Fields{"provider": e.Identifier(), "model": prepared.model}).Info("provider request completed")
	return cliproxyexecutor.Response{Payload: out}, nil
}

// ExecuteStream relays NeoPi's generateContent stream, retrying the bounded
// number of times NeoPi does when a STOP arrives with no content.
func (e *NeoPiGoogleExecutor) ExecuteStream(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (_ *cliproxyexecutor.StreamResult, err error) {
	ctx = helps.EnsureSessionContext(ctx, opts, req.Payload)
	if opts.Alt == "responses/compact" {
		return nil, statusErr{code: http.StatusNotImplemented, msg: "/responses/compact not supported"}
	}
	prepared, err := e.prepare(ctx, auth, req, opts, true)
	if err != nil {
		return nil, err
	}
	reporter := helps.NewExecutorUsageReporter(ctx, e, prepared.model, auth)
	defer reporter.TrackFailure(ctx, &err)
	client := reporter.TrackHTTPClient(helps.NewProxyAwareHTTPClient(ctx, e.cfg, auth, 0))
	out := make(chan cliproxyexecutor.StreamChunk, 1)
	go func() {
		defer close(out)
		defer reporter.EnsurePublished(ctx)
		responseFormat := cliproxyexecutor.ResponseFormatOrSource(opts)
		to := sdktranslator.FormatGemini
		original := helps.ApplyPatchOriginalRequest(req, opts)
		gatewayErr := statusErr{code: http.StatusBadGateway, msg: helps.ApplyPatchUpstreamErrorMessage}
		var streamUsage helps.StreamUsageBuffer
		defer streamUsage.Publish(ctx, reporter)
		var retained error
		defer func() { _ = helps.StopTranslatedClaudeStream(ctx, &retained, reporter, &streamUsage, out) }()
		var param any
		helps.InitializeApplyPatchStream(ctx, to, responseFormat, req.Model, original, prepared.body, &param)
		emit := func(payload []byte) error {
			lines := sdktranslator.TranslateStream(ctx, to, responseFormat, req.Model, original, prepared.body, bytes.Clone(payload), &param)
			helps.RecordApplyPatchStreamFailure(ctx, param, reporter, gatewayErr)
			helps.CaptureTranslatedClaudeStreamError(responseFormat, lines, &retained, func(msg string) error {
				return statusErr{code: http.StatusBadGateway, msg: msg}
			})
			for i := range lines {
				select {
				case out <- cliproxyexecutor.StreamChunk{Payload: bytes.Clone(lines[i])}:
				case <-ctx.Done():
					return ctx.Err()
				}
			}
			if helps.ApplyPatchTranslationError(param) != nil {
				return errApplyPatchStop
			}
			return nil
		}
		var streamErr error
		for attempt := 0; ; attempt++ {
			meaningful := false
			finish := ""
			response, errOpen := e.open(ctx, auth, prepared, client)
			if errOpen != nil {
				streamErr = errOpen
				break
			}
			meaningful, streamErr = scanGeminiStream(ctx, e.cfg, response.Body, func(chunk geminiChunk) error {
				if len(chunk.payload) > 0 {
					if detail, ok := helps.ParseGeminiStreamUsage(chunk.payload); ok {
						streamUsage.Observe(detail, true)
					}
				}
				if chunk.err != nil {
					return chunk.err
				}
				if len(chunk.payload) == 0 {
					return nil
				}
				if chunk.finish != "" {
					finish = chunk.finish
				}
				reporter.ObserveResponseModel(chunk.payload)
				return emit(chunk.payload)
			})
			response.Body.Close()
			if errors.Is(streamErr, errApplyPatchStop) {
				helps.StopApplyPatchStream(ctx, param, reporter, out, gatewayErr)
				return
			}
			if streamErr == nil {
				streamErr = geminiFinishError(finish)
			}
			if streamErr != nil {
				break
			}
			if finish == "MAX_TOKENS" || meaningful {
				break
			}
			if attempt >= maxEmptyStreamRetries {
				streamErr = statusErr{code: http.StatusBadGateway, msg: fmt.Sprintf("Google API returned an empty response (finishReason STOP with no content) after %d attempts", maxEmptyStreamRetries+1)}
				break
			}
			if errWait := sleepContext(ctx, emptyStreamBaseDelay<<attempt); errWait != nil {
				streamErr = errWait
				break
			}
		}
		if streamErr != nil {
			if ctx.Err() == nil && helps.EndApplyPatchStream(ctx, param, reporter, out, gatewayErr) {
				return
			}
			helps.RecordAPIResponseError(ctx, e.cfg, streamErr)
			if !streamUsage.PublishFailure(ctx, reporter, streamErr) {
				reporter.PublishFailure(ctx, streamErr)
			}
			select {
			case out <- cliproxyexecutor.StreamChunk{Err: streamErr}:
			case <-ctx.Done():
			}
			return
		}
		if ctx.Err() != nil {
			return
		}
		if emit([]byte("[DONE]")) != nil {
			if errors.Is(ctx.Err(), context.Canceled) || ctx.Err() != nil {
				return
			}
			helps.StopApplyPatchStream(ctx, param, reporter, out, gatewayErr)
			return
		}
		if helps.EndApplyPatchStream(ctx, param, reporter, out, gatewayErr) {
			return
		}
		if helps.StopTranslatedClaudeStream(ctx, &retained, reporter, &streamUsage, out) {
			return
		}
		log.WithFields(log.Fields{"provider": e.Identifier(), "model": prepared.model}).Info("provider request completed")
	}()
	return &cliproxyexecutor.StreamResult{Chunks: out}, nil
}

// errApplyPatchStop ends a stream whose apply_patch validation failed.
var errApplyPatchStop = errors.New("apply_patch translation failed")

// geminiFinishError is consumeGoogleStream's post-loop check: a stream must end
// with a finish reason, and only STOP and MAX_TOKENS are benign (every other
// reason, including SAFETY and MALFORMED_FUNCTION_CALL, surfaces as an error).
func geminiFinishError(finish string) error {
	switch finish {
	case "STOP", "MAX_TOKENS":
		return nil
	case "":
		return statusErr{code: http.StatusBadGateway, msg: "Google API stream ended without a finish reason (connection dropped or response truncated)"}
	}
	return statusErr{code: http.StatusBadGateway, msg: "Generation failed with finish reason: " + finish}
}

func sleepContext(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func geminiChunksMeaningful(chunks [][]byte) bool {
	for _, payload := range chunks {
		if readGeminiChunk(payload).meaningful {
			return true
		}
	}
	return false
}

// geminiAggregateChunks folds a generateContent SSE stream into the single
// response a non-streaming client expects.
func geminiAggregateChunks(chunks [][]byte) []byte {
	var parts []jsjson.Field
	type textRun struct {
		index     int
		thought   bool
		signature string
	}
	var runs []textRun
	var text strings.Builder
	flushText := func() {
		if len(runs) == 0 {
			return
		}
		run := runs[len(runs)-1]
		fields := []jsjson.Field{}
		if run.thought {
			fields = append(fields, jsjson.Field{Name: "thought", Value: []byte("true")})
		}
		fields = append(fields, jsjson.Field{Name: "text", Value: jsjson.Marshal(text.String())})
		if run.signature != "" {
			fields = append(fields, jsjson.Field{Name: "thoughtSignature", Value: jsjson.Marshal(run.signature)})
		}
		parts[run.index] = jsjson.Field{Name: "", Value: jsjson.Object(fields...)}
		text.Reset()
		runs = nil
	}
	var finish, blockReason, blockMessage string
	var usage json.RawMessage
	for _, payload := range chunks {
		root := gjson.ParseBytes(payload)
		if value := root.Get("usageMetadata"); value.IsObject() {
			usage = json.RawMessage(value.Raw)
		}
		if value := root.Get("promptFeedback.blockReason"); value.Type == gjson.String {
			blockReason = value.String()
			blockMessage = root.Get("promptFeedback.blockReasonMessage").String()
		}
		candidate := root.Get("candidates.0")
		if value := candidate.Get("finishReason"); value.Type == gjson.String {
			finish = value.String()
		}
		for _, part := range candidate.Get("content.parts").Array() {
			if part.Get("text").Type == gjson.String && !part.Get("functionCall").Exists() && !part.Get("inlineData").Exists() && !part.Get("fileData").Exists() {
				thought := part.Get("thought").Type == gjson.True
				signature := part.Get("thoughtSignature").String()
				if last := len(runs) - 1; last >= 0 && runs[last].thought == thought {
					text.WriteString(part.Get("text").String())
					if signature != "" {
						runs[last].signature = signature
					}
					continue
				}
				flushText()
				runs = append(runs, textRun{index: len(parts), thought: thought, signature: signature})
				text.WriteString(part.Get("text").String())
				parts = append(parts, jsjson.Field{})
				continue
			}
			flushText()
			parts = append(parts, jsjson.Field{Name: "", Value: jsjson.Normalize([]byte(part.Raw))})
		}
	}
	flushText()
	rawParts := make([]json.RawMessage, 0, len(parts))
	for _, part := range parts {
		rawParts = append(rawParts, part.Value)
	}
	candidate := []jsjson.Field{}
	if len(rawParts) > 0 {
		candidate = append(candidate, jsjson.Field{Name: "content", Value: jsjson.Object(
			jsjson.Field{Name: "role", Value: jsjson.Marshal("model")},
			jsjson.Field{Name: "parts", Value: jsjson.Marshal(rawParts)})})
	}
	if finish != "" {
		candidate = append(candidate, jsjson.Field{Name: "finishReason", Value: jsjson.Marshal(finish)})
		candidate = append(candidate, jsjson.Field{Name: "index", Value: []byte("0")})
	}
	fields := []jsjson.Field{}
	if len(candidate) > 0 {
		fields = append(fields, jsjson.Field{Name: "candidates", Value: jsjson.Marshal([]json.RawMessage{jsjson.Object(candidate...)})})
	}
	if blockReason != "" {
		promptFeedback := []jsjson.Field{{Name: "blockReason", Value: jsjson.Marshal(blockReason)}}
		if blockMessage != "" {
			promptFeedback = append(promptFeedback, jsjson.Field{Name: "blockReasonMessage", Value: jsjson.Marshal(blockMessage)})
		}
		fields = append(fields, jsjson.Field{Name: "promptFeedback", Value: jsjson.Object(promptFeedback...)})
	}
	if usage != nil {
		fields = append(fields, jsjson.Field{Name: "usageMetadata", Value: usage})
	}
	return jsjson.Object(fields...)
}

// FetchGoogleModels is NeoPi's fetchGeminiModels: GET /v1beta/models?key=… with
// pageSize 100, up to 25 pages, de-duplicated by id. NeoPi treats a transport
// or protocol failure as "no answer" and keeps its bundled rows, so a failure
// here is returned as an error (the discovery cache keeps the bundle) and a
// listing without usable rows yields the bundle unchanged.
func FetchGoogleModels(ctx context.Context, cfg *config.Config, auth *cliproxyauth.Auth) ([]*registry.ModelInfo, error) {
	key := geminiAPIKey(auth)
	if key == "" {
		return nil, fmt.Errorf("gemini models: credential has no API key")
	}
	base := geminiNeoPiBaseURL(auth, nil)
	client := helps.NewProxyAwareHTTPClient(ctx, cfg, auth, 0)
	var listing []gjson.Result
	seen := map[string]bool{}
	token := ""
	for page := 0; page < 25; page++ {
		url := base + "/models?key=" + neturl.QueryEscape(key) + "&pageSize=100"
		if token != "" {
			url += "&pageToken=" + neturl.QueryEscape(token)
		}
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return nil, err
		}
		response, err := client.Do(request)
		if err != nil {
			return nil, fmt.Errorf("gemini models: %w", err)
		}
		body, errRead := io.ReadAll(io.LimitReader(response.Body, 8<<20))
		response.Body.Close()
		if errRead != nil {
			return nil, fmt.Errorf("gemini models: %w", errRead)
		}
		if response.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("gemini models: %s returned HTTP %d", base+"/models", response.StatusCode)
		}
		payload := gjson.ParseBytes(body)
		for _, item := range payload.Get("models").Array() {
			listing = append(listing, item)
		}
		next := strings.TrimSpace(payload.Get("nextPageToken").String())
		if next == "" || seen[next] {
			break
		}
		seen[next] = true
		token = next
	}
	models := registry.GoogleModels(listing)
	if len(models) == 0 {
		return nil, fmt.Errorf("gemini models: %s listed no usable models", base+"/models")
	}
	return models, nil
}

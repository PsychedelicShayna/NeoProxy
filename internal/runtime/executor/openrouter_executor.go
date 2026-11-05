package executor

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/neopiretry"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/openaiwire"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/thinking"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tidwall/gjson"
)

// openRouterDialect is the wire dialect the proxy's thinking pipeline treats an
// OpenRouter request as: the Codex-shaped Responses form NeoPi's builder
// reshapes into OpenRouter's body.
const openRouterDialect = "codex"

// OpenRouterExecutor serves an OpenRouter key (a pasted key or a sign-in's
// durable key) the way NeoPi's `openrouter` provider does: OpenAI Responses at
// https://openrouter.ai/api/v1/responses, with NeoPi's attribution headers, its
// reasoning policy and its session routing. NeoPi has no native Anthropic or
// Chat Completions path for OpenRouter, so every request is translated to
// Responses first.
type OpenRouterExecutor struct {
	*ClaudeExecutor
	cfg *config.Config
	// counter estimates tokens locally; NeoPi never asks OpenRouter to count.
	counter *OpenAICompatExecutor
}

// NewOpenRouterExecutor binds the executor to an OpenRouter account's provider key.
func NewOpenRouterExecutor(provider string, cfg *config.Config) *OpenRouterExecutor {
	base := NewClaudeExecutor(cfg)
	base.provider = provider
	return &OpenRouterExecutor{ClaudeExecutor: base, cfg: cfg, counter: NewOpenAICompatExecutor(provider, cfg)}
}

// UsesConfig reports whether the executor was built for cfg.
func (e *OpenRouterExecutor) UsesConfig(cfg *config.Config) bool {
	return e != nil && e.cfg == cfg && (e.ClaudeExecutor == nil || e.ClaudeExecutor.cfg == cfg)
}

// SupportsNativeToolSearch keeps OpenRouter out of Anthropic's native tool search.
func (e *OpenRouterExecutor) SupportsNativeToolSearch(*cliproxyauth.Auth, string) bool { return false }

// Refresh is a no-op: an OpenRouter key (pasted or issued at sign-in) never expires.
func (e *OpenRouterExecutor) Refresh(_ context.Context, auth *cliproxyauth.Auth) (*cliproxyauth.Auth, error) {
	return auth, nil
}

// CountTokens estimates locally from the request's Chat Completions form.
func (e *OpenRouterExecutor) CountTokens(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return e.counter.CountTokens(ctx, auth, req, opts)
}

func (e *OpenRouterExecutor) Execute(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (resp cliproxyexecutor.Response, err error) {
	ctx = helps.EnsureSessionContext(ctx, opts, req.Payload)
	model := openRouterNeoPiModel(auth, req)
	if model == nil {
		return resp, statusErr{code: http.StatusBadRequest, msg: e.Identifier() + ": model " + req.Model + " is not in NeoPi's OpenRouter catalog"}
	}
	return e.executeOpenRouter(ctx, auth, model, req, opts)
}

func (e *OpenRouterExecutor) ExecuteStream(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	ctx = helps.EnsureSessionContext(ctx, opts, req.Payload)
	model := openRouterNeoPiModel(auth, req)
	if model == nil {
		return nil, statusErr{code: http.StatusBadRequest, msg: e.Identifier() + ": model " + req.Model + " is not in NeoPi's OpenRouter catalog"}
	}
	return e.streamOpenRouter(ctx, auth, model, req, opts)
}

// openRouterAPIKey is the bearer an OpenRouter account authenticates with.
func openRouterAPIKey(auth *cliproxyauth.Auth) string {
	if auth == nil {
		return ""
	}
	if key := strings.TrimSpace(auth.Attributes["api_key"]); key != "" {
		return key
	}
	key, _ := auth.Metadata["api_key"].(string)
	return strings.TrimSpace(key)
}

// openRouterNeoPiModel returns the NeoPi model an OpenRouter request runs on:
// the account's discovered record, else NeoPi's bundled row. A configured base
// URL other than NeoPi's own OpenRouter host wins over the bundled endpoint.
func openRouterNeoPiModel(auth *cliproxyauth.Auth, req cliproxyexecutor.Request) *registry.NeoPiModel {
	var model *registry.NeoPiModel
	if info, ok := cliproxyauth.ResolvedModelInfo(req); ok && info != nil && info.NeoPi != nil {
		model = info.NeoPi.Clone()
	} else {
		model = registry.OpenRouterNeoPiModel(thinking.ParseSuffix(req.Model).ModelName)
	}
	if model == nil || model.Compat == nil {
		return nil
	}
	if auth != nil {
		if base := strings.TrimSpace(auth.Attributes["base_url"]); base != "" && !registry.IsOpenRouterBaseURL(base) {
			model.BaseURL = strings.TrimRight(base, "/")
		}
	}
	return model
}

// openRouterNeoPiTurn is one OpenRouter chat request shaped by NeoPi's
// openrouter provider, with the proxy state its events need.
type openRouterNeoPiTurn struct {
	request         *openaiwire.Request
	model           *registry.NeoPiModel
	conversation    openaiwire.Context
	options         openaiwire.Options
	apiKey          string
	auth            *cliproxyauth.Auth
	incomingHeaders http.Header
	baseModel       string
	to              sdktranslator.Format
	responseFormat  sdktranslator.Format
	originalPayload []byte
	body            []byte
	applyPatch      *helps.ApplyPatchResponsesState
	reporter        *helps.UsageReporter
	claudeTokens    *helps.ClaudeInputTokenState
}

// openRouterSessionID is NeoPi's options.sessionId for a downstream request:
// the client's prompt cache identity, else the stable execution session.
func openRouterSessionID(req cliproxyexecutor.Request, opts cliproxyexecutor.Options) string {
	for _, payload := range [][]byte{opts.OriginalRequest, req.Payload} {
		if key := strings.TrimSpace(gjson.GetBytes(payload, "prompt_cache_key").String()); key != "" {
			return key
		}
	}
	return helps.ProviderSessionUUID("openrouter", req.Metadata, opts.Metadata)
}

// shapeOpenRouter translates the downstream request to Responses, applies the
// proxy's thinking and apply_patch normalization, then shapes it as NeoPi does.
// Payload rules run once on the shaped body, inside the transport.
func (e *OpenRouterExecutor) shapeOpenRouter(ctx context.Context, auth *cliproxyauth.Auth, model *registry.NeoPiModel, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (context.Context, *openRouterNeoPiTurn, error) {
	baseModel := thinking.ParseSuffix(req.Model).ModelName
	from := opts.SourceFormat
	to := sdktranslator.FormatCodex
	originalPayload := req.Payload
	if len(opts.OriginalRequest) > 0 {
		originalPayload = opts.OriginalRequest
	}
	originalPayload = bytes.Clone(originalPayload)
	compat := helps.APIKeyModelIsCompat(req)
	originalTranslated := helps.TranslateRequestWithAPIKeyModelCompatibilityForExecutor(ctx, opts.Headers, e.cfg, e.Identifier(), from, to, baseModel, originalPayload, true, compat)
	originalTranslated = helps.SetStringIfDifferent(originalTranslated, "model", baseModel)
	body, updatesChanged, err := helps.TranslateRequestWithAPIKeyModelCompatibilityAndUpdateIntentForExecutor(ctx, opts.Headers, e.cfg, e.Identifier(), from, to, baseModel, bytes.Clone(req.Payload), true, compat)
	if err != nil {
		return ctx, nil, err
	}
	body = helps.SetStringIfDifferent(body, "model", baseModel)
	if body, err = helps.ApplyRequestThinking(body, req, opts, from.String(), openRouterDialect, e.Identifier(), updatesChanged); err != nil {
		return ctx, nil, err
	}
	applyPatch := helps.NewApplyPatchResponsesState(from, originalPayload, originalTranslated)
	if body, err = helps.NormalizeApplyPatchResponsesRequest(body, originalPayload); err != nil {
		return ctx, nil, err
	}
	ctx = helps.WithPayloadFinalizer(ctx, helps.NewPayloadFinalizer(e.cfg, e.Identifier(), baseModel, to.String(), "", originalTranslated, req, opts))
	conversation, options := openaiwire.FromResponses(body, model)
	options.SessionID = openRouterSessionID(req, opts)
	options.DisableStrictTools = openRouterStrictDisabled.Load(openRouterStrictScope(auth, options.SessionID, model))
	responseFormat := cliproxyexecutor.ResponseFormatOrSource(opts)
	key := openRouterAPIKey(auth)
	turn := &openRouterNeoPiTurn{
		model:           model,
		conversation:    conversation,
		options:         options,
		apiKey:          key,
		auth:            auth,
		incomingHeaders: opts.Headers,
		baseModel:       baseModel,
		to:              to,
		responseFormat:  responseFormat,
		originalPayload: originalPayload,
		body:            body,
		applyPatch:      applyPatch,
		claudeTokens:    helps.NewClaudeInputTokenState(from, to, responseFormat, originalPayload),
	}
	turn.request = turn.shape(options)
	return ctx, turn, nil
}

// shape builds the upstream request for options as NeoPi's builder does.
// Headers configured on the credential fill in what NeoPi's own set does not
// carry; NeoPi's identity headers always win.
func (t *openRouterNeoPiTurn) shape(options openaiwire.Options) *openaiwire.Request {
	shaped := openaiwire.ShapeResponses(t.model, t.conversation, options, t.apiKey)
	applyOpenRouterCustomHeaders(&http.Request{Header: shaped.Header}, t.auth, t.incomingHeaders)
	return shaped
}

// openRouterStrictDisabled remembers, per account and client session, the
// models whose provider refused strict tools (disableStrictToolsForScope):
// later turns of that session skip the failed attempt. NeoPi keeps this in
// the agent session's provider state, so a request without a session id
// remembers nothing, and no other session or account inherits it.
var openRouterStrictDisabled = &strictScopes{scopes: map[string]struct{}{}}

// strictScopesLimit bounds the remembered scopes: sessions never announce
// their end here, so the set is dropped whole once it grows past the limit.
const strictScopesLimit = 4096

type strictScopes struct {
	mu     sync.Mutex
	scopes map[string]struct{}
}

func (s *strictScopes) Load(scope string) bool {
	if scope == "" {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.scopes[scope]
	return ok
}

func (s *strictScopes) Store(scope string) {
	if scope == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.scopes) >= strictScopesLimit {
		clear(s.scopes)
	}
	s.scopes[scope] = struct{}{}
}

// openRouterStrictScope keys the strict-tools fallback by account, session
// and model; empty without a session.
func openRouterStrictScope(auth *cliproxyauth.Auth, sessionID string, model *registry.NeoPiModel) string {
	if sessionID == "" {
		return ""
	}
	authID := ""
	if auth != nil {
		authID = auth.ID
	}
	return authID + "\x00" + sessionID + "\x00" + model.Provider + ":" + model.BaseURL + ":" + model.ID
}

// applyOpenRouterCustomHeaders applies the account's configured headers without
// displacing the ones NeoPi sets for every request.
func applyOpenRouterCustomHeaders(request *http.Request, auth *cliproxyauth.Auth, incoming http.Header) {
	if auth == nil || len(auth.Attributes) == 0 {
		return
	}
	custom := http.Header{}
	util.ApplyCustomHeadersFromAttrs(&http.Request{Header: custom}, auth.Attributes, incoming)
	for name, values := range custom {
		if len(request.Header.Values(name)) > 0 {
			continue
		}
		for _, value := range values {
			request.Header.Add(name, value)
		}
	}
}

// runOpenRouter sends the turn through NeoPi's transport, relaying raw events.
func (e *OpenRouterExecutor) runOpenRouter(ctx context.Context, auth *cliproxyauth.Auth, turn *openRouterNeoPiTurn, emit func([]byte) error) error {
	do := neoPiDo(helps.NewProxyAwareHTTPClient(ctx, e.cfg, auth, 0))
	helps.RecordAPIRequest(ctx, e.cfg, helps.UpstreamRequestLog{
		URL: turn.request.URL, Method: http.MethodPost, Headers: turn.request.Header.Clone(), Body: turn.request.Body, Provider: e.Identifier(),
	})
	runner := &openaiwire.Turn{
		Request:     turn.request,
		StrictRetry: openaiwire.StrictToolsRetry{GrammarTooLarge: turn.model.Compat.RetryWithoutStrictOnGrammarError, OpenRouter: true},
		WithoutStrictTools: func() *openaiwire.Request {
			options := turn.options
			options.DisableStrictTools = true
			turn.request = turn.shape(options)
			return turn.request
		},
		OnStrictDisabled: func() {
			openRouterStrictDisabled.Store(openRouterStrictScope(turn.auth, turn.options.SessionID, turn.model))
		},
		Finalize: func(body []byte) []byte { return helps.FinalizePayload(ctx, body) },
		Do: func(request *http.Request) (*http.Response, error) {
			response, err := do(request)
			if err == nil {
				helps.RecordAPIResponseMetadata(ctx, e.cfg, response.StatusCode, response.Header.Clone())
			}
			return response, err
		},
	}
	return runner.Run(ctx, emit)
}

// openRouterNeoPiError maps a transport failure onto the proxy's errors,
// carrying OpenRouter's routed-upstream detail the way NeoPi surfaces it.
func openRouterNeoPiError(err error) error {
	var httpErr *openaiwire.HTTPError
	if errors.As(err, &httpErr) {
		return openRouterStatusErr(httpErr.Status, httpErr.Header, httpErr.Body)
	}
	var streamErr *openaiwire.StreamError
	if errors.As(err, &streamErr) {
		if len(streamErr.Event) > 0 {
			return statusErr{code: http.StatusBadGateway, msg: string(streamErr.Event)}
		}
		return statusErr{code: http.StatusRequestTimeout, msg: "openrouter stream error: " + streamErr.Message}
	}
	return err
}

// openRouterStatusErr is NeoPi's HTTP failure surfacing: the routed provider's
// own words live in error.metadata.raw (OpenRouter fronts several upstreams and
// reports a generic sentence at the top level), so they are appended to the
// message the client sees.
func openRouterStatusErr(code int, headers http.Header, body []byte) statusErr {
	message := string(body)
	if raw := strings.TrimSpace(gjson.GetBytes(body, "error.metadata.raw").String()); raw != "" {
		message += "\n" + raw
	}
	retryAfter := openAICompatRetryAfter(code, headers, body, time.Now())
	if code == http.StatusTooManyRequests {
		// NeoPi's extractRetryHint: retry-after-ms, fractional Retry-After,
		// dates and the reset headers all name the cooldown.
		if hint, ok := neopiretry.ExtractRetryHint(headers, string(body), time.Now()); ok {
			delay := time.Duration(hint * float64(time.Millisecond))
			retryAfter = &delay
		}
	}
	return statusErr{code: code, msg: message, retryAfter: retryAfter}
}

// openRouterNeoPiStream translates upstream events for the client.
type openRouterNeoPiStream struct {
	ctx  context.Context
	e    *OpenRouterExecutor
	req  cliproxyexecutor.Request
	turn *openRouterNeoPiTurn
	// streaming clients get the apply_patch bridge's live line preview;
	// non-streaming ones its whole-event transform.
	streaming     bool
	param         any
	outputByIndex map[int64][]byte
	fallback      [][]byte
	// usage is the upstream terminal usage, observed before the apply_patch
	// bridge so a local failure still accounts for it.
	usage helps.StreamUsageBuffer
}

// publish records the turn's usage as a success or a failure.
func (s *openRouterNeoPiStream) publish(failure error) {
	if failure == nil {
		if !s.usage.Publish(s.ctx, s.turn.reporter) {
			s.turn.reporter.EnsurePublished(s.ctx)
		}
		return
	}
	if !s.usage.PublishFailure(s.ctx, s.turn.reporter, failure) {
		s.turn.reporter.PublishFailure(s.ctx, failure)
	}
}

// translate turns one upstream event into client chunks; completed carries
// the patched terminal event for non-streaming callers.
func (s *openRouterNeoPiStream) translate(event []byte) (chunks [][]byte, completed []byte, err error) {
	helps.AppendAPIResponseChunk(s.ctx, s.e.cfg, event)
	switch gjson.GetBytes(event, "type").String() {
	case "response.completed", "response.incomplete", "response.done":
		s.usage.Observe(helps.ParseCodexUsage(event))
	}
	bridgeErr := statusErr{code: http.StatusBadGateway, msg: helps.ApplyPatchUpstreamErrorMessage}
	var lines [][]byte
	var failed bool
	if s.streaming {
		// The bridge's lines carry its one response.failed frame on failure.
		bridged, errBridge := s.turn.applyPatch.Stream(append([]byte("data: "), event...))
		lines, failed = bridged, errBridge != nil
	} else if string(event) == "[DONE]" {
		return nil, nil, nil
	} else {
		events, errBridge := s.turn.applyPatch.Transform(event)
		if errBridge != nil {
			return nil, nil, bridgeErr
		}
		for _, data := range events {
			lines = append(lines, append([]byte("data: "), data...))
		}
	}
	for _, line := range lines {
		if data, isData := bytes.CutPrefix(line, []byte("data:")); isData {
			ending := line[len(bytes.TrimRight(line, "\r\n")):]
			data = bytes.TrimSpace(data)
			s.turn.reporter.ObserveResponseModel(data)
			switch gjson.GetBytes(data, "type").String() {
			case "response.created":
				s.outputByIndex, s.fallback = map[int64][]byte{}, nil
			case "response.output_item.done":
				if s.outputByIndex == nil {
					s.outputByIndex = map[int64][]byte{}
				}
				collectCodexOutputItemDone(data, s.outputByIndex, &s.fallback)
			case "response.completed", "response.incomplete", "response.done":
				data = patchCodexCompletedOutput(data, s.outputByIndex, s.fallback)
				completed = data
				line = append(append([]byte("data: "), data...), ending...)
			}
		}
		chunks = append(chunks, helps.TranslateStreamWithClaudeInputTokens(s.ctx, s.turn.to, s.turn.responseFormat, s.req.Model, s.turn.originalPayload, s.turn.body, line, &s.param, s.turn.claudeTokens)...)
	}
	if failed || s.streaming && helps.ApplyPatchTranslationError(s.param) != nil {
		return chunks, completed, bridgeErr
	}
	return chunks, completed, nil
}

// executeOpenRouter runs a non-streaming request and translates the completed response.
func (e *OpenRouterExecutor) executeOpenRouter(ctx context.Context, auth *cliproxyauth.Auth, model *registry.NeoPiModel, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (resp cliproxyexecutor.Response, err error) {
	ctx, turn, err := e.shapeOpenRouter(ctx, auth, model, req, opts)
	if err != nil {
		return resp, err
	}
	turn.reporter = helps.NewExecutorUsageReporter(ctx, e, turn.baseModel, auth)
	defer turn.reporter.TrackFailure(ctx, &err)
	turn.reporter.SetTranslatedReasoningEffort(helps.FinalizePayload(ctx, turn.request.Body), openRouterDialect)
	stream := &openRouterNeoPiStream{ctx: ctx, e: e, req: req, turn: turn}
	var completed []byte
	errRun := e.runOpenRouter(ctx, auth, turn, func(event []byte) error {
		_, done, errTranslate := stream.translate(event)
		if done != nil {
			completed = done
		}
		return errTranslate
	})
	fail := func(failure error) (cliproxyexecutor.Response, error) {
		stream.publish(failure)
		return cliproxyexecutor.Response{}, failure
	}
	if errRun != nil && !openaiwire.IsIncompleteStream(errRun) {
		mapped := openRouterNeoPiError(errRun)
		helps.RecordAPIResponseError(ctx, e.cfg, mapped)
		return fail(mapped)
	}
	// A stream that ended inside an apply_patch call fails as the bridge's 502.
	if errFinish := turn.applyPatch.Finish(); errFinish != nil {
		return fail(statusErr{code: http.StatusBadGateway, msg: helps.ApplyPatchUpstreamErrorMessage})
	}
	if errRun != nil {
		mapped := openRouterNeoPiError(errRun)
		helps.RecordAPIResponseError(ctx, e.cfg, mapped)
		return fail(mapped)
	}
	if completed == nil {
		return fail(statusErr{code: http.StatusRequestTimeout, msg: "openrouter stream error: stream disconnected before response.completed or response.incomplete"})
	}
	// OpenRouter can report a native upstream failure as "completed" with no
	// output; only the generation metadata names it (NeoPi's empty-stop recovery).
	if failure := e.readOpenRouterGenerationError(ctx, auth, turn, completed); failure != nil {
		return fail(failure)
	}
	var param any
	out := sdktranslator.TranslateNonStream(ctx, turn.to, turn.responseFormat, req.Model, turn.originalPayload, turn.body, completed, &param)
	if helps.ApplyPatchTranslationError(param) != nil || len(out) == 0 {
		return fail(statusErr{code: http.StatusBadGateway, msg: helps.ApplyPatchUpstreamErrorMessage})
	}
	stream.publish(nil)
	if turn.responseFormat == sdktranslator.FormatOpenAIResponse {
		out = helps.EnsureResponsesUsageDetails(out)
	}
	return cliproxyexecutor.Response{Payload: out, Headers: http.Header{}}, nil
}

// streamOpenRouter runs the turn and returns once the first event reaches the
// client, so failures before that point can fail over to another credential.
func (e *OpenRouterExecutor) streamOpenRouter(ctx context.Context, auth *cliproxyauth.Auth, model *registry.NeoPiModel, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (_ *cliproxyexecutor.StreamResult, err error) {
	ctx, turn, err := e.shapeOpenRouter(ctx, auth, model, req, opts)
	if err != nil {
		return nil, err
	}
	turn.reporter = helps.NewExecutorUsageReporter(ctx, e, turn.baseModel, auth)
	defer turn.reporter.TrackFailure(ctx, &err)
	turn.reporter.SetTranslatedReasoningEffort(helps.FinalizePayload(ctx, turn.request.Body), openRouterDialect)
	stream := &openRouterNeoPiStream{ctx: ctx, e: e, req: req, turn: turn, streaming: true}
	out := make(chan cliproxyexecutor.StreamChunk, 64)
	started := make(chan error, 1)
	var once sync.Once
	committed := false
	start := func(errStart error) { once.Do(func() { started <- errStart }) }
	send := func(chunk cliproxyexecutor.StreamChunk) error {
		select {
		case out <- chunk:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	// An empty "completed" may hide a native failure (NeoPi's empty-stop
	// recovery); its frames wait until GET /generation clears it.
	var heldCompleted []byte
	var held [][]byte
	emit := func(event []byte) error {
		chunks, done, errTranslate := stream.translate(event)
		if errTranslate != nil && len(chunks) == 0 {
			return errTranslate
		}
		if done != nil && openRouterEmptyCompleted(done) {
			heldCompleted, held = done, append(held, chunks...)
			return errTranslate
		}
		committed = true
		start(nil)
		for _, chunk := range chunks {
			if errSend := send(cliproxyexecutor.StreamChunk{Payload: chunk}); errSend != nil {
				return errSend
			}
		}
		// A retained apply_patch failure ends the stream after its one frame.
		return errTranslate
	}
	turn.reporter.StartResponseTTFT()
	go func() {
		defer close(out)
		errRun := e.runOpenRouter(ctx, auth, turn, emit)
		if errRun == nil || openaiwire.IsIncompleteStream(errRun) {
			// FinishStream frames are complete SSE lines carrying the local
			// apply_patch failure, emitted once before the error; a stream
			// ending inside an apply_patch call fails as the bridge's 502.
			tail, errTail := turn.applyPatch.FinishStream()
			for _, line := range tail {
				for _, chunk := range helps.TranslateStreamWithClaudeInputTokens(ctx, turn.to, turn.responseFormat, req.Model, turn.originalPayload, turn.body, line, &stream.param, turn.claudeTokens) {
					committed = true
					start(nil)
					_ = send(cliproxyexecutor.StreamChunk{Payload: chunk})
				}
			}
			if errTail != nil {
				errRun = statusErr{code: http.StatusBadGateway, msg: helps.ApplyPatchUpstreamErrorMessage}
			}
		}
		if errRun == nil && heldCompleted != nil {
			errRun = e.readOpenRouterGenerationError(ctx, auth, turn, heldCompleted)
			if errRun == nil {
				for _, chunk := range held {
					committed = true
					start(nil)
					_ = send(cliproxyexecutor.StreamChunk{Payload: chunk})
				}
			}
		}
		if errRun == nil {
			stream.publish(nil)
			start(nil)
			return
		}
		mapped := openRouterNeoPiError(errRun)
		helps.RecordAPIResponseError(ctx, e.cfg, mapped)
		stream.publish(mapped)
		if !committed {
			start(mapped)
			return
		}
		_ = send(cliproxyexecutor.StreamChunk{Err: mapped})
	}()
	if errStart := <-started; errStart != nil {
		return nil, errStart
	}
	return &cliproxyexecutor.StreamResult{Headers: http.Header{}, Chunks: out}, nil
}

// openRouterEmptyCompleted reports whether a terminal event is a
// "response.completed" with an id and no output: the empty stop NeoPi
// checks against the generation metadata.
func openRouterEmptyCompleted(completed []byte) bool {
	response := gjson.GetBytes(completed, "response")
	output := response.Get("output")
	return gjson.GetBytes(completed, "type").String() == "response.completed" && response.Get("id").String() != "" &&
		!(output.IsArray() && len(output.Array()) > 0)
}

// readOpenRouterGenerationError is NeoPi's OpenRouter empty-stop recovery: a
// completed response with no output can hide a native upstream failure, which
// only GET /generation?id= reports. NeoPi bounds the lookup at 2s; the
// repository allows no deadline once a connection is up (AGENTS.md), so the
// request's own context governs it.
func (e *OpenRouterExecutor) readOpenRouterGenerationError(ctx context.Context, auth *cliproxyauth.Auth, turn *openRouterNeoPiTurn, completed []byte) error {
	if !openRouterEmptyCompleted(completed) {
		return nil
	}
	response := gjson.GetBytes(completed, "response")
	id := response.Get("id").String()
	base := strings.TrimSuffix(turn.request.URL, "/responses")
	request, errRequest := http.NewRequestWithContext(ctx, http.MethodGet, base+"/generation?id="+url.QueryEscape(id), nil)
	if errRequest != nil {
		return nil
	}
	request.Header = turn.request.Header.Clone()
	client := helps.NewProxyAwareHTTPClient(ctx, e.cfg, auth, 0)
	generation, errDo := client.Do(request)
	if errDo != nil {
		return nil
	}
	defer func() { _ = generation.Body.Close() }()
	data, errRead := io.ReadAll(generation.Body)
	if errRead != nil {
		return nil
	}
	if generation.StatusCode != http.StatusOK {
		if generation.StatusCode != http.StatusNotFound {
			return nil
		}
		// Metadata lag: non-reasoning output usage means the upstream produced
		// text the response never carried.
		outputTokens := response.Get("usage.output_tokens").Int()
		reasoningTokens := response.Get("usage.output_tokens_details.reasoning_tokens").Int()
		if outputTokens-reasoningTokens <= 0 {
			return nil
		}
		return statusErr{code: http.StatusBadGateway, msg: fmt.Sprintf("Provider returned error (inferred MALFORMED_RESPONSE: empty output with non-reasoning usage; generation metadata not ready); response ID: %s", id)}
	}
	detail := gjson.GetBytes(data, "data")
	if detail.Get("id").String() != id {
		return nil
	}
	switch strings.ToUpper(detail.Get("native_finish_reason").String()) {
	case "MALFORMED_RESPONSE", "MALFORMED_FUNCTION_CALL", "UNEXPECTED_TOOL_CALL", "NETWORK_ERROR":
		return statusErr{code: http.StatusBadGateway, msg: fmt.Sprintf("Provider returned error (native_finish_reason: %s); response ID: %s", detail.Get("native_finish_reason").String(), id)}
	}
	return nil
}

package executor

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"

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

// neoPiResponsesSubscriptionUsageEvent is Meta's per-turn quota frame. It is
// not part of the Responses API: NeoPi's stream reader ignores it, and so does
// the relay, so clients never see it.
const neoPiResponsesSubscriptionUsageEvent = "response.subscription_usage"

// neoPiResponsesTurn is one chat request shaped by NeoPi's openai-responses
// provider, with the proxy state its events need.
type neoPiResponsesTurn struct {
	request         *openaiwire.Request
	baseModel       string
	to              sdktranslator.Format
	responseFormat  sdktranslator.Format
	originalPayload []byte
	body            []byte
	applyPatch      *helps.ApplyPatchResponsesState
	reporter        *helps.UsageReporter
	claudeTokens    *helps.ClaudeInputTokenState
}

// neoPiResponsesSessionID is NeoPi's options.sessionId for a downstream
// request: the client's prompt cache identity, else the stable execution session.
func neoPiResponsesSessionID(provider string, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) string {
	for _, payload := range [][]byte{opts.OriginalRequest, req.Payload} {
		if key := strings.TrimSpace(gjson.GetBytes(payload, "prompt_cache_key").String()); key != "" {
			return key
		}
	}
	return helps.ProviderSessionUUID(provider, req.Metadata, opts.Metadata)
}

// shapeNeoPi translates the downstream request to Responses, applies the
// proxy's thinking and apply_patch normalization, then shapes it as NeoPi does.
// Payload rules run once on the shaped body, inside the transport.
func (e *NeoPiResponsesExecutor) shapeNeoPi(ctx context.Context, auth *cliproxyauth.Auth, model *registry.NeoPiModel, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (context.Context, *neoPiResponsesTurn, error) {
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
	if body, err = helps.ApplyRequestThinking(body, req, opts, from.String(), neoPiResponsesDialect, e.Identifier(), updatesChanged); err != nil {
		return ctx, nil, err
	}
	applyPatch := helps.NewApplyPatchResponsesState(from, originalPayload, originalTranslated)
	if body, err = helps.NormalizeApplyPatchResponsesRequest(body, originalPayload); err != nil {
		return ctx, nil, err
	}
	ctx = helps.WithPayloadFinalizer(ctx, helps.NewPayloadFinalizer(e.cfg, e.Identifier(), baseModel, to.String(), "", originalTranslated, req, opts))
	_, token := e.neoPiResponsesCreds(auth)
	conversation, options := openaiwire.FromResponses(body, model)
	options.SessionID = neoPiResponsesSessionID(e.Identifier(), req, opts)
	responseFormat := cliproxyexecutor.ResponseFormatOrSource(opts)
	shaped := openaiwire.ShapeResponses(model, conversation, options, token)
	e.applyTransportHeaders(shaped.Header, auth, model, opts.Headers)
	return ctx, &neoPiResponsesTurn{
		request:         shaped,
		baseModel:       baseModel,
		to:              to,
		responseFormat:  responseFormat,
		originalPayload: originalPayload,
		body:            body,
		applyPatch:      applyPatch,
		claudeTokens:    helps.NewClaudeInputTokenState(from, to, responseFormat, originalPayload),
	}, nil
}

// applyTransportHeaders adds the provider transport's headers (Muse Code's
// x-api-version, registry/muse-code.ts prepareRequest) and then the headers
// configured on the credential, which win as NeoPi's configured provider
// headers do.
func (e *NeoPiResponsesExecutor) applyTransportHeaders(header http.Header, auth *cliproxyauth.Auth, model *registry.NeoPiModel, clientHeaders http.Header) {
	if model.Provider == registry.ResponsesMuseCode {
		header.Set("x-api-version", neoPiResponsesMuseVersion)
	}
	var attrs map[string]string
	if auth != nil {
		attrs = auth.Attributes
	}
	util.ApplyCustomHeadersFromAttrs(&http.Request{Header: header}, attrs, clientHeaders)
}

// runNeoPi sends the turn through NeoPi's transport, relaying raw events.
func (e *NeoPiResponsesExecutor) runNeoPi(ctx context.Context, auth *cliproxyauth.Auth, turn *neoPiResponsesTurn, emit func([]byte) error) error {
	do := neoPiDo(helps.NewProxyAwareHTTPClient(ctx, e.cfg, auth, 0))
	helps.RecordAPIRequest(ctx, e.cfg, helps.UpstreamRequestLog{
		URL: turn.request.URL, Method: http.MethodPost, Headers: turn.request.Header.Clone(), Body: turn.request.Body, Provider: e.Identifier(),
	})
	runner := &openaiwire.Turn{
		Request:  turn.request,
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

// neoPiResponsesError maps a transport failure onto the proxy's credential-aware errors.
func (e *NeoPiResponsesExecutor) neoPiResponsesError(err error) error {
	meta := e.Identifier() == "meta"
	var httpErr *openaiwire.HTTPError
	if errors.As(err, &httpErr) {
		if meta {
			return wrapMetaUpstreamError(httpErr.Status, httpErr.Body)
		}
		return statusErr{code: httpErr.Status, msg: string(httpErr.Body)}
	}
	var streamErr *openaiwire.StreamError
	if errors.As(err, &streamErr) {
		if len(streamErr.Event) > 0 {
			if meta {
				return metaStreamEventError(streamErr.Event)
			}
			return statusErr{code: http.StatusBadGateway, msg: string(streamErr.Event)}
		}
		return statusErr{code: http.StatusRequestTimeout, msg: e.Identifier() + " stream error: " + streamErr.Message}
	}
	return err
}

// neoPiResponsesStream translates upstream events for the client.
type neoPiResponsesStream struct {
	ctx  context.Context
	e    *NeoPiResponsesExecutor
	req  cliproxyexecutor.Request
	turn *neoPiResponsesTurn
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
func (s *neoPiResponsesStream) publish(failure error) {
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
func (s *neoPiResponsesStream) translate(event []byte) (chunks [][]byte, completed []byte, err error) {
	if isMetaSubscriptionUsageEvent(gjson.GetBytes(event, "type").String(), event) {
		return nil, nil, nil
	}
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

// executeNeoPi runs a non-streaming request and translates the completed response.
func (e *NeoPiResponsesExecutor) executeNeoPi(ctx context.Context, auth *cliproxyauth.Auth, model *registry.NeoPiModel, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (resp cliproxyexecutor.Response, err error) {
	ctx, turn, err := e.shapeNeoPi(ctx, auth, model, req, opts)
	if err != nil {
		return resp, err
	}
	turn.reporter = helps.NewExecutorUsageReporter(ctx, e, turn.baseModel, auth)
	defer turn.reporter.TrackFailure(ctx, &err)
	turn.reporter.SetTranslatedReasoningEffort(helps.FinalizePayload(ctx, turn.request.Body), neoPiResponsesDialect)
	stream := &neoPiResponsesStream{ctx: ctx, e: e, req: req, turn: turn}
	var completed []byte
	errRun := e.runNeoPi(ctx, auth, turn, func(event []byte) error {
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
		mapped := e.neoPiResponsesError(errRun)
		helps.RecordAPIResponseError(ctx, e.cfg, mapped)
		return fail(mapped)
	}
	// A stream that ended inside an apply_patch call fails as the bridge's 502.
	if errFinish := turn.applyPatch.Finish(); errFinish != nil {
		return fail(statusErr{code: http.StatusBadGateway, msg: helps.ApplyPatchUpstreamErrorMessage})
	}
	if errRun != nil {
		mapped := e.neoPiResponsesError(errRun)
		helps.RecordAPIResponseError(ctx, e.cfg, mapped)
		return fail(mapped)
	}
	if completed == nil {
		return fail(statusErr{code: http.StatusRequestTimeout, msg: e.Identifier() + " stream error: stream disconnected before response.completed or response.incomplete"})
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

// streamNeoPi runs the turn and returns once the first event reaches the
// client, so failures before that point can fail over to another credential.
func (e *NeoPiResponsesExecutor) streamNeoPi(ctx context.Context, auth *cliproxyauth.Auth, model *registry.NeoPiModel, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (_ *cliproxyexecutor.StreamResult, err error) {
	ctx, turn, err := e.shapeNeoPi(ctx, auth, model, req, opts)
	if err != nil {
		return nil, err
	}
	turn.reporter = helps.NewExecutorUsageReporter(ctx, e, turn.baseModel, auth)
	defer turn.reporter.TrackFailure(ctx, &err)
	turn.reporter.SetTranslatedReasoningEffort(helps.FinalizePayload(ctx, turn.request.Body), neoPiResponsesDialect)
	stream := &neoPiResponsesStream{ctx: ctx, e: e, req: req, turn: turn, streaming: true}
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
	emit := func(event []byte) error {
		chunks, _, errTranslate := stream.translate(event)
		if errTranslate != nil && len(chunks) == 0 {
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
		errRun := e.runNeoPi(ctx, auth, turn, emit)
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
		if errRun == nil {
			stream.publish(nil)
			start(nil)
			return
		}
		mapped := e.neoPiResponsesError(errRun)
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

// executeCompaction answers a compaction trigger the way the proxy always has
// for these hosts: by asking the model for a summary.
func (e *NeoPiResponsesExecutor) executeCompaction(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	var resp cliproxyexecutor.Response
	baseModel := thinking.ParseSuffix(req.Model).ModelName
	summaryPayload := helps.PrepareAntigravityCompactionSummaryPayload(helps.ResponsesCompactionSourcePayload(req, opts), baseModel)
	summaryReq := cliproxyexecutor.Request{Model: req.Model, Payload: summaryPayload, Metadata: req.Metadata}
	summaryOpts := opts
	summaryOpts.Alt = ""
	summaryOpts.Stream = false
	summaryOpts.OriginalRequest = nil
	summaryOpts.SourceFormat = sdktranslator.FormatOpenAIResponse
	summaryOpts.ResponseFormat = sdktranslator.FormatOpenAIResponse
	summaryResp, errSummary := e.Execute(ctx, auth, summaryReq, summaryOpts)
	if errSummary != nil {
		return resp, errSummary
	}
	respPayload, errBuild := helps.BuildResponsesCompactionResponseFromSummary(baseModel, summaryResp.Payload)
	if errBuild != nil {
		return resp, statusErr{code: http.StatusBadGateway, msg: errBuild.Error()}
	}
	return cliproxyexecutor.Response{Payload: respPayload, Headers: summaryResp.Headers}, nil
}

func (e *NeoPiResponsesExecutor) executeCompactionStream(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	baseModel := thinking.ParseSuffix(req.Model).ModelName
	summaryResp, errSummary := e.executeCompaction(ctx, auth, req, opts)
	if errSummary != nil {
		return nil, errSummary
	}
	chunks, errChunks := helps.BuildResponsesCompactionStreamChunksFromResponse(baseModel, summaryResp.Payload)
	if errChunks != nil {
		return nil, statusErr{code: http.StatusBadGateway, msg: errChunks.Error()}
	}
	out := make(chan cliproxyexecutor.StreamChunk, len(chunks))
	for _, chunk := range chunks {
		out <- cliproxyexecutor.StreamChunk{Payload: chunk}
	}
	close(out)
	headers := summaryResp.Headers.Clone()
	if headers == nil {
		headers = make(http.Header)
	}
	headers.Set("Content-Type", "text/event-stream")
	return &cliproxyexecutor.StreamResult{Headers: headers, Chunks: out}, nil
}

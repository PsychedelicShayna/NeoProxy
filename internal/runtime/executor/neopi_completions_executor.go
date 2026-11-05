package executor

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"math"
	"math/rand/v2"
	"net/http"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// NeoPiCompletionsProfile adapts the shared NeoPi Chat Completions port to one
// host: where its models come from, how its key is read, and any provider
// hooks NeoPi applies around the shared builder.
type NeoPiCompletionsProfile struct {
	// Provider is NeoPi's provider id, which keys the exported catalog.
	Provider string
	// Row resolves a model id to NeoPi's record; it defaults to the provider's
	// exported catalog (bundled rows plus NeoPi's discovered-id compat).
	Row func(provider, model string) *registry.NeoPiModel
	// APIKey reads the bearer credential; it defaults to the account's api_key.
	APIKey func(auth *cliproxyauth.Auth) string
	// URL is the request endpoint; it defaults to the row's base URL plus
	// /chat/completions.
	URL func(row *registry.NeoPiModel) string
	// Headers runs after NeoPi's own headers are set, for providers that add
	// identity headers of their own.
	Headers func(ctx context.Context, auth *cliproxyauth.Auth, incoming http.Header, key string, row *registry.NeoPiModel, headers http.Header, sessionID string)
	// Body runs after NeoPi's request shaping, for provider-only body fields.
	Body func(ctx context.Context, auth *cliproxyauth.Auth, row *registry.NeoPiModel, body []byte) ([]byte, error)
	// ModelID transforms the model id already resolved by NeoPi's compat rules.
	ModelID func(ctx context.Context, auth *cliproxyauth.Auth, row *registry.NeoPiModel, modelID string) string
}

// NeoPiCompletionsExecutor serves a NeoPi openai-completions provider: the
// request built by NeoPi's own buildParams, sent with NeoPi's headers and retry
// policy, relayed as NeoPi's stream.
type NeoPiCompletionsExecutor struct {
	provider string
	profile  NeoPiCompletionsProfile
	cfg      *config.Config
	counter  *OpenAICompatExecutor
	random   func() float64
	wait     func(context.Context, time.Duration) error
}

// NewNeoPiCompletionsExecutor binds the shared port to a provider id.
func NewNeoPiCompletionsExecutor(provider string, cfg *config.Config, profile NeoPiCompletionsProfile) *NeoPiCompletionsExecutor {
	if profile.Provider == "" {
		profile.Provider = provider
	}
	return &NeoPiCompletionsExecutor{
		provider: provider,
		profile:  profile,
		cfg:      cfg,
		counter:  NewOpenAICompatExecutor(provider, cfg),
		random:   rand.Float64,
		wait:     neoPiWait,
	}
}

// Identifier is the engine provider key this executor is registered under.
func (e *NeoPiCompletionsExecutor) Identifier() string { return e.provider }

// UsesConfig reports whether the executor was built for cfg.
func (e *NeoPiCompletionsExecutor) UsesConfig(cfg *config.Config) bool {
	return e != nil && e.cfg == cfg
}

// SupportsNativeToolSearch keeps these hosts out of Anthropic's native tool search.
func (e *NeoPiCompletionsExecutor) SupportsNativeToolSearch(*cliproxyauth.Auth, string) bool {
	return false
}

// RequestToFormat reports that every request is shaped in Chat Completions.
func (e *NeoPiCompletionsExecutor) RequestToFormat(cliproxyexecutor.Request, cliproxyexecutor.Options) sdktranslator.Format {
	return sdktranslator.FormatOpenAI
}

// Refresh is a no-op: these providers take long-lived API keys.
func (e *NeoPiCompletionsExecutor) Refresh(_ context.Context, auth *cliproxyauth.Auth) (*cliproxyauth.Auth, error) {
	return auth, nil
}

// HttpRequest sends an arbitrary request with the account's bearer key.
func (e *NeoPiCompletionsExecutor) HttpRequest(ctx context.Context, auth *cliproxyauth.Auth, request *http.Request) (*http.Response, error) {
	if request == nil {
		return nil, fmt.Errorf("%s: request is nil", e.provider)
	}
	if key := e.apiKey(auth); key != "" {
		request.Header.Set("Authorization", "Bearer "+key)
	}
	if auth != nil {
		util.ApplyCustomHeadersFromAttrs(request, auth.Attributes)
	}
	return helps.NewProxyAwareHTTPClient(ctx, e.cfg, auth, 0).Do(request)
}

// CountTokens estimates locally from the Chat Completions form.
func (e *NeoPiCompletionsExecutor) CountTokens(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return e.counter.CountTokens(ctx, auth, req, opts)
}

// Execute runs one non-streaming request through NeoPi's stream-shaped wire.
func (e *NeoPiCompletionsExecutor) Execute(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (result cliproxyexecutor.Response, err error) {
	row := e.row(auth, req)
	prepared, err := e.prepare(ctx, auth, req, opts, row)
	if err != nil {
		return result, err
	}
	reporter := helps.NewExecutorUsageReporter(ctx, e, prepared.model, auth)
	defer reporter.TrackFailure(ctx, &err)
	response, err := e.open(ctx, auth, prepared, reporter.TrackHTTPClient(helps.NewProxyAwareHTTPClient(ctx, e.cfg, auth, 0)))
	if err != nil {
		return result, err
	}
	defer func() { _ = response.Body.Close() }()
	var usage helps.StreamUsageBuffer
	var lines [][]byte
	err = neoPiScanCompletions(ctx, e.cfg, e.provider, response.Body, func(line []byte) error {
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
			return result, fmt.Errorf("%s: response translation failed", e.provider)
		}
	}
	usage.Publish(ctx, reporter)
	log.WithFields(log.Fields{"provider": e.provider, "model": prepared.model}).Info("provider request completed")
	return cliproxyexecutor.Response{Payload: data, Headers: response.Header.Clone()}, nil
}

// ExecuteStream relays NeoPi's upstream stream in the client's format.
func (e *NeoPiCompletionsExecutor) ExecuteStream(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (_ *cliproxyexecutor.StreamResult, err error) {
	row := e.row(auth, req)
	prepared, err := e.prepare(ctx, auth, req, opts, row)
	if err != nil {
		return nil, err
	}
	reporter := helps.NewExecutorUsageReporter(ctx, e, prepared.model, auth)
	defer reporter.TrackFailure(ctx, &err)
	response, err := e.open(ctx, auth, prepared, reporter.TrackHTTPClient(helps.NewProxyAwareHTTPClient(ctx, e.cfg, auth, 0)))
	if err != nil {
		return nil, err
	}
	out := make(chan cliproxyexecutor.StreamChunk, 1)
	go func() {
		defer close(out)
		defer func() { _ = response.Body.Close() }()
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
		consumeErr := neoPiScanCompletions(ctx, e.cfg, e.provider, response.Body, func(line []byte) error {
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
		log.WithFields(log.Fields{"provider": e.provider, "model": prepared.model}).Info("provider request completed")
	}()
	return &cliproxyexecutor.StreamResult{Headers: response.Header.Clone(), Chunks: out}, nil
}

// neoPiCompletionsPrepared is one shaped request ready to post.
type neoPiCompletionsPrepared struct {
	body    []byte
	headers http.Header
	url     string
	model   string
}

// prepare shapes the downstream body exactly as NeoPi does for this row.
func (e *NeoPiCompletionsExecutor) prepare(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options, row *registry.NeoPiModel) (*neoPiCompletionsPrepared, error) {
	if row == nil || row.CompletionsCompat == nil {
		return nil, statusErr{code: http.StatusBadRequest, msg: e.provider + ": model has no NeoPi completions record"}
	}
	key := e.apiKey(auth)
	if key == "" {
		return nil, statusErr{code: http.StatusUnauthorized, msg: e.provider + ": account has no API key"}
	}
	original := req.Payload
	if len(opts.OriginalRequest) > 0 {
		original = opts.OriginalRequest
	}
	originalTranslated, translated, err := helps.TranslateRequestPairReturningError(ctx, opts.Headers, e.cfg, opts.SourceFormat, sdktranslator.FormatOpenAI, row.ID, original, req.Payload, true, helps.APIKeyModelIsCompat(req))
	if err != nil {
		return nil, err
	}
	if translated, err = helps.ApplyRequestThinking(translated, req, opts, opts.SourceFormat.String(), "openai", e.provider); err != nil {
		return nil, err
	}
	sessionID := e.sessionID(req, opts)
	body, err := neoPiCompletionsRequest(translated, row, neoPiCompletionsCall{SessionID: sessionID})
	if err != nil {
		return nil, statusErr{code: http.StatusBadGateway, msg: e.provider + ": " + err.Error()}
	}
	if e.profile.ModelID != nil {
		modelID := e.profile.ModelID(ctx, auth, row, gjson.GetBytes(body, "model").String())
		if body, err = sjson.SetBytes(body, "model", modelID); err != nil {
			return nil, err
		}
	}
	if e.profile.Body != nil {
		if body, err = e.profile.Body(ctx, auth, row, body); err != nil {
			return nil, err
		}
	}
	body = helps.NewPayloadFinalizer(e.cfg, e.provider, row.ID, "openai", "", originalTranslated, req, opts)(body)
	headers := neoPiCompletionsHeaders(ctx, auth, opts.Headers, key, row)
	if e.profile.Headers != nil {
		e.profile.Headers(ctx, auth, opts.Headers, key, row, headers, sessionID)
	}
	return &neoPiCompletionsPrepared{body: body, headers: headers, url: e.url(row), model: row.ID}, nil
}

// sessionID is NeoPi's options.sessionId for a downstream request: the
// client's prompt cache identity, else a stable per-conversation id.
func (e *NeoPiCompletionsExecutor) sessionID(req cliproxyexecutor.Request, opts cliproxyexecutor.Options) string {
	for _, payload := range [][]byte{opts.OriginalRequest, req.Payload} {
		if key := strings.TrimSpace(gjson.GetBytes(payload, "prompt_cache_key").String()); key != "" {
			return key
		}
	}
	return helps.ProviderSessionUUID(e.provider, req.Metadata, opts.Metadata)
}

// open posts the shaped request with NeoPi's transport retry policy: 5xx, 408
// and 429 are retried before any response byte, honouring Retry-After hints up
// to one minute.
func (e *NeoPiCompletionsExecutor) open(ctx context.Context, auth *cliproxyauth.Auth, prepared *neoPiCompletionsPrepared, client *http.Client) (*http.Response, error) {
	const maxAttempts = 6
	for attempt := 0; ; attempt++ {
		request, err := http.NewRequestWithContext(ctx, http.MethodPost, prepared.url, bytes.NewReader(prepared.body))
		if err != nil {
			return nil, err
		}
		request.Header = prepared.headers.Clone()
		info := helps.UpstreamRequestLog{URL: request.URL.String(), Method: request.Method, Headers: request.Header, Body: prepared.body, Provider: e.provider}
		if auth != nil {
			info.AuthID, info.AuthLabel = auth.ID, auth.Label
			info.AuthType, info.AuthValue = auth.AccountInfo()
		}
		helps.RecordAPIRequest(ctx, e.cfg, info)
		response, err := client.Do(request)
		if err != nil {
			helps.RecordAPIResponseError(ctx, e.cfg, err)
			if attempt+1 >= maxAttempts || ctx.Err() != nil {
				return nil, err
			}
			if waitErr := e.wait(ctx, e.retryDelay(attempt, 0, nil)); waitErr != nil {
				return nil, err
			}
			continue
		}
		helps.RecordAPIResponseMetadata(ctx, e.cfg, response.StatusCode, response.Header)
		if response.StatusCode >= 200 && response.StatusCode < 300 {
			decoded, errDecode := util.DecodeResponseBody(response.Body, response.Header.Get("Content-Encoding"))
			if errDecode != nil {
				_ = response.Body.Close()
				return nil, errDecode
			}
			response.Body = decoded
			return response, nil
		}
		decoded, errDecode := util.DecodeResponseBody(response.Body, response.Header.Get("Content-Encoding"))
		var data []byte
		if errDecode == nil {
			data, _ = io.ReadAll(decoded)
			_ = decoded.Close()
		} else {
			_ = response.Body.Close()
		}
		helps.AppendAPIResponseChunk(ctx, e.cfg, data)
		if !neoPiRetryableStatus(response.StatusCode) || attempt+1 >= maxAttempts {
			return nil, &neoPiUpstreamError{code: response.StatusCode, body: string(data), headers: response.Header}
		}
		// NeoPi's transport honours a Retry-After hint up to a minute; a longer
		// one is surfaced instead of slept on.
		hint, ok := neoPiRetryAfter(response.Header, e.now())
		if ok && hint > time.Minute {
			return nil, &neoPiUpstreamError{code: response.StatusCode, body: string(data), headers: response.Header}
		}
		if waitErr := e.wait(ctx, e.retryDelay(attempt, hint, response.Header)); waitErr != nil {
			return nil, &neoPiUpstreamError{code: response.StatusCode, body: string(data), headers: response.Header}
		}
	}
}

// neoPiRetryableStatus is isRetryableStatus.
func neoPiRetryableStatus(status int) bool {
	return status >= 500 || status == http.StatusRequestTimeout || status == http.StatusTooManyRequests
}

// retryDelay is fetchWithRetry's schedule: the server hint when there is one,
// else 500ms doubling per attempt, capped at a minute.
func (e *NeoPiCompletionsExecutor) retryDelay(attempt int, hint time.Duration, _ http.Header) time.Duration {
	if hint > 0 {
		return min(hint, time.Minute)
	}
	return time.Duration(math.Min(float64(500*time.Millisecond)*math.Pow(2, float64(attempt)), float64(time.Minute)))
}

func (e *NeoPiCompletionsExecutor) now() time.Time { return time.Now() }

// apiKey reads the account's bearer credential.
func (e *NeoPiCompletionsExecutor) apiKey(auth *cliproxyauth.Auth) string {
	if e.profile.APIKey != nil {
		return strings.TrimSpace(e.profile.APIKey(auth))
	}
	return neoPiCompletionsAPIKey(auth)
}

// neoPiCompletionsAPIKey is the api_key an imported or pasted key is stored on.
func neoPiCompletionsAPIKey(auth *cliproxyauth.Auth) string {
	if auth == nil {
		return ""
	}
	if key := strings.TrimSpace(auth.Attributes["api_key"]); key != "" {
		return key
	}
	key, _ := auth.Metadata["api_key"].(string)
	return strings.TrimSpace(key)
}

// row resolves the NeoPi record the request must be shaped from.
func (e *NeoPiCompletionsExecutor) row(auth *cliproxyauth.Auth, req cliproxyexecutor.Request) *registry.NeoPiModel {
	var row *registry.NeoPiModel
	if info, ok := cliproxyauth.ResolvedModelInfo(req); ok && info != nil && info.NeoPi != nil && info.NeoPi.API == "openai-completions" {
		row = info.NeoPi
	} else {
		resolve := e.profile.Row
		if resolve == nil {
			resolve = registry.NeoPiCompletionsModel
		}
		row = resolve(e.profile.Provider, req.Model)
	}
	// Token Plan credentials can select China or a custom regional endpoint.
	if row != nil && e.profile.Provider == "alibaba-token-plan" && auth != nil {
		if baseURL := strings.TrimSpace(auth.Attributes["base_url"]); baseURL != "" {
			row = row.Clone()
			row.BaseURL = baseURL
		}
	}
	return row
}

// url is the endpoint NeoPi posts the shaped request to.
func (e *NeoPiCompletionsExecutor) url(row *registry.NeoPiModel) string {
	if e.profile.URL != nil {
		return e.profile.URL(row)
	}
	return strings.TrimRight(row.BaseURL, "/") + "/chat/completions"
}

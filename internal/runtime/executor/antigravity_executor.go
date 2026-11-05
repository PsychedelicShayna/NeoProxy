package executor

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/antigravitywire"
	antigravityauth "github.com/router-for-me/CLIProxyAPI/v8/internal/auth/antigravity"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/googlewire"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/neopiretry"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/thinking"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tidwall/gjson"
)

// AntigravityExecutor serves a Google Antigravity sign-in the way NeoPi's
// google-antigravity provider does: a Cloud Code Assist
// `streamGenerateContent` request built by NeoPi's own request builder, with
// the Antigravity client identity, endpoint failover and empty-response
// retry, answered here as Anthropic Messages events.
type AntigravityExecutor struct {
	cfg *config.Config
	// counter estimates tokens locally; Cloud Code Assist has no counting RPC.
	counter *OpenAICompatExecutor
	sources antigravitywire.Sources
}

// NewAntigravityExecutor builds the Antigravity executor.
func NewAntigravityExecutor(cfg *config.Config) *AntigravityExecutor {
	return &AntigravityExecutor{cfg: cfg, counter: NewOpenAICompatExecutor("antigravity", cfg), sources: antigravitywire.DefaultSources()}
}

// Identifier returns the provider key.
func (e *AntigravityExecutor) Identifier() string { return "antigravity" }

// SupportsApplyPatch reports the executor contract.
func (e *AntigravityExecutor) SupportsApplyPatch() bool { return e != nil }

// UsesConfig reports whether the executor was built for cfg.
func (e *AntigravityExecutor) UsesConfig(cfg *config.Config) bool { return e != nil && e.cfg == cfg }

// SupportsNativeToolSearch keeps Antigravity out of Anthropic's native tool
// search: Cloud Code Assist takes its own function declarations.
func (e *AntigravityExecutor) SupportsNativeToolSearch(*cliproxyauth.Auth, string) bool { return false }

// NeoPi's retry budget for this provider.
const (
	antigravityMaxRetries         = 3
	antigravityBaseDelay          = time.Second
	antigravityRateLimitBudget    = 5 * time.Minute
	antigravityDefaultFirstEvent  = 300 * time.Second
	antigravityClaudeThinkingBeta = "interleaved-thinking-2025-05-14"
	// antigravityHeldTextLimit bounds the whitespace NeoPi's empty-response
	// retry may hold back before the proxy has to stream it.
	antigravityHeldTextLimit = 4096
)

// antigravityCredential is the stored sign-in.
type antigravityCredential struct {
	accessToken  string
	refreshToken string
	projectID    string
	email        string
	accountID    string
	expiresAt    time.Time
	hasExpiry    bool
	baseURL      string
}

func antigravityCredentialOf(auth *cliproxyauth.Auth) antigravityCredential {
	if auth == nil {
		return antigravityCredential{}
	}
	meta := func(keys ...string) string {
		for _, key := range keys {
			if value, ok := auth.Metadata[key].(string); ok && strings.TrimSpace(value) != "" {
				return strings.TrimSpace(value)
			}
		}
		return ""
	}
	credential := antigravityCredential{
		accessToken:  meta("access_token", "access"),
		refreshToken: meta("refresh_token"),
		projectID:    meta("project_id", "projectId"),
		email:        meta("email", "email_address"),
		accountID:    meta("account_id"),
	}
	if expiry, known := auth.ExpirationTime(); known && !expiry.IsZero() {
		credential.expiresAt, credential.hasExpiry = expiry, true
	}
	if auth.Attributes != nil {
		credential.baseURL = strings.TrimSpace(auth.Attributes["base_url"])
	}
	return credential
}

// antigravityModel resolves the NeoPi record discovery registered for the
// account the request was routed to, else NeoPi's bundled record, which NeoPi
// serves until discovery answers.
func antigravityModel(req cliproxyexecutor.Request, modelID string) *registry.NeoPiModel {
	if info, ok := cliproxyauth.ResolvedModelInfo(req); ok && info != nil && info.NeoPi != nil && info.NeoPi.API == "google-gemini-cli" {
		return info.NeoPi
	}
	return registry.AntigravityBundledNeoPiModel(modelID)
}

// antigravityPrepared is one request shaped the way NeoPi sends it.
type antigravityPrepared struct {
	model      *registry.NeoPiModel
	credential antigravityCredential
	// body is the finalized Cloud Code Assist envelope.
	body []byte
	// translated is the Claude-dialect request the body was built from.
	translated []byte
	endpoints  []string
	state      *antigravitywire.SessionState
	firstEvent time.Duration
	// modelName is the client-facing model id, echoed in message events.
	modelName string
}

// prepare shapes the request from the client's body, NeoPi's model record and
// the sign-in's identity.
func (e *AntigravityExecutor) prepare(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (*antigravityPrepared, error) {
	credential := antigravityCredentialOf(auth)
	if credential.accessToken == "" {
		return nil, statusErr{code: http.StatusUnauthorized, msg: "antigravity: account has no access token"}
	}
	if credential.projectID == "" {
		return nil, statusErr{code: http.StatusBadRequest,
			msg: "Missing token or projectId in Google Cloud credentials. Use /login to re-authenticate."}
	}
	// AuthStorage refreshes before threading credentials here; an expired token
	// is not POSTed, and the next attempt carries a fresh one.
	if credential.hasExpiry && !time.Now().Before(credential.expiresAt) {
		return nil, statusErr{code: http.StatusUnauthorized,
			msg: "OAuth token expired before request — please retry; AuthStorage will refresh on the next attempt."}
	}
	modelID := thinking.ParseSuffix(req.Model).ModelName
	model := antigravityModel(req, modelID)
	if model == nil {
		return nil, statusErr{code: http.StatusBadRequest,
			msg: fmt.Sprintf("antigravity: model %s is not in the account's Antigravity catalog", modelID)}
	}
	original := req.Payload
	if len(opts.OriginalRequest) > 0 {
		original = opts.OriginalRequest
	}
	originalTranslated, translated, err := helps.TranslateRequestPairReturningError(ctx, opts.Headers, e.cfg, opts.SourceFormat,
		sdktranslator.FormatClaude, modelID, original, req.Payload, true, helps.APIKeyModelIsCompat(req))
	if err != nil {
		return nil, err
	}
	if translated, err = helps.ApplyRequestThinking(translated, req, opts, opts.SourceFormat.String(), "claude", e.Identifier()); err != nil {
		return nil, err
	}
	body := gjson.ParseBytes(translated)
	googleModel := antigravityGoogleModel(model)
	npiContext := antigravityContext(body, model)
	antigravityPrepareImages(&npiContext, model)
	simple, err := antigravitywire.MapSimpleOptions(model, antigravitySimpleOptions(body))
	if err != nil {
		return nil, statusErr{code: http.StatusBadRequest, msg: err.Error()}
	}
	state := e.sessionState(opts, auth, translated)
	requestBody := antigravitywire.BuildRequest(&antigravitywire.Input{
		Model:           model,
		ProjectID:       credential.projectID,
		Contents:        googlewire.ConvertMessages(googleModel, npiContext.Messages),
		SystemPrompts:   npiContext.SystemPrompts,
		Tools:           antigravityTools(model, npiContext.Tools),
		ContextMessages: len(npiContext.Messages),
		FirstUserText:   antigravityFirstUserText(body),
		Options:         simple,
		State:           state,
		Sources:         e.sources,
	})
	finalize := helps.NewPayloadFinalizer(e.cfg, e.Identifier(), model.ID, "claude", "", originalTranslated, req, opts)
	prepared := &antigravityPrepared{model: model, credential: credential, body: finalize(requestBody), translated: translated,
		state: state, firstEvent: antigravityFirstEvent(model), modelName: req.Model}
	baseURL := credential.baseURL
	if baseURL == "" {
		baseURL = model.BaseURL
	}
	prepared.endpoints = antigravityEndpoints(baseURL, prepared.state)
	return prepared, nil
}

// antigravityEndpoints is the endpoint list NeoPi's auto mode sends to: the
// preferred host first, then the fallbacks; a configured base URL on another
// host replaces them entirely.
func antigravityEndpoints(baseURL string, state *antigravitywire.SessionState) []string {
	clean := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if clean != "" && clean != antigravitywire.DailyEndpoint && clean != antigravitywire.SandboxEndpoint {
		state.SetLastGood("")
		return []string{clean}
	}
	fallbacks := []string{antigravitywire.DailyEndpoint, antigravitywire.SandboxEndpoint}
	if last := state.LastGood(); last == antigravitywire.DailyEndpoint || last == antigravitywire.SandboxEndpoint {
		ordered := []string{last}
		for _, endpoint := range fallbacks {
			if endpoint != last {
				ordered = append(ordered, endpoint)
			}
		}
		return ordered
	}
	return fallbacks
}

func antigravityFirstEvent(model *registry.NeoPiModel) time.Duration {
	if model.GeminiCompat != nil && model.GeminiCompat.StreamFirstEventTimeoutMs > 0 {
		return time.Duration(model.GeminiCompat.StreamFirstEventTimeoutMs) * time.Millisecond
	}
	return antigravityDefaultFirstEvent
}

// antigravitySimpleOptions is the auth gateway's parse of a Claude body into
// SimpleStreamOptions: sampling fields, tool choice and the thinking budget or
// effort the client asked for.
func antigravitySimpleOptions(body gjson.Result) antigravitywire.SimpleOptions {
	var options antigravitywire.SimpleOptions
	sampling := map[string]**float64{"temperature": &options.Temperature, "top_p": &options.TopP,
		"top_k": &options.TopK, "presence_penalty": &options.PresencePenalty}
	for name, target := range sampling {
		if value := body.Get(name); value.Type == gjson.Number {
			number := value.Float()
			*target = &number
		}
	}
	if maxTokens := body.Get("max_tokens"); maxTokens.Type == gjson.Number && maxTokens.Float() > 0 {
		number := maxTokens.Float()
		options.MaxTokens = &number
	}
	switch choice := body.Get("tool_choice"); choice.Get("type").String() {
	case "auto":
		options.ToolChoice = &antigravitywire.ToolChoice{Mode: "auto"}
	case "any":
		options.ToolChoice = &antigravitywire.ToolChoice{Mode: "any"}
	case "none":
		options.ToolChoice = &antigravitywire.ToolChoice{Mode: "none"}
	case "tool":
		if name := choice.Get("name").String(); name != "" {
			options.ToolChoice = &antigravitywire.ToolChoice{Mode: "any", Allowed: []string{name}}
		}
	}
	budget, budgetSet := 0, false
	switch body.Get("thinking.type").String() {
	case "enabled", "adaptive":
		if value := body.Get("thinking.budget_tokens"); value.Type == gjson.Number {
			budget, budgetSet = int(value.Int()), true
		}
	case "disabled":
		options.DisableReasoning = true
	}
	switch effort := body.Get("output_config.effort").String(); effort {
	case "low", "medium", "high", "xhigh", "max":
		options.Reasoning = effort
	}
	if budgetSet {
		// Mirror resolve_thinking_budget: an explicit budget pins onto the
		// requested effort (High when unspecified) and also sets the effort so
		// providers that gate on `reasoning` surface the budget.
		effort := options.Reasoning
		if effort == "" {
			effort = "high"
		}
		options.ThinkingBudgets = map[string]int{effort: budget}
		options.Reasoning = effort
	}
	return options
}

// antigravityFirstUserText is getFirstUserTextForAntigravitySession.
func antigravityFirstUserText(body gjson.Result) string {
	for _, message := range body.Get("messages").Array() {
		if message.Get("role").String() != "user" {
			continue
		}
		content := message.Get("content")
		if content.Type == gjson.String {
			return content.Str
		}
		for _, block := range content.Array() {
			if block.Get("type").String() == "text" {
				return block.Get("text").String()
			}
		}
		return ""
	}
	return ""
}

// antigravityStateTTL is how long a conversation's envelope identity is kept.
const antigravityStateTTL = 2 * time.Hour

var antigravityStates = struct {
	mu      sync.Mutex
	entries map[string]antigravityStateEntry
}{entries: map[string]antigravityStateEntry{}}

type antigravityStateEntry struct {
	state   *antigravitywire.SessionState
	lastUse time.Time
}

// sessionState returns the provider session state NeoPi threads through its
// stream options for this conversation. Without a session id the request gets
// ephemeral ids, exactly as a direct NeoPi caller does.
func (e *AntigravityExecutor) sessionState(opts cliproxyexecutor.Options, auth *cliproxyauth.Auth, body []byte) *antigravitywire.SessionState {
	session := neoPiSession(opts, body)
	if session == "" {
		return nil
	}
	authID := ""
	if auth != nil {
		authID = auth.ID
	}
	key := authID + "\x00" + session
	now := time.Now()
	antigravityStates.mu.Lock()
	defer antigravityStates.mu.Unlock()
	for stateKey, entry := range antigravityStates.entries {
		if now.Sub(entry.lastUse) > antigravityStateTTL {
			delete(antigravityStates.entries, stateKey)
		}
	}
	entry, ok := antigravityStates.entries[key]
	if !ok {
		entry = antigravityStateEntry{state: &antigravitywire.SessionState{}}
	}
	entry.lastUse = now
	antigravityStates.entries[key] = entry
	return entry.state
}

// Refresh rotates the sign-in's tokens the way NeoPi's refresh grant does and
// records the account's quota report on it.
func (e *AntigravityExecutor) Refresh(ctx context.Context, auth *cliproxyauth.Auth) (*cliproxyauth.Auth, error) {
	if refreshed, handled, err := helps.RefreshAuthViaHome(ctx, e.cfg, auth); handled {
		return refreshed, err
	}
	if auth == nil {
		return auth, nil
	}
	credential := antigravityCredentialOf(auth)
	next := auth
	if credential.refreshToken != "" {
		refreshed, err := antigravityauth.Refresh(ctx, helps.NewProxyAwareHTTPClient(ctx, e.cfg, auth, 0), credential.refreshToken, credential.projectID)
		if err != nil {
			return nil, err
		}
		updated := auth.Clone()
		if updated.Metadata == nil {
			updated.Metadata = map[string]any{}
		}
		updated.Metadata["type"] = "antigravity"
		updated.Metadata["access_token"] = refreshed.Access
		if refreshed.Refresh != "" {
			updated.Metadata["refresh_token"] = refreshed.Refresh
		}
		if refreshed.ProjectID != "" {
			updated.Metadata["project_id"] = refreshed.ProjectID
		}
		if refreshed.Email != "" {
			updated.Metadata["email"] = refreshed.Email
		}
		if refreshed.AccountID != "" {
			updated.Metadata["account_id"] = refreshed.AccountID
		}
		if !refreshed.ExpiresAt.IsZero() {
			updated.Metadata["expired"] = refreshed.ExpiresAt.UTC().Format(time.RFC3339)
		}
		updated.Metadata["timestamp"] = time.Now().UnixMilli()
		updated.LastRefreshedAt = time.Now()
		next = updated
	}
	credential = antigravityCredentialOf(next)
	usage, errUsage := antigravityauth.FetchUsage(ctx, antigravityauth.UsageRequest{
		Client:      helps.NewProxyAwareHTTPClient(ctx, e.cfg, next, 0),
		BaseURL:     credential.baseURL,
		AccessToken: credential.accessToken,
		ProjectID:   credential.projectID,
		Email:       credential.email,
		AccountID:   credential.accountID,
		ExpiresAt:   credential.expiresAt,
	})
	if errUsage != nil || usage == nil {
		return next, nil
	}
	updated := next.Clone()
	antigravityauth.ApplyUsage(updated, usage, time.Now())
	return updated, nil
}

// PrepareRequest injects the account's bearer token into an outgoing request.
func (e *AntigravityExecutor) PrepareRequest(req *http.Request, auth *cliproxyauth.Auth) error {
	if req == nil {
		return nil
	}
	credential := antigravityCredentialOf(auth)
	if credential.accessToken == "" {
		return statusErr{code: http.StatusUnauthorized, msg: "antigravity: account has no access token"}
	}
	req.Header.Set("Authorization", "Bearer "+credential.accessToken)
	return nil
}

// HttpRequest sends an arbitrary request as the Antigravity client does.
func (e *AntigravityExecutor) HttpRequest(ctx context.Context, auth *cliproxyauth.Auth, req *http.Request) (*http.Response, error) {
	if req == nil {
		return nil, fmt.Errorf("antigravity executor: request is nil")
	}
	out := req.Clone(ctx)
	contentType := out.Header.Get("Content-Type")
	for name := range out.Header {
		delete(out.Header, name)
	}
	if contentType != "" {
		out.Header.Set("Content-Type", contentType)
	}
	antigravitywire.SetClientHeaders(out.Header, "")
	if err := e.PrepareRequest(out, auth); err != nil {
		return nil, err
	}
	if auth != nil {
		util.ApplyCustomHeadersFromAttrs(out, auth.Attributes)
	}
	return helps.NewProxyAwareHTTPClient(ctx, e.cfg, auth, 0).Do(out)
}

// CountTokens estimates locally; Cloud Code Assist has no counting RPC.
func (e *AntigravityExecutor) CountTokens(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return e.counter.CountTokens(ctx, auth, req, opts)
}

// antigravityRun carries one client request's execution.
type antigravityRun struct {
	executor *AntigravityExecutor
	auth     *cliproxyauth.Auth
	prepared *antigravityPrepared
	client   *http.Client
	// emit receives one Claude stream event.
	emit func([]byte) error
	// accepted is the attempt whose events were delivered.
	accepted *antigravitywire.Attempt
}

// run sends the request through the endpoint ladder with NeoPi's retry and
// empty-response policy, streaming Claude events to the sink.
func (r *antigravityRun) run(ctx context.Context) error {
	antigravitywire.EnsureVersion(ctx, r.client)
	for index, endpoint := range r.prepared.endpoints {
		lastEndpoint := index == len(r.prepared.endpoints)-1
		outcome, err := r.attemptEndpoint(ctx, endpoint, lastEndpoint)
		committed := outcome != nil && outcome.committed
		if err != nil {
			if !lastEndpoint && !committed && antigravityTransient(statusOf(err), err) {
				continue
			}
			return err
		}
		attempt := outcome.attempt
		switch attempt.StopReason() {
		case antigravitywire.StopReasonError:
			message := attempt.ErrorMessage()
			if message == "" {
				message = "An unknown error occurred"
			}
			return statusErr{code: http.StatusBadGateway, msg: message}
		case antigravitywire.StopReasonStop, antigravitywire.StopReasonLength, antigravitywire.StopReasonToolUse:
		}
		if !attempt.Meaningful() {
			// A thought-only STOP is a complete provider response, not a
			// transiently empty transport.
			if attempt.ThoughtOnly() {
				return statusErr{code: http.StatusBadGateway, msg: "Cloud Code Assist API returned a thought-only response without final output"}
			}
			if !lastEndpoint && !committed {
				continue
			}
			return statusErr{code: http.StatusBadGateway, msg: "Cloud Code Assist API returned an empty response"}
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if !attempt.SawFinish() {
			return statusErr{code: http.StatusBadGateway,
				msg: "Cloud Code Assist stream ended without a finish reason (connection dropped or response truncated)"}
		}
		// Succeeded: the endpoint is committed, and the response id becomes the
		// next request's labels.last_execution_id.
		r.prepared.state.SetLastGood(endpoint)
		r.prepared.state.SetLastExecutionID(attempt.ResponseID())
		r.accepted = attempt
		return nil
	}
	return statusErr{code: http.StatusBadGateway, msg: "Cloud Code Assist API returned an empty response"}
}

// antigravityOutcome is one streamResponse pass.
type antigravityOutcome struct {
	attempt   *antigravitywire.Attempt
	committed bool
}

// attemptEndpoint opens one endpoint and applies the empty-response retries.
func (r *antigravityRun) attemptEndpoint(ctx context.Context, endpoint string, lastEndpoint bool) (*antigravityOutcome, error) {
	url := endpoint + "/v1internal:streamGenerateContent?alt=sse"
	maxAttempts := 1
	if lastEndpoint {
		maxAttempts = antigravityMaxRetries + 1
	}
	var outcome *antigravityOutcome
	for emptyAttempt := 0; emptyAttempt <= antigravitywire.MaxEmptyStreamRetries; emptyAttempt++ {
		if emptyAttempt > 0 {
			if errWait := neopiretry.Sleep(ctx, antigravitywire.EmptyStreamBaseDelay<<(emptyAttempt-1)); errWait != nil {
				return outcome, ctx.Err()
			}
		}
		response, err := r.open(ctx, url, maxAttempts, emptyAttempt > 0)
		if err != nil {
			return outcome, err
		}
		streamed, errStream := r.streamResponse(ctx, response)
		outcome = streamed
		if errStream != nil {
			return outcome, errStream
		}
		attempt := streamed.attempt
		if attempt.StopReason() != antigravitywire.StopReasonStop || attempt.Meaningful() || attempt.ThoughtOnly() {
			return outcome, nil
		}
	}
	return outcome, nil
}

// open sends the request, honouring NeoPi's bounded retries and the
// pre-response first-event watchdog. The watchdog only guards the wait for
// response headers: it is disarmed the moment they arrive, so an actively
// streaming body is never aborted by it.
func (r *antigravityRun) open(ctx context.Context, url string, maxAttempts int, retry bool) (*http.Response, error) {
	if retry {
		response, err := r.send(ctx, ctx, url)
		if err != nil {
			return nil, err
		}
		return r.checkStatus(ctx, response)
	}
	watchdog, cancel := context.WithCancel(ctx)
	var timedOut atomic.Bool
	timer := time.AfterFunc(r.prepared.firstEvent, func() { timedOut.Store(true); cancel() })
	response, err := neopiretry.Do(watchdog, func(int) (*http.Response, error) { return r.send(ctx, watchdog, url) },
		neopiretry.Options{MaxAttempts: maxAttempts, MaxDelay: antigravityRateLimitBudget,
			DefaultDelay: func(attempt int) time.Duration { return antigravityBaseDelay << attempt }})
	if !timer.Stop() {
		timedOut.Store(true)
	}
	if err != nil {
		cancel()
		if timedOut.Load() && ctx.Err() == nil {
			return nil, statusErr{code: http.StatusGatewayTimeout,
				msg: "Cloud Code Assist stream timed out while waiting for the first event"}
		}
		return nil, err
	}
	// The request context inherits caller cancellation. Closing the body also
	// releases the child context.
	response.Body = &antigravityBody{ReadCloser: response.Body, cancel: cancel}
	return r.checkStatus(ctx, response)
}

// antigravityBody releases the watchdog context when the body closes.
type antigravityBody struct {
	io.ReadCloser
	cancel context.CancelFunc
}

func (b *antigravityBody) Close() error {
	err := b.ReadCloser.Close()
	b.cancel()
	return err
}

// checkStatus turns a non-2xx response into NeoPi's API error.
func (r *antigravityRun) checkStatus(ctx context.Context, response *http.Response) (*http.Response, error) {
	if response.StatusCode >= 200 && response.StatusCode < 300 {
		return response, nil
	}
	decoded, err := util.DecodeResponseBody(response.Body, response.Header.Get("Content-Encoding"))
	var payload []byte
	if err == nil {
		payload, _ = io.ReadAll(decoded)
		_ = decoded.Close()
	}
	_ = response.Body.Close()
	helps.AppendAPIResponseChunk(ctx, r.executor.cfg, payload)
	return nil, antigravityStatusError(response.StatusCode, payload, r.prepared.credential.email)
}

// send posts the shaped envelope with the Antigravity client identity.
func (r *antigravityRun) send(ctx, requestCtx context.Context, url string) (*http.Response, error) {
	request, err := http.NewRequestWithContext(requestCtx, http.MethodPost, url, bytes.NewReader(r.prepared.body))
	if err != nil {
		return nil, err
	}
	request.Header = r.headers()
	info := helps.UpstreamRequestLog{URL: request.URL.String(), Method: request.Method, Headers: request.Header.Clone(),
		Body: r.prepared.body, Provider: r.executor.Identifier()}
	if r.auth != nil {
		info.AuthID, info.AuthLabel = r.auth.ID, r.auth.Label
		info.AuthType, info.AuthValue = r.auth.AccountInfo()
	}
	helps.RecordAPIRequest(ctx, r.executor.cfg, info)
	response, err := r.client.Do(request)
	if err != nil {
		helps.RecordAPIResponseError(ctx, r.executor.cfg, err)
		return nil, err
	}
	helps.RecordAPIResponseMetadata(ctx, r.executor.cfg, response.StatusCode, response.Header)
	return response, nil
}

// headers is the Cloud Code Assist request header set NeoPi sends: bearer
// token, JSON, the Antigravity client identity, and the Claude thinking beta
// on the Claude models that scope it.
func (r *antigravityRun) headers() http.Header {
	headers := make(http.Header)
	antigravitywire.SetClientHeaders(headers, "")
	headers.Set("Authorization", "Bearer "+r.prepared.credential.accessToken)
	headers.Set("Content-Type", "application/json")
	model := r.prepared.model
	if compat := model.GeminiCompat; compat != nil && compat.ClaudeThinkingBetaHeader && compat.IdentityClass == "anthropic" && model.Reasoning {
		headers.Set("anthropic-beta", antigravityClaudeThinkingBeta)
	}
	if r.auth != nil {
		util.ApplyCustomHeadersFromAttrs(&http.Request{Header: headers}, r.auth.Attributes)
	}
	return headers
}

// streamResponse reads one response and streams its events.
func (r *antigravityRun) streamResponse(ctx context.Context, response *http.Response) (*antigravityOutcome, error) {
	defer func() { _ = response.Body.Close() }()
	var firstTimedOut atomic.Bool
	timer := time.AfterFunc(r.prepared.firstEvent, func() {
		firstTimedOut.Store(true)
		_ = response.Body.Close()
	})
	defer timer.Stop()
	decoded, err := util.DecodeResponseBody(response.Body, response.Header.Get("Content-Encoding"))
	if err != nil {
		if firstTimedOut.Load() && ctx.Err() == nil {
			return nil, statusErr{code: http.StatusGatewayTimeout, msg: "Cloud Code Assist stream timed out while waiting for the first event"}
		}
		return nil, err
	}
	defer func() { _ = decoded.Close() }()
	attempt := antigravitywire.NewAttempt(r.prepared.modelName, antigravityToolNames(r.prepared.translated),
		r.prepared.model.GeminiCompat != nil && r.prepared.model.GeminiCompat.FlashStreamLeakWorkaround, nil)
	attempt.SetHeldLimit(antigravityHeldTextLimit)
	outcome := &antigravityOutcome{attempt: attempt}
	reader := antigravitywire.NewSSEReader(decoded)
	first := true
	for {
		event, errRead := reader.Next()
		if first {
			first = false
			timer.Stop()
			if firstTimedOut.Load() && ctx.Err() == nil {
				return outcome, statusErr{code: http.StatusGatewayTimeout, msg: "Cloud Code Assist stream timed out while waiting for the first event"}
			}
		}
		if errors.Is(errRead, io.EOF) {
			break
		}
		if errRead != nil {
			return outcome, errRead
		}
		if errFeed := attempt.Feed(event); errFeed != nil {
			return outcome, antigravityInBandError(errFeed)
		}
		if !outcome.committed && attempt.CommitReady() {
			if err := r.commit(outcome); err != nil {
				return outcome, err
			}
		}
		if outcome.committed {
			if err := r.flushAttempt(attempt); err != nil {
				return outcome, err
			}
		}
	}
	attempt.Finish()
	if !outcome.committed && attempt.CommitReady() {
		if err := r.commit(outcome); err != nil {
			return outcome, err
		}
	}
	if outcome.committed {
		if err := r.flushAttempt(attempt); err != nil {
			return outcome, err
		}
	}
	return outcome, nil
}

// commit releases the held events once the attempt cannot be retried.
func (r *antigravityRun) commit(outcome *antigravityOutcome) error {
	if outcome.committed {
		return nil
	}
	outcome.committed = true
	if err := r.emit([]byte(antigravitywire.MessageStart(antigravityMessageID(outcome.attempt), r.prepared.modelName))); err != nil {
		return err
	}
	return r.flushAttempt(outcome.attempt)
}

func (r *antigravityRun) flushAttempt(attempt *antigravitywire.Attempt) error {
	for _, event := range attempt.Take() {
		if err := r.emit(event); err != nil {
			return err
		}
	}
	return nil
}

func antigravityMessageID(attempt *antigravitywire.Attempt) string {
	if id := attempt.ResponseID(); id != "" {
		return id
	}
	return "msg_antigravity"
}

// finishEvents are the closing events of the accepted attempt.
func (r *antigravityRun) finishEvents() []string {
	if r.accepted == nil {
		return nil
	}
	return r.accepted.MessageEnd()
}

func antigravityToolNames(body []byte) []string {
	var names []string
	for _, tool := range gjson.GetBytes(body, "tools").Array() {
		if name := tool.Get("name").String(); name != "" {
			names = append(names, name)
		}
	}
	return names
}

// antigravityStatusError is NeoPi's `Cloud Code Assist API error (<status>):
// <body>`, with Google's validation message when the body asks for one.
func antigravityStatusError(status int, payload []byte, email string) error {
	text := string(payload)
	if url := extractGoogleValidationURL(text); url != "" {
		account := ""
		if email != "" {
			account = " for " + email
		}
		text = "Account verification required" + account + ". Visit " + url + " to continue, then retry your request."
	}
	err := statusErr{code: status, msg: fmt.Sprintf("Cloud Code Assist API error (%d): %s", status, text)}
	if status == http.StatusTooManyRequests {
		err.credentialScoped = true
		delay := 30 * time.Second
		if hint, ok := neopiretry.ExtractRetryHint(nil, text, time.Now()); ok && hint > 0 {
			delay = time.Duration(hint * float64(time.Millisecond))
		}
		err.retryAfter = &delay
	}
	return err
}

// antigravityInBandError maps an in-band stream error onto the status the
// proxy's scheduler reads.
func antigravityInBandError(err error) error {
	var streamErr *antigravitywire.StreamError
	if !errors.As(err, &streamErr) {
		return err
	}
	status := streamErr.Status
	if status == 0 {
		status = http.StatusBadGateway
	}
	return statusErr{code: status, msg: streamErr.Message}
}

// antigravityTransient reports whether a failed attempt may be replayed on the
// next endpoint: NeoPi retries transient statuses, and any failure that is not
// the provider's own output verdict.
func antigravityTransient(status int, err error) bool {
	if status > 0 {
		return neopiretry.IsRetryableStatus(status)
	}
	var streamErr *antigravitywire.StreamError
	if errors.As(err, &streamErr) && streamErr.Kind == "output" {
		return false
	}
	return true
}

func statusOf(err error) int {
	var se statusErr
	if errors.As(err, &se) {
		return se.code
	}
	return 0
}

// extractGoogleValidationURL is extractGoogleValidationUrl.
func extractGoogleValidationURL(body string) string {
	if !strings.Contains(body, "VALIDATION_REQUIRED") {
		return ""
	}
	start := strings.Index(body, "{")
	if start == -1 || !gjson.Valid(body[start:]) {
		return ""
	}
	for _, detail := range gjson.Parse(body[start:]).Get("error.details").Array() {
		if detail.Get("reason").String() == "VALIDATION_REQUIRED" {
			if url := detail.Get("metadata.validation_url"); url.Type == gjson.String {
				return url.Str
			}
		}
	}
	return ""
}

// antigravityAggregate turns the streamed Claude events into one message.
func antigravityAggregate(events [][]byte) ([]byte, error) {
	var transcript bytes.Buffer
	for _, event := range events {
		transcript.Write(neoPiFrame(event))
	}
	return neoPiAggregateMessage(transcript.Bytes())
}

// Execute answers one request without streaming.
func (e *AntigravityExecutor) Execute(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (result cliproxyexecutor.Response, err error) {
	if isAntigravityImageRequest(opts) {
		return e.executeImage(ctx, auth, req, opts)
	}
	prepared, err := e.prepare(ctx, auth, req, opts)
	if err != nil {
		return result, err
	}
	reporter := helps.NewExecutorUsageReporter(ctx, e, prepared.model.ID, auth)
	defer reporter.TrackFailure(ctx, &err)
	var events [][]byte
	var usage helps.StreamUsageBuffer
	run := &antigravityRun{executor: e, auth: auth, prepared: prepared,
		client: reporter.TrackHTTPClient(helps.NewProxyAwareHTTPClient(ctx, e.cfg, auth, 0)),
		emit: func(event []byte) error {
			usage.ObserveClaudeStream(append([]byte("data: "), event...))
			events = append(events, bytes.Clone(event))
			return nil
		}}
	if err = run.run(ctx); err != nil {
		usage.PublishFailure(ctx, reporter, err)
		return result, err
	}
	for _, event := range run.finishEvents() {
		data := []byte(event)
		usage.ObserveClaudeStream(append([]byte("data: "), data...))
		events = append(events, data)
	}
	data, errAggregate := antigravityAggregate(events)
	if errAggregate != nil {
		return result, errAggregate
	}
	if format := cliproxyexecutor.ResponseFormatOrSource(opts); format != sdktranslator.FormatClaude {
		var state any
		var transcript bytes.Buffer
		for _, event := range events {
			transcript.Write(neoPiFrame(event))
		}
		data = sdktranslator.TranslateNonStream(ctx, sdktranslator.FormatClaude, format, req.Model,
			helps.ApplyPatchOriginalRequest(req, opts), prepared.translated, transcript.Bytes(), &state)
		if helps.ApplyPatchTranslationError(state) != nil {
			return result, statusErr{code: http.StatusBadGateway, msg: helps.ApplyPatchUpstreamErrorMessage}
		}
		if len(data) == 0 {
			return result, errors.New("antigravity: response translation failed")
		}
	}
	reporter.SetUpstreamModel(prepared.model.ID)
	usage.Publish(ctx, reporter)
	return cliproxyexecutor.Response{Payload: data, Headers: http.Header{}}, nil
}

// ExecuteStream streams one request as Anthropic Messages events.
func (e *AntigravityExecutor) ExecuteStream(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (_ *cliproxyexecutor.StreamResult, err error) {
	if isAntigravityImageRequest(opts) {
		return e.executeImageStream(ctx, auth, req, opts)
	}
	prepared, err := e.prepare(ctx, auth, req, opts)
	if err != nil {
		return nil, err
	}
	reporter := helps.NewExecutorUsageReporter(ctx, e, prepared.model.ID, auth)
	defer reporter.TrackFailure(ctx, &err)
	out := make(chan cliproxyexecutor.StreamChunk, 1)
	go func() {
		defer close(out)
		var usage helps.StreamUsageBuffer
		var state any
		format := cliproxyexecutor.ResponseFormatOrSource(opts)
		helps.InitializeApplyPatchStream(ctx, sdktranslator.FormatClaude, format, req.Model, helps.ApplyPatchOriginalRequest(req, opts), prepared.translated, &state)
		run := &antigravityRun{executor: e, auth: auth, prepared: prepared,
			client: reporter.TrackHTTPClient(helps.NewProxyAwareHTTPClient(ctx, e.cfg, auth, 0)),
			emit: func(event []byte) error {
				line := append([]byte("data: "), event...)
				usage.ObserveClaudeStream(line)
				chunks := [][]byte{neoPiFrame(event)}
				if format != sdktranslator.FormatClaude {
					chunks = sdktranslator.TranslateStream(ctx, sdktranslator.FormatClaude, format, req.Model,
						helps.ApplyPatchOriginalRequest(req, opts), prepared.translated, line, &state)
				}
				for _, chunk := range chunks {
					select {
					case out <- cliproxyexecutor.StreamChunk{Payload: bytes.Clone(chunk)}:
					case <-ctx.Done():
						return ctx.Err()
					}
				}
				if helps.ApplyPatchTranslationError(state) != nil {
					return statusErr{code: http.StatusBadGateway, msg: helps.ApplyPatchUpstreamErrorMessage}
				}
				return nil
			}}
		runErr := run.run(ctx)
		if runErr == nil {
			for _, event := range run.finishEvents() {
				if emitErr := run.emit([]byte(event)); emitErr != nil {
					runErr = emitErr
					break
				}
			}
		}
		for _, chunk := range helps.FinalizeApplyPatchStream(state) {
			select {
			case out <- cliproxyexecutor.StreamChunk{Payload: chunk}:
			case <-ctx.Done():
				return
			}
		}
		if runErr != nil {
			usage.PublishFailure(ctx, reporter, runErr)
			select {
			case out <- cliproxyexecutor.StreamChunk{Err: runErr}:
			case <-ctx.Done():
			}
			return
		}
		reporter.SetUpstreamModel(prepared.model.ID)
		usage.Publish(ctx, reporter)
	}()
	return &cliproxyexecutor.StreamResult{Headers: http.Header{}, Chunks: out}, nil
}

// antigravityProxyURL resolves the proxy a Cloud Code Assist request uses:
// the per-request proxy, else the credential's, else the global one.
func antigravityProxyURL(ctx context.Context, cfg *config.Config, auth *cliproxyauth.Auth) string {
	if proxyURL := cliproxyexecutor.RequestProxyURL(ctx); proxyURL != "" {
		return proxyURL
	}
	if auth != nil {
		if proxyURL := strings.TrimSpace(auth.ProxyURL); proxyURL != "" {
			return proxyURL
		}
	}
	if cfg != nil {
		return strings.TrimSpace(cfg.ProxyURL)
	}
	return ""
}

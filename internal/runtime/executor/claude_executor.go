package executor

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
	"math"
	"math/rand/v2"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/auth/anthropicnpi"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/thinking"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
	"golang.org/x/sync/singleflight"
)

// ClaudeExecutor follows NeoPi's Anthropic request and stream implementation.
type ClaudeExecutor struct {
	cfg      *config.Config
	refresh  *singleflight.Group
	provider string
	// thirdParty describes a provider NeoPi serves over Anthropic Messages
	// (Z.ai, Kimi Code); nil for Claude itself.
	thirdParty *neoPiThirdPartyProvider
	wait       func(context.Context, time.Duration) error
	now        func() time.Time
	random     func() float64
}

// neoPiThirdPartyProvider is what prepareThirdParty needs from a provider:
// the NeoPi record of the requested model, the bearer the account
// authenticates with, and any identity headers NeoPi adds for it.
type neoPiThirdPartyProvider struct {
	catalog func(req cliproxyexecutor.Request, model string) *registry.NeoPiModel
	bearer  func(auth *cliproxyauth.Auth) string
	headers func() http.Header
	// apiKeyHeader authenticates with X-Api-Key instead of Authorization:
	// OpenCode's Anthropic-compatible endpoint rejects bearer-only requests
	// (401 Missing API key).
	apiKeyHeader bool
	// sessionHeader carries the conversation id next to the endpoint's own
	// protocol header (OpenCode's x-opencode-session).
	sessionHeader string
}

func NewClaudeExecutor(cfg *config.Config) *ClaudeExecutor {
	return &ClaudeExecutor{cfg: cfg, refresh: &singleflight.Group{}, wait: neoPiWait, now: time.Now, random: rand.Float64}
}

func (e *ClaudeExecutor) Identifier() string {
	if e.provider != "" {
		return e.provider
	}
	return anthropicnpi.Provider
}

func neoPiWait(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func neoPiCredential(auth *cliproxyauth.Auth) (*anthropicnpi.Credential, error) {
	return anthropicnpi.CredentialFromAuth(auth)
}

func neoPiBaseURL(auth *cliproxyauth.Auth) string {
	if auth != nil {
		if value := strings.TrimSpace(auth.Attributes["base_url"]); value != "" {
			return strings.TrimSuffix(strings.TrimRight(value, "/"), "/v1")
		}
		if value, ok := auth.Metadata["base_url"].(string); ok && value != "" {
			return strings.TrimSuffix(strings.TrimRight(strings.TrimSpace(value), "/"), "/v1")
		}
	}
	return "https://api.anthropic.com"
}

func neoPiSession(opts cliproxyexecutor.Options, body []byte) string {
	if value := opts.Headers.Get("X-Claude-Code-Session-Id"); value != "" {
		return value
	}
	for _, key := range []string{cliproxyexecutor.ExecutionSessionMetadataKey, cliproxyexecutor.CanonicalSessionIDMetadataKey, cliproxyexecutor.DerivedSessionIDMetadataKey} {
		if value, ok := opts.Metadata[key].(string); ok && value != "" {
			return value
		}
	}
	userID := gjson.GetBytes(body, "metadata.user_id").String()
	if gjson.Valid(userID) {
		if session := gjson.Get(userID, "session_id"); session.Type == gjson.String && session.String() != "" {
			return session.String()
		}
	}
	if session := neoPiLegacySession(userID); session != "" {
		return session
	}
	return ""
}

type neoPiPrepared struct {
	auth       *cliproxyauth.Auth
	body       []byte
	translated []byte
	headers    http.Header
	url        string
	model      string
	identity   neoPiWireIdentity
	base       string
	oauth      bool
	finalize   helps.PayloadFinalizer
}

func (e *ClaudeExecutor) prepare(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options, countOnly ...bool) (*neoPiPrepared, error) {
	if e.thirdParty != nil {
		return e.prepareThirdParty(ctx, auth, req, opts)
	}
	credential, err := neoPiCredential(auth)
	if err != nil {
		return nil, err
	}
	oauth := credential.APIKey == ""
	if oauth {
		authDir := ""
		if e.cfg != nil {
			authDir = e.cfg.AuthDir
		}
		if err = anthropicnpi.EnsureInstallID(authDir, credential); err != nil {
			return nil, err
		}
	}
	model := thinking.ParseSuffix(req.Model).ModelName
	model = strings.TrimPrefix(model, anthropicnpi.Provider+"/")
	original := req.Payload
	if len(opts.OriginalRequest) > 0 {
		original = opts.OriginalRequest
	}
	originalTranslated, translated, err := helps.TranslateRequestPairReturningError(ctx, opts.Headers, e.cfg, opts.SourceFormat, sdktranslator.FromString("claude"), model, original, req.Payload, true, helps.APIKeyModelIsCompat(req))
	if err != nil {
		return nil, err
	}
	translated, err = helps.ApplyRequestThinking(translated, req, opts, opts.SourceFormat.String(), "claude", e.Identifier())
	if err != nil {
		return nil, err
	}
	identity := neoPiWireIdentity{InstallID: credential.InstallID, AccountID: credential.AccountUUID, SessionID: neoPiSession(opts, translated), APIKey: !oauth}
	if oauth && identity.SessionID == "" {
		identity.SessionID = uuid.NewString()
		identity.OmitSessionHeader = true
	}
	for _, name := range []string{"account_uuid", "accountId", "account_id"} {
		if value := gjson.GetBytes(translated, "metadata."+name); value.Type == gjson.String && value.String() != "" {
			identity.AccountID = value.String()
			break
		}
	}
	base := neoPiBaseURL(auth)
	body, headers, err := projectNeoPiRequest(translated, model, base, anthropicnpi.WireVersion(), identity)
	if err != nil {
		return nil, err
	}
	finalize := helps.NewPayloadFinalizer(e.cfg, e.Identifier(), model, "claude", "", originalTranslated, req, opts)
	if len(countOnly) > 0 && countOnly[0] {
		for _, name := range []string{"max_tokens", "stream", "metadata", "output_config", "speed", "service_tier", "fallbacks", "fallback_credit_token"} {
			body = neoPiDelete(body, name)
		}
	}
	body = finalize(body)
	if oauth {
		body = neoPiSignBody(body)
	}
	neoPiApplyHeaders(headers, auth, opts.Headers, credential, base, ctx)
	path := "/v1/messages"
	if oauth {
		path += "?beta=true"
	}
	return &neoPiPrepared{auth: auth, body: body, translated: translated, headers: headers, url: base + path, model: model, identity: identity, base: base, oauth: oauth, finalize: finalize}, nil
}

func (e *ClaudeExecutor) HttpRequest(ctx context.Context, auth *cliproxyauth.Auth, request *http.Request) (*http.Response, error) {
	credential, err := neoPiCredential(auth)
	if err != nil {
		return nil, err
	}
	request = request.Clone(ctx)
	neoPiApplyHeaders(request.Header, auth, nil, credential, neoPiBaseURL(auth), ctx)
	return helps.NewProxyAwareHTTPClient(ctx, e.cfg, auth, 0).Do(request)
}

func (e *ClaudeExecutor) Refresh(ctx context.Context, auth *cliproxyauth.Auth) (*cliproxyauth.Auth, error) {
	if cliproxyauth.IsRefreshLocked(auth) {
		return nil, cliproxyauth.ErrRefreshLocked
	}
	credential, err := neoPiCredential(auth)
	if err != nil {
		return nil, err
	}
	if credential.APIKey != "" {
		return auth, nil
	}
	// The conductor normally serializes refreshes. This also protects direct
	// executor callers; the key never appears in logs or persisted metadata.
	key := auth.ID + "\x00" + credential.RefreshToken
	value, err, _ := e.refresh.Do(key, func() (any, error) {
		updated, refreshErr := anthropicnpi.NewClient(e.cfg, auth.ProxyURL).Refresh(ctx, credential)
		if refreshErr != nil {
			return nil, refreshErr
		}
		next := auth.Clone()
		fields, metadataErr := anthropicnpi.CredentialMetadata(updated)
		if metadataErr != nil {
			return nil, metadataErr
		}
		next.Metadata = fields
		next.Provider = anthropicnpi.Provider
		next.LastRefreshedAt = time.Now()
		return next, nil
	})
	if err != nil {
		return nil, err
	}
	return value.(*cliproxyauth.Auth).Clone(), nil
}

type neoPiUpstreamError struct {
	code    int
	body    string
	headers http.Header
}

func (e *neoPiUpstreamError) Error() string                { return e.body }
func (e *neoPiUpstreamError) StatusCode() int              { return e.code }
func (e *neoPiUpstreamError) ResponseHeaders() http.Header { return e.headers.Clone() }
func (e *neoPiUpstreamError) RetryAfter() *time.Duration {
	if delay, ok := neoPiRetryAfter(e.headers, time.Now()); ok {
		return &delay
	}
	return nil
}

// The public stream provider disables the client's retry loop. Its shared
// Retry-After helper takes the maximum of all hints, rounds up milliseconds,
// and accepts both relative and epoch reset headers.
func neoPiRetryAfter(headers http.Header, now time.Time) (time.Duration, bool) {
	var delayMs float64
	add := func(value float64) {
		if !math.IsNaN(value) && !math.IsInf(value, 0) && value > 0 {
			delayMs = math.Max(delayMs, math.Ceil(value))
		}
	}
	number := func(name string) (float64, bool) {
		value, err := strconv.ParseFloat(strings.TrimSpace(headers.Get(name)), 64)
		return value, err == nil && !math.IsNaN(value) && !math.IsInf(value, 0)
	}
	if value, ok := number("retry-after-ms"); ok {
		add(value)
	}
	if value, ok := number("retry-after"); ok {
		add(value * 1000)
	} else if date, err := http.ParseTime(headers.Get("retry-after")); err == nil {
		add(float64(date.UnixMilli() - now.UnixMilli()))
	}
	for _, name := range []string{"x-ratelimit-reset-ms", "x-ratelimit-reset"} {
		value, ok := number(name)
		if !ok || value <= 0 {
			continue
		}
		switch {
		case value > 1e12:
			add(value - float64(now.UnixMilli()))
		case value > 1e9:
			add(value*1000 - float64(now.UnixMilli()))
		case name == "x-ratelimit-reset":
			add(value * 1000)
		default:
			add(value)
		}
	}
	if delayMs == 0 {
		return 0, false
	}
	if delayMs >= float64(math.MaxInt64)/float64(time.Millisecond) {
		return time.Duration(math.MaxInt64), true
	}
	return time.Duration(delayMs) * time.Millisecond, true
}

var neoPiAccountLimitPattern = regexp.MustCompile(`(?i)\baccount(?:'s)?\b[^\n]{0,80}\brate.?limit\b|\brate.?limit\b[^\n]{0,80}\baccount\b|spend(?:ing)?[\s_-]?(?:limit|cap)\b`)

func neoPiRetryable(err error) bool {
	var upstream *neoPiUpstreamError
	if !errors.As(err, &upstream) {
		return !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded)
	}
	if neoPiAccountLimitPattern.MatchString(upstream.body) {
		return false
	}
	message := strings.ToLower(upstream.body)
	for _, limit := range []string{"usage_limit_reached", "insufficient_quota", "weekly usage limit", "usage credits are required", "you have reached your", "credit balance is too low"} {
		if strings.Contains(message, limit) {
			return false
		}
	}
	return upstream.code == 408 || upstream.code == 429 || upstream.code >= 500
}

func (e *ClaudeExecutor) retry(ctx context.Context, err error, attempt int) error {
	// streamAnthropic passes maxRetries:0 to its client and owns ten retries.
	// Consequently 409 and x-should-retry do not use the client's dormant policy.
	if attempt >= 10 || ctx.Err() != nil || !neoPiRetryable(err) {
		return err
	}
	delay := time.Duration(math.Min(.5*math.Pow(2, float64(attempt)), 8) * (1 - e.random()*.25) * float64(time.Second))
	var upstream *neoPiUpstreamError
	if errors.As(err, &upstream) {
		if hint, ok := neoPiRetryAfter(upstream.headers, e.now()); ok {
			if hint > time.Minute {
				return err
			}
			delay = max(delay, hint)
		}
	}
	return e.wait(ctx, delay)
}

// neoPiStream buffers pre-output frames for the separate empty-completion
// policy. Provider-error replay stops at a content-block start, even before
// its first delta, exactly as NeoPi's inner stream loop does.
type neoPiStream struct {
	response *http.Response
	body     io.ReadCloser
	scanner  *bufio.Scanner
	pending  [][]byte
	started  bool
	stopped  bool
}

func neoPiSSEData(line []byte) []byte {
	if !bytes.HasPrefix(line, []byte("data:")) {
		return nil
	}
	return bytes.TrimSpace(line[5:])
}

func neoPiMeaningful(data []byte) bool {
	root := gjson.ParseBytes(data)
	if root.Get("type").String() != "content_block_delta" {
		return false
	}
	for _, name := range []string{"text", "thinking", "partial_json"} {
		if root.Get("delta."+name).String() != "" {
			return true
		}
	}
	return false
}

func neoPiSSEError(data []byte) error {
	if gjson.GetBytes(data, "type").String() != "error" {
		return nil
	}
	kind := gjson.GetBytes(data, "error.type").String()
	status := http.StatusBadRequest
	switch kind {
	case "overloaded_error":
		status = 529
	case "rate_limit_error":
		status = 429
	case "api_error", "server_error":
		status = 500
	}
	// NeoPi's SSE ProviderResponseError does not carry HTTP response headers.
	return &neoPiUpstreamError{code: status, body: string(data)}
}
func (e *ClaudeExecutor) recordRequest(ctx context.Context, prepared *neoPiPrepared, request *http.Request) {
	info := helps.UpstreamRequestLog{
		URL: request.URL.String(), Method: request.Method, Headers: request.Header,
		Body: prepared.body, Provider: e.Identifier(),
	}
	if prepared.auth != nil {
		info.AuthID, info.AuthLabel = prepared.auth.ID, prepared.auth.Label
		info.AuthType, info.AuthValue = prepared.auth.AccountInfo()
	}
	helps.RecordAPIRequest(ctx, e.cfg, info)
}

func (e *ClaudeExecutor) open(ctx context.Context, client *http.Client, prepared *neoPiPrepared) (*neoPiStream, error) {
	emptyRetries := 0
	for attempt := 0; ; attempt++ {
		requestCtx, cancel := context.WithCancel(ctx)
		request, err := http.NewRequestWithContext(requestCtx, http.MethodPost, prepared.url, bytes.NewReader(prepared.body))
		if err != nil {
			cancel()
			return nil, err
		}
		request.Header = prepared.headers.Clone()
		e.recordRequest(ctx, prepared, request)
		response, err := client.Do(request)
		if err != nil {
			helps.RecordAPIResponseError(ctx, e.cfg, err)
			cancel()
			if retryErr := e.retry(ctx, err, attempt); retryErr != nil {
				return nil, retryErr
			}
			continue
		}
		helps.RecordAPIResponseMetadata(ctx, e.cfg, response.StatusCode, response.Header)
		decoded, err := util.DecodeResponseBody(response.Body, response.Header.Get("Content-Encoding"))
		if err != nil {
			response.Body.Close()
			cancel()
			return nil, err
		}
		body := &neoPiCancelBody{ReadCloser: decoded, cancel: cancel}
		if response.StatusCode < 200 || response.StatusCode >= 300 {
			data, readErr := io.ReadAll(body)
			helps.AppendAPIResponseChunk(ctx, e.cfg, data)
			body.Close()
			if readErr != nil {
				err = readErr
			} else {
				err = &neoPiUpstreamError{code: response.StatusCode, body: string(data), headers: response.Header}
			}
			if prepared.oauth && anthropicnpi.AdoptRequiredVersion(err.Error()) {
				projected, headers, projectErr := projectNeoPiRequest(prepared.translated, prepared.model, prepared.base, anthropicnpi.WireVersion(), prepared.identity)
				if projectErr != nil {
					return nil, projectErr
				}
				projected = neoPiSignBody(prepared.finalize(projected))
				for _, name := range []string{"Authorization", "X-Api-Key", "User-Agent"} {
					if value := prepared.headers.Get(name); value != "" {
						headers.Set(name, value)
					}
				}
				if slow := prepared.headers.Get("anthropic-usage-limit"); slow != "" {
					headers.Set("anthropic-usage-limit", slow)
				}
				prepared.body, prepared.headers = projected, headers
				attempt--
				continue
			}
			if retryErr := e.retry(ctx, err, attempt); retryErr != nil {
				return nil, retryErr
			}
			continue
		}
		scanner := bufio.NewScanner(body)
		scanner.Buffer(make([]byte, 64<<10), 32<<20)
		stream := &neoPiStream{response: response, body: body, scanner: scanner}
		visibleTool := false
		replayUnsafe := false
		outputTokens := int64(0)
		stopReason := ""
		for scanner.Scan() {
			helps.AppendAPIResponseChunk(ctx, e.cfg, scanner.Bytes())
			data := neoPiSSEData(scanner.Bytes())
			if len(data) == 0 {
				continue
			}
			if !gjson.ValidBytes(data) {
				// NeoPi reports and skips malformed frames; an incomplete envelope
				// is classified after the stream ends rather than replayed here.
				continue
			}
			if eventErr := neoPiSSEError(data); eventErr != nil {
				err = eventErr
				break
			}
			kind := gjson.GetBytes(data, "type").String()
			if kind == "message_start" {
				stream.started = true
				outputTokens = gjson.GetBytes(data, "message.usage.output_tokens").Int()
			} else if !stream.started && kind != "ping" {
				err = errors.New("claude: content before message_start")
				break
			}
			if kind == "content_block_start" {
				blockKind := gjson.GetBytes(data, "content_block.type").String()
				replayUnsafe = replayUnsafe || blockKind != ""
				visibleTool = visibleTool || blockKind == "tool_use"
			}
			if kind == "message_delta" {
				if value := gjson.GetBytes(data, "delta.stop_reason").String(); value != "" {
					stopReason = value
				}
				if value := gjson.GetBytes(data, "usage.output_tokens"); value.Exists() {
					outputTokens = value.Int()
				}
			}
			stream.pending = append(stream.pending, bytes.Clone(data))
			if neoPiMeaningful(data) {
				if !stream.started {
					err = errors.New("claude: content before message_start")
					break
				}
				return stream, nil
			}
			if kind == "message_stop" {
				stream.stopped = true
				if !stream.started {
					err = errors.New("claude: message_stop without message_start")
					break
				}
				if visibleTool || outputTokens > 1 || (stopReason != "" && stopReason != "end_turn" && stopReason != "stop_sequence" && stopReason != "pause_turn" && stopReason != "compaction") || emptyRetries >= 2 {
					return stream, nil
				}
				break
			}
		}
		body.Close()
		if err == nil {
			err = scanner.Err()
		}
		if err == nil && stream.stopped {
			if waitErr := e.wait(ctx, 500*time.Millisecond*time.Duration(1<<emptyRetries)); waitErr != nil {
				return nil, waitErr
			}
			emptyRetries++
			attempt = -1
			continue
		}
		if err == nil {
			err = errors.New("claude: stream ended before message_stop")
		}
		if replayUnsafe {
			return nil, err
		}
		if retryErr := e.retry(ctx, err, attempt); retryErr != nil {
			return nil, retryErr
		}
	}
}

type neoPiCancelBody struct {
	io.ReadCloser
	cancel context.CancelFunc
}

func (b *neoPiCancelBody) Close() error { b.cancel(); return b.ReadCloser.Close() }

func (s *neoPiStream) consume(ctx context.Context, cfg *config.Config, emit func([]byte) error) error {
	defer s.body.Close()
	for _, data := range s.pending {
		if err := emit(data); err != nil {
			return err
		}
	}
	if s.stopped {
		return nil
	}
	for s.scanner.Scan() {
		helps.AppendAPIResponseChunk(ctx, cfg, s.scanner.Bytes())
		data := neoPiSSEData(s.scanner.Bytes())
		if len(data) == 0 {
			continue
		}
		if !gjson.ValidBytes(data) {
			continue
		}
		if err := neoPiSSEError(data); err != nil {
			return err
		}
		if err := emit(data); err != nil {
			return err
		}
		if gjson.GetBytes(data, "type").String() == "message_stop" {
			return nil
		}
	}
	if err := s.scanner.Err(); err != nil {
		return err
	}
	return errors.New("claude: stream ended before message_stop")
}

func neoPiFrame(data []byte) []byte {
	return []byte("event: " + gjson.GetBytes(data, "type").String() + "\ndata: " + string(data) + "\n\n")
}

func (e *ClaudeExecutor) Execute(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (result cliproxyexecutor.Response, err error) {
	prepared, err := e.prepare(ctx, auth, req, opts)
	if err != nil {
		return result, err
	}
	reporter := helps.NewExecutorUsageReporter(ctx, e, prepared.model, auth)
	defer reporter.TrackFailure(ctx, &err)
	stream, err := e.open(ctx, reporter.TrackHTTPClient(helps.NewProxyAwareHTTPClient(ctx, e.cfg, auth, 0)), prepared)
	if err != nil {
		return result, err
	}
	var transcript bytes.Buffer
	var usage helps.StreamUsageBuffer
	err = stream.consume(ctx, e.cfg, func(data []byte) error {
		if prepared.oauth {
			data = neoPiStripToolPrefix(data)
		}
		line := append([]byte("data: "), data...)
		usage.ObserveClaudeStream(line)
		reporter.ObserveResponseModel(line)
		transcript.Write(neoPiFrame(data))
		return nil
	})
	if err != nil {
		usage.PublishFailure(ctx, reporter, err)
		return result, err
	}
	data := transcript.Bytes()
	format := cliproxyexecutor.ResponseFormatOrSource(opts)
	if format == sdktranslator.FromString("claude") {
		data, err = neoPiAggregateMessage(data)
		if err != nil {
			return result, err
		}
	} else {
		var state any
		data = sdktranslator.TranslateNonStream(ctx, sdktranslator.FromString("claude"), format, req.Model, helps.ApplyPatchOriginalRequest(req, opts), prepared.body, data, &state)
		if helps.ApplyPatchTranslationError(state) != nil {
			return result, statusErr{code: http.StatusBadGateway, msg: helps.ApplyPatchUpstreamErrorMessage}
		}
		if len(data) == 0 {
			return result, errors.New("claude: response translation failed")
		}
	}
	usage.Publish(ctx, reporter)
	log.WithFields(log.Fields{"provider": e.Identifier(), "model": prepared.model}).Info("provider request completed")
	return cliproxyexecutor.Response{Payload: data, Headers: stream.response.Header.Clone()}, nil
}

func (e *ClaudeExecutor) ExecuteStream(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (_ *cliproxyexecutor.StreamResult, err error) {
	prepared, err := e.prepare(ctx, auth, req, opts)
	if err != nil {
		return nil, err
	}
	reporter := helps.NewExecutorUsageReporter(ctx, e, prepared.model, auth)
	defer reporter.TrackFailure(ctx, &err)
	stream, err := e.open(ctx, reporter.TrackHTTPClient(helps.NewProxyAwareHTTPClient(ctx, e.cfg, auth, 0)), prepared)
	if err != nil {
		return nil, err
	}
	out := make(chan cliproxyexecutor.StreamChunk, 1)
	go func() {
		defer close(out)
		var usage helps.StreamUsageBuffer
		var state any
		format := cliproxyexecutor.ResponseFormatOrSource(opts)
		helps.InitializeApplyPatchStream(ctx, sdktranslator.FormatClaude, format, req.Model, helps.ApplyPatchOriginalRequest(req, opts), prepared.body, &state)
		consumeErr := stream.consume(ctx, e.cfg, func(data []byte) error {
			if prepared.oauth {
				data = neoPiStripToolPrefix(data)
			}
			line := append([]byte("data: "), data...)
			usage.ObserveClaudeStream(line)
			reporter.ObserveResponseModel(line)
			chunks := [][]byte{neoPiFrame(data)}
			if format != sdktranslator.FromString("claude") {
				chunks = sdktranslator.TranslateStream(ctx, sdktranslator.FromString("claude"), format, req.Model, helps.ApplyPatchOriginalRequest(req, opts), prepared.body, line, &state)
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
		})
		for _, chunk := range helps.FinalizeApplyPatchStream(state) {
			select {
			case out <- cliproxyexecutor.StreamChunk{Payload: chunk}:
			case <-ctx.Done():
				return
			}
		}
		if helps.ApplyPatchTranslationError(state) != nil {
			consumeErr = statusErr{code: http.StatusBadGateway, msg: helps.ApplyPatchUpstreamErrorMessage}
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
	return &cliproxyexecutor.StreamResult{Headers: stream.response.Header.Clone(), Chunks: out}, nil
}

func (e *ClaudeExecutor) CountTokens(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	prepared, err := e.prepare(ctx, auth, req, opts, true)
	if err != nil {
		return cliproxyexecutor.Response{}, err
	}
	body := prepared.body
	path := "/v1/messages/count_tokens"
	if prepared.oauth {
		path += "?beta=true"
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, prepared.base+path, bytes.NewReader(body))
	if err != nil {
		return cliproxyexecutor.Response{}, err
	}
	request.Header = prepared.headers
	e.recordRequest(ctx, prepared, request)
	response, err := helps.NewProxyAwareHTTPClient(ctx, e.cfg, auth, 0).Do(request)
	if err != nil {
		helps.RecordAPIResponseError(ctx, e.cfg, err)
		return cliproxyexecutor.Response{}, err
	}
	defer response.Body.Close()
	helps.RecordAPIResponseMetadata(ctx, e.cfg, response.StatusCode, response.Header)
	decoded, err := util.DecodeResponseBody(response.Body, response.Header.Get("Content-Encoding"))
	if err != nil {
		return cliproxyexecutor.Response{}, err
	}
	defer decoded.Close()
	data, err := io.ReadAll(decoded)
	helps.AppendAPIResponseChunk(ctx, e.cfg, data)
	if err != nil {
		return cliproxyexecutor.Response{}, err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return cliproxyexecutor.Response{}, &neoPiUpstreamError{code: response.StatusCode, body: string(data), headers: response.Header}
	}
	return cliproxyexecutor.Response{Payload: data, Headers: response.Header.Clone()}, nil
}

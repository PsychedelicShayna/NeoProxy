package neopool

import (
	"context"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/auth/anthropicnpi"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/auth/codex"
	devinauth "github.com/router-for-me/CLIProxyAPI/v8/internal/auth/devin"
	kimiauth "github.com/router-for-me/CLIProxyAPI/v8/internal/auth/kimi"
	xaiauth "github.com/router-for-me/CLIProxyAPI/v8/internal/auth/xai"
	zaiauth "github.com/router-for-me/CLIProxyAPI/v8/internal/auth/zai"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/neo"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/openaiwire"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
)

// Native OAuth subscriptions (Claude, Codex/ChatGPT) report their own 5h and
// weekly usage. NeoProxy asks the provider with the account's own access token,
// keeps the answer in memory and shows it as Account.ProviderWindows. The token
// is read from the engine auth (Metadata["access_token"], the same credential
// the executors send); nothing is persisted and tokens are never logged.
const (
	// quotaPollInterval is how often the background poller re-reads every account.
	quotaPollInterval = 5 * time.Minute
	// quotaKeepLastGood is how long the last successful reading is still shown
	// after the provider stops answering; later the windows become unknown.
	quotaKeepLastGood   = 30 * time.Minute
	quotaRequestTimeout = 15 * time.Second
	quotaIdleTimeout    = 90 * time.Second

	claudeUsageURL = "https://api.anthropic.com/api/oauth/usage"
	codexUsageURL  = "https://chatgpt.com/backend-api/wham/usage"

	quotaSourceReported    = "provider-reported"
	quotaSourceUnavailable = "provider-unavailable"
)

type quotaEntry struct {
	windows []Window // last good reading
	goodAt  time.Time
	failing bool
	gen     uint64 // request generation that produced this entry
}

// quotaProber caches provider-reported windows by engine auth ID.
type quotaProber struct {
	mu        sync.Mutex
	entries   map[string]quotaEntry
	gen       uint64
	claudeURL string
	codexURL  string
	clientFor func(*coreauth.Auth) *http.Client
}

func newQuotaProber() *quotaProber {
	clients := &quotaClients{clients: map[string]*http.Client{}}
	return &quotaProber{entries: map[string]quotaEntry{}, claudeURL: claudeUsageURL, codexURL: codexUsageURL, clientFor: clients.client}
}

// quotaClients keeps one client per proxy setting, so repeated polls reuse
// their connections instead of leaving a new transport's idle ones behind.
type quotaClients struct {
	mu      sync.Mutex
	clients map[string]*http.Client
}

// client never follows redirects with a bearer. Only an explicit http(s)
// proxy on the auth is honoured; otherwise the environment decides.
func (c *quotaClients) client(a *coreauth.Auth) *http.Client {
	proxy := strings.TrimSpace(a.ProxyURL)
	key, proxyFunc := "", http.ProxyFromEnvironment
	switch {
	case strings.EqualFold(proxy, "direct") || strings.EqualFold(proxy, "none"):
		key, proxyFunc = "direct", nil
	case proxy != "":
		if u, err := url.Parse(proxy); err == nil && (u.Scheme == "http" || u.Scheme == "https") {
			key, proxyFunc = "proxy:"+u.String(), http.ProxyURL(u)
		}
	}
	timeout, idleTimeout := quotaRequestTimeout, quotaIdleTimeout
	if a.Provider == "claude" {
		key += ":claude"
		timeout, idleTimeout = 0, 0
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if client, ok := c.clients[key]; ok {
		return client
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = proxyFunc
	transport.IdleConnTimeout = idleTimeout
	client := &http.Client{Timeout: timeout, Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	c.clients[key] = client
	return client
}

// quotaProvider reports which usage endpoint serves this credential, or ""
// when it is not a native OAuth sign-in of a provider that has one.
func quotaProvider(a *coreauth.Auth) string {
	if a == nil || a.Attributes["neopi_source_id"] != "" {
		return ""
	}
	if a.Provider == "claude" && strings.Contains(quotaAccessToken(a), "sk-ant-oat") {
		return "claude"
	}
	// NeoPi meters every Z.ai credential, pasted key or sign-in, with its key.
	if zaiauth.IsCodingPlanBaseURL(a.Attributes["base_url"]) {
		return "zai"
	}
	// OpenCode Go reports its subscription windows for the account's key.
	if registry.GatewayProviderFor(a.Attributes["base_url"]) == registry.GatewayOpenCodeGo && quotaAccessToken(a) != "" {
		return "opencode-go"
	}
	if a.AuthKind() != coreauth.AuthKindOAuth {
		return ""
	}
	if kind := accessKind(a); kind != "subscription" && kind != "imported" {
		return ""
	}
	switch strings.ToLower(strings.TrimSpace(a.Provider)) {
	case "claude":
		return "claude"
	case "codex":
		return "codex"
	case "xai":
		return "xai"
	case "kimi":
		return "kimi"
	case "devin":
		return "devin"
	case "meta":
		return "meta"
	}
	return ""
}

// openCodeGoUsage is NeoPi's GET /zen/go/v1/usage report.
type openCodeGoUsage struct {
	Rolling *openCodeGoWindow `json:"-"`
	Weekly  *openCodeGoWindow `json:"-"`
	Monthly *openCodeGoWindow `json:"-"`
}

// openCodeGoWindow is one window of the report: a floored, clamped 0-100
// percent, a status and the server-computed reset.
type openCodeGoWindow struct {
	Percent  float64
	Status   string
	ResetsAt time.Time
}

// openCodeGoUsageURL maps the account's inference endpoint back to the Zen
// account endpoint NeoPi reads usage from.
func openCodeGoUsageURL(baseURL string) string {
	base := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if trimmed, ok := strings.CutSuffix(base, "/v1"); ok {
		base = trimmed
	}
	if base == "https://opencode.ai/inference/go" {
		base = "https://opencode.ai/zen/go"
	}
	if base == "" {
		base = "https://opencode.ai/zen/go"
	}
	return base + "/v1/usage"
}

// fetchOpenCodeGoUsage is NeoPi's usage provider: bearer key, NeoPi's user
// agent and the install-id session header. 401/403 mean the key is not usable
// (NeoPi throws, so the credential is flagged); any other failure is transient.
func fetchOpenCodeGoUsage(ctx context.Context, client *http.Client, a *coreauth.Auth, key string) (*openCodeGoUsage, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, openCodeGoUsageURL(a.Attributes["base_url"]), nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Authorization", "Bearer "+key)
	request.Header.Set("User-Agent", openaiwire.NeoPiUserAgent)
	request.Header.Set("x-opencode-session", uuid.NewSHA1(uuid.NameSpaceOID, []byte("cli-proxy-api:gateway:install\x00opencode-go\x00"+a.ID)).String())
	response, err := client.Do(request)
	if err != nil {
		return nil, err
	}
	defer func() { _ = response.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	if response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden {
		if message := gjson.GetBytes(body, "error.message").String(); message != "" {
			return nil, fmt.Errorf("OpenCode Go usage endpoint returned %d: %s", response.StatusCode, message)
		}
		return nil, fmt.Errorf("OpenCode Go usage endpoint returned %d", response.StatusCode)
	}
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("OpenCode Go usage endpoint returned %d", response.StatusCode)
	}
	usage := &openCodeGoUsage{
		Rolling: openCodeGoWindowAt(body, "rolling"),
		Weekly:  openCodeGoWindowAt(body, "weekly"),
		Monthly: openCodeGoWindowAt(body, "monthly"),
	}
	// All-or-nothing: a partial report must not replace the last good one.
	if usage.Rolling == nil || usage.Weekly == nil || usage.Monthly == nil {
		return nil, fmt.Errorf("OpenCode Go usage response missing or malformed windows")
	}
	return usage, nil
}

// openCodeGoWindowAt decodes one window; nil when any field is malformed.
func openCodeGoWindowAt(body []byte, key string) *openCodeGoWindow {
	window := gjson.GetBytes(body, "usage."+key)
	percent := window.Get("percent")
	status := window.Get("status").String()
	if percent.Type != gjson.Number || percent.Float() < 0 || percent.Float() > 100 || (status != "ok" && status != "rate-limited") {
		return nil
	}
	resets, err := time.Parse(time.RFC3339Nano, window.Get("resetsAt").String())
	if err != nil {
		return nil
	}
	return &openCodeGoWindow{Percent: percent.Float(), Status: status, ResetsAt: resets}
}

// openCodeGoUsageWindows adapts NeoPi's report: the rolling 5h and weekly
// windows gate routing, the monthly window is display-only.
func openCodeGoUsageWindows(usage *openCodeGoUsage) []Window {
	if usage == nil {
		return nil
	}
	windows := []Window{
		percentWindow("5h", usage.Rolling.Percent, usage.Rolling.ResetsAt.Add(-5*time.Hour), usage.Rolling.ResetsAt),
		percentWindow("week", usage.Weekly.Percent, usage.Weekly.ResetsAt.Add(-7*24*time.Hour), usage.Weekly.ResetsAt),
	}
	// The monthly window anchors on the subscription anniversary (no fixed
	// span) and never gates routing: an exhausted monthly can still serve a
	// key whose console balance fallback is enabled.
	month := percentWindow("month", usage.Monthly.Percent, time.Time{}, usage.Monthly.ResetsAt)
	month.StartsAt = 0
	month.Known = false
	return append(windows, month)
}

func quotaAccessToken(a *coreauth.Auth) string {
	if a == nil {
		return ""
	}
	if a.Provider == "meta" {
		// A Muse sign-in is read with its account token, not the minted key.
		return metaDeviceToken(a)
	}
	token, _ := a.Metadata["access_token"].(string)
	if token == "" && zaiauth.IsCodingPlanBaseURL(a.Attributes["base_url"]) {
		token = a.Attributes["api_key"]
	}
	if token == "" && registry.GatewayProviderFor(a.Attributes["base_url"]) == registry.GatewayOpenCodeGo {
		// An OpenCode Go key is the account's api_key.
		token = a.Attributes["api_key"]
		if token == "" {
			token, _ = a.Metadata["api_key"].(string)
		}
	}
	if token == "" && a.Provider == "devin" {
		// Devin sign-ins store the session token as their api_key.
		token = a.Attributes["api_key"]
		if token == "" {
			token, _ = a.Metadata["api_key"].(string)
		}
	}
	if token == "" && a.Provider == "claude" {
		token = a.Attributes["api_key"]
		if token == "" {
			token, _ = a.Metadata["api_key"].(string)
		}
		if !strings.Contains(token, "sk-ant-oat") {
			return ""
		}
	}
	return strings.TrimSpace(token)
}

// codexChatGPTAccountID is the account the sign-in stored, falling back to the
// access token's chatgpt_account_id claim (credential.accountId ?? JWT).
func codexChatGPTAccountID(a *coreauth.Auth, token string) string {
	if id, _ := a.Metadata["account_id"].(string); strings.TrimSpace(id) != "" {
		return strings.TrimSpace(id)
	}
	if claims, err := codex.ParseJWTToken(token); err == nil {
		return claims.GetAccountID()
	}
	return ""
}

func percentWindow(kind string, percent float64, startsAt, resetsAt time.Time) Window {
	percent = math.Min(100, math.Max(0, percent))
	w := Window{Kind: kind, Known: true, Source: quotaSourceReported, Unit: "percent", Capacity: 100, UsedTokens: int64(math.Round(percent)), Used: percent / 100}
	if !resetsAt.IsZero() {
		w.ResetsAt = resetsAt.UnixMilli()
		w.StartsAt = startsAt.UnixMilli()
	}
	return w
}

// claudeUsageWindows adapts NeoPi's shared gates to the routing/dashboard model.
// A null, malformed or reset-only bucket does not invent a zero-percent meter.
func claudeUsageWindows(usage *anthropicnpi.Usage) ([]Window, error) {
	out := make([]Window, 0, 2)
	for _, spec := range []struct {
		bucket *anthropicnpi.UsageBucket
		kind   string
		length time.Duration
	}{{usage.FiveHour, "5h", 5 * time.Hour}, {usage.SevenDay, "week", 7 * 24 * time.Hour}} {
		if spec.bucket == nil || spec.bucket.Utilization == nil {
			continue
		}
		var starts, resets time.Time
		if reset, err := time.Parse(time.RFC3339Nano, spec.bucket.ResetsAt); err == nil {
			resets, starts = reset, reset.Add(-spec.length)
		}
		out = append(out, percentWindow(spec.kind, *spec.bucket.Utilization, starts, resets))
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("usage response has no shared windows")
	}
	return out, nil
}

// codexUsageWindows adapts NeoPi's plan windows to the 5h/week projection. A
// window of at most five hours is the 5h window, a longer one the weekly
// window; without a length the primary is 5h, the secondary week. A window
// without a percentage is unknown and is not shown as zero use. Separately
// metered allowances (Spark) do not gate other models and are not projected.
func codexUsageWindows(usage *codex.Usage, now time.Time) ([]Window, error) {
	if usage == nil {
		return nil, fmt.Errorf("usage response has no windows")
	}
	out := []Window{}
	seen := map[string]bool{}
	for i, v := range []*codex.UsageWindow{usage.Primary, usage.Secondary} {
		if v == nil || v.UsedPercent == nil {
			continue
		}
		kind, length := "5h", 5*time.Hour
		if v.LimitWindowSeconds != nil && *v.LimitWindowSeconds > 0 {
			length = time.Duration(*v.LimitWindowSeconds * float64(time.Second))
			if length > 5*time.Hour {
				kind = "week"
			}
		} else if i == 1 {
			kind, length = "week", 7*24*time.Hour
		}
		if seen[kind] {
			continue
		}
		seen[kind] = true
		resets, _ := v.ResetTime(now)
		out = append(out, percentWindow(kind, *v.UsedPercent, resets.Add(-length), resets))
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("usage response has no windows")
	}
	return out, nil
}

// xaiUsageWindows adapts NeoPi's SuperGrok report: weekly credits as the week
// window, unified monthly included quota as the month window. A window NeoPi
// does not gate on (an advisory monthly counter, or spent credits with
// on-demand headroom left) is reported but not known to routing.
func xaiUsageWindows(usage *xaiauth.Usage) []Window {
	out := []Window{}
	if weekly := usage.Weekly; weekly != nil {
		w := percentWindow("week", weekly.CreditUsagePercent, weekly.PeriodStart, weekly.PeriodEnd)
		w.Known = w.Used < 1 || !usage.OnDemandHeadroom()
		out = append(out, w)
	}
	if monthly := usage.Monthly; monthly != nil {
		w := percentWindow("month", math.Min(monthly.Used/monthly.Limit, 1)*100, monthly.PeriodStart, monthly.PeriodEnd)
		w.Known = !usage.MonthlyAdvisory && (w.Used < 1 || !usage.OnDemandHeadroom())
		out = append(out, w)
	}
	return out
}

// zaiUsageWindows adapts NeoPi's Z.ai report: each shared quota whose window
// the budget model knows (5h, day, week, month) becomes that window. A limit
// without a fraction is reported but unknown to routing.
func zaiUsageWindows(usage *zaiauth.Usage) []Window {
	out := []Window{}
	if usage == nil {
		return out
	}
	kinds := map[string]string{"5h": "5h", "1d": "day", "1w": "week", "1mo": "month"}
	for _, limit := range usage.Limits {
		kind, ok := kinds[limit.WindowID]
		if !ok || !limit.Shared {
			continue
		}
		startsAt := time.Time{}
		if !limit.ResetsAt.IsZero() && limit.DurationMs > 0 {
			startsAt = limit.ResetsAt.Add(-time.Duration(limit.DurationMs) * time.Millisecond)
		}
		if limit.UsedFraction == nil {
			out = append(out, Window{Kind: kind, Source: quotaSourceReported, Unit: "percent", Capacity: 100})
			continue
		}
		out = append(out, percentWindow(kind, *limit.UsedFraction*100, startsAt, limit.ResetsAt))
	}
	return out
}

// kimiUsageWindows adapts NeoPi's Kimi Code report the way its ranking
// strategy reads it: the first 5h limit and the first 7d limit gate the
// account; a limit without a duration spans its window's default. A limit
// without a fraction is reported but unknown to routing.
func kimiUsageWindows(usage *kimiauth.Usage) []Window {
	out := []Window{}
	if usage == nil {
		return out
	}
	windows := map[string]struct {
		kind     string
		duration time.Duration
	}{"5h": {"5h", 5 * time.Hour}, "7d": {"week", 7 * 24 * time.Hour}}
	seen := map[string]bool{}
	for _, limit := range usage.Limits {
		window, ok := windows[limit.WindowID]
		if !ok || seen[limit.WindowID] {
			continue
		}
		seen[limit.WindowID] = true
		if limit.UsedFraction == nil {
			out = append(out, Window{Kind: window.kind, Source: quotaSourceReported, Unit: "percent", Capacity: 100})
			continue
		}
		duration := window.duration
		if limit.DurationMs > 0 {
			duration = time.Duration(limit.DurationMs) * time.Millisecond
		}
		startsAt := time.Time{}
		if !limit.ResetsAt.IsZero() {
			startsAt = limit.ResetsAt.Add(-duration)
		}
		out = append(out, percentWindow(window.kind, *limit.UsedFraction*100, startsAt, limit.ResetsAt))
	}
	return out
}

// devinUsageWindows adapts NeoPi's Devin report the way its default ranking
// gates on it: the daily and weekly quota become the day and week windows;
// the plan-period credit buckets become the month window at the most consumed
// bucket, where a bucket without a grant counts as used up once its balance
// is spent (isUsageLimitExhausted). A window without a fraction is reported
// but unknown to routing.
func devinUsageWindows(usage *devinauth.Usage) []Window {
	out := []Window{}
	if usage == nil {
		return out
	}
	window := func(kind string, fraction float64, limit devinauth.UsageLimit) Window {
		resets := time.Time{}
		if limit.Window.ResetsAt > 0 {
			resets = time.UnixMilli(int64(limit.Window.ResetsAt))
		}
		starts := time.Time{}
		if !resets.IsZero() && limit.Window.DurationMs > 0 {
			starts = resets.Add(-time.Duration(limit.Window.DurationMs) * time.Millisecond)
		}
		return percentWindow(kind, fraction*100, starts, resets)
	}
	var credits *Window
	creditFraction, creditReported := -1.0, false
	for _, limit := range usage.Limits {
		switch limit.ID {
		case "devin:quota:daily":
			out = append(out, window("day", *limit.Amount.UsedFraction, limit))
		case "devin:quota:weekly":
			out = append(out, window("week", *limit.Amount.UsedFraction, limit))
		default:
			creditReported = true
			fraction := -1.0
			if limit.Amount.UsedFraction != nil {
				fraction = math.Min(*limit.Amount.UsedFraction, 1)
			} else if limit.Amount.Remaining <= 0 {
				fraction = 1
			}
			if fraction > creditFraction {
				creditFraction = fraction
				w := window("month", fraction, limit)
				credits = &w
			}
		}
	}
	switch {
	case credits != nil:
		out = append(out, *credits)
	case creditReported:
		out = append(out, Window{Kind: "month", Source: quotaSourceReported, Unit: "percent", Capacity: 100})
	}
	return out
}

// fetch asks the provider for the account's current windows.
func (q *quotaProber) fetch(ctx context.Context, a *coreauth.Auth, now time.Time) ([]Window, error) {
	token := quotaAccessToken(a)
	if token == "" {
		return nil, fmt.Errorf("sign-in has no access token")
	}
	client := q.clientFor(a)
	switch quotaProvider(a) {
	case "claude":
		baseURL := a.Attributes["base_url"]
		if baseURL == "" {
			baseURL, _ = a.Metadata["base_url"].(string)
		}
		if q.claudeURL != claudeUsageURL {
			baseURL = strings.TrimSuffix(q.claudeURL, "/api/oauth/usage")
		}
		usage, err := (&anthropicnpi.Client{HTTP: client}).Usage(ctx, token, baseURL)
		if err != nil {
			return nil, err
		}
		return claudeUsageWindows(usage)
	case "codex":
		endpoint := q.codexURL
		if endpoint == codexUsageURL {
			baseURL := a.Attributes["base_url"]
			if baseURL == "" {
				baseURL, _ = a.Metadata["base_url"].(string)
			}
			endpoint = codex.UsageURL(baseURL)
		}
		usage, err := codex.FetchUsage(ctx, client, endpoint, token, codexChatGPTAccountID(a, token))
		if err != nil {
			return nil, err
		}
		return codexUsageWindows(usage, now)
	case "xai":
		usage, err := xaiauth.FetchUsage(ctx, client, token, now)
		if err != nil {
			return nil, err
		}
		return xaiUsageWindows(usage), nil
	case "zai":
		usage, err := zaiauth.FetchUsage(ctx, client, token)
		if err != nil {
			return nil, err
		}
		return zaiUsageWindows(usage), nil
	case "opencode-go":
		usage, err := fetchOpenCodeGoUsage(ctx, client, a, token)
		if err != nil {
			return nil, err
		}
		return openCodeGoUsageWindows(usage), nil
	case "kimi":
		// NeoPi skips the probe while the stored token is expired; the next
		// poll carries the refreshed one.
		if expires, ok := a.ExpirationTime(); ok && !expires.After(now) {
			return nil, fmt.Errorf("kimi usage: access token expired")
		}
		authDir := ""
		if path := a.Attributes[coreauth.AttributePath]; path != "" {
			authDir = filepath.Dir(path)
		}
		usage, err := kimiauth.FetchUsage(ctx, client, authDir, token, now)
		if err != nil {
			return nil, err
		}
		return kimiUsageWindows(usage), nil
	case "devin":
		usage, err := devinauth.FetchUsage(ctx, client, a.Attributes["base_url"], token)
		if err != nil {
			return nil, err
		}
		return devinUsageWindows(usage), nil
	case "meta":
		return fetchMetaUsage(ctx, client, a, now)
	}
	return nil, fmt.Errorf("provider has no usage endpoint")
}

// refresh fetches and caches one account. A failure keeps the last good reading.
// A result older than the stored one (a slow poll overtaken by a manual refresh)
// is dropped, so a newer reading is never overwritten.
func (q *quotaProber) refresh(ctx context.Context, a *coreauth.Auth, now time.Time) error {
	q.mu.Lock()
	q.gen++
	gen := q.gen
	q.mu.Unlock()
	windows, err := q.fetch(ctx, a, now)
	q.mu.Lock()
	entry := q.entries[a.ID]
	if entry.gen > gen {
		q.mu.Unlock()
		return err
	}
	wasFailing := entry.failing
	if err == nil {
		entry = quotaEntry{windows: windows, goodAt: now, gen: gen}
	} else {
		entry.failing = true
		entry.gen = gen
	}
	q.entries[a.ID] = entry
	q.mu.Unlock()
	if err != nil {
		entryLog := log.WithField("account", AccountID(a.ID)).WithField("provider", a.Provider).WithError(err)
		if wasFailing {
			entryLog.Debug("NeoProxy quota: provider usage still unavailable")
		} else {
			entryLog.Warn("NeoProxy quota: provider usage unavailable")
		}
	}
	return err
}

// windows returns what the snapshot shows for a credential: nil when nothing
// was asked yet, the last good reading for 30 minutes after a failure, and
// unknown windows afterwards.
func (q *quotaProber) windows(a *coreauth.Auth, now time.Time) []Window {
	if quotaProvider(a) == "" {
		return nil
	}
	q.mu.Lock()
	entry, ok := q.entries[a.ID]
	q.mu.Unlock()
	if !ok {
		return nil
	}
	if !entry.goodAt.IsZero() && now.Sub(entry.goodAt) < quotaKeepLastGood {
		return append([]Window(nil), entry.windows...)
	}
	kinds := []string{"5h", "week"}
	if quotaProvider(a) == "xai" {
		// SuperGrok reports weekly credits (or a monthly quota), no 5h window.
		kinds = []string{"week"}
	}
	if len(entry.windows) > 0 {
		kinds = kinds[:0]
		for _, w := range entry.windows {
			kinds = append(kinds, w.Kind)
		}
	}
	out := make([]Window, 0, len(kinds))
	for _, kind := range kinds {
		out = append(out, Window{Kind: kind, Known: false, Source: quotaSourceUnavailable, Unit: "percent", Capacity: 100})
	}
	return out
}

// forget drops accounts the engine no longer has.
func (q *quotaProber) forget(live map[string]bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	for id := range q.entries {
		if !live[id] {
			delete(q.entries, id)
		}
	}
}

// RefreshQuota reads one credential's provider usage now. It reports false when
// the credential has no provider usage endpoint.
func (s *Store) RefreshQuota(ctx context.Context, a *coreauth.Auth) (bool, error) {
	if quotaProvider(a) == "" {
		return false, nil
	}
	err := s.quota.refresh(ctx, a, s.now())
	s.notifyChanged()
	return true, err
}

func (s *Store) notifyChanged() {
	s.mu.Lock()
	s.notifyLocked()
	s.mu.Unlock()
}

func (s *Store) refreshAllQuota(ctx context.Context) {
	live := map[string]bool{}
	for _, a := range s.manager.List() {
		if ctx.Err() != nil {
			return
		}
		if quotaProvider(a) == "" {
			continue
		}
		live[a.ID] = true
		_ = s.quota.refresh(ctx, a, s.now())
	}
	s.quota.forget(live)
	s.notifyChanged()
}

// pollQuota refreshes every native OAuth account every quotaPollInterval until
// ctx is cancelled.
func (s *Store) pollQuota(ctx context.Context) {
	ticker := time.NewTicker(quotaPollInterval)
	defer ticker.Stop()
	for {
		s.refreshAllQuota(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// accessQuotaRefresh is POST /v8/neo/access/:id/quota/refresh: read the
// provider's usage for one sign-in now and return the updated item.
func (h *HTTP) accessQuotaRefresh(c *gin.Context) {
	p, ok := session(c)
	if !ok {
		return
	}
	a, owner, ok := h.accessTarget(c, p)
	if !ok {
		return
	}
	if p.Role != neo.RoleAdmin && !(owner == p.UserID && p.Role == neo.RoleOwner) {
		poolError(c, http.StatusForbidden, "Only the contributor or an administrator can refresh this usage.")
		return
	}
	supported, err := h.pool.RefreshQuota(c.Request.Context(), a)
	if !supported {
		poolError(c, http.StatusConflict, "The provider does not report usage for this kind of access.")
		return
	}
	if err != nil {
		poolError(c, http.StatusBadGateway, fmt.Sprintf("The provider did not report usage (%s). NeoProxy keeps the last reading for 30 minutes and retries every 5 minutes.", err.Error()))
		return
	}
	h.respondItem(c, p, a.ID, http.StatusOK)
}

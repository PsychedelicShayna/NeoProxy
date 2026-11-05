package antigravity

import (
	"context"
	"errors"
	"math"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/antigravitywire"
	"github.com/tidwall/gjson"
)

// NeoPi's Antigravity usage report (usage/google-antigravity.ts): the
// retrieveUserQuotaSummary buckets with the legacy fetchAvailableModels model
// counters as the fallback, in NeoPi's report shape.
type Usage struct {
	Provider  string        `json:"provider"`
	FetchedAt float64       `json:"fetchedAt"`
	Limits    []UsageLimit  `json:"limits"`
	Metadata  UsageMetadata `json:"metadata"`
}

// UsageMetadata is the report's endpoint and account identity.
type UsageMetadata struct {
	Endpoint  string `json:"endpoint"`
	ProjectID string `json:"projectId"`
	Email     string `json:"email,omitempty"`
	AccountID string `json:"accountId,omitempty"`
}

// UsageLimit is one quota window of one backend counter.
type UsageLimit struct {
	ID     string       `json:"id"`
	Label  string       `json:"label"`
	Scope  UsageScope   `json:"scope"`
	Window *UsageWindow `json:"window,omitempty"`
	Amount UsageAmount  `json:"amount"`
	Status string       `json:"status"`
}

// UsageScope names the counter, tier and window a limit belongs to. Shared
// limits are the one third-party quota Antigravity exposes to both the Claude
// and GPT families.
type UsageScope struct {
	Provider    string `json:"provider"`
	AccountID   string `json:"accountId,omitempty"`
	ProjectID   string `json:"projectId,omitempty"`
	Tier        string `json:"tier,omitempty"`
	WindowID    string `json:"windowId,omitempty"`
	Shared      bool   `json:"shared,omitempty"`
	SharedGroup string `json:"sharedGroup,omitempty"`
}

// UsageWindow is a limit's period; ResetsAt is epoch milliseconds.
type UsageWindow struct {
	ID         string   `json:"id"`
	Label      string   `json:"label"`
	DurationMs *float64 `json:"durationMs,omitempty"`
	ResetsAt   *float64 `json:"resetsAt,omitempty"`
}

// UsageAmount is a limit's consumption; absent fields are omitted as NeoPi
// omits them.
type UsageAmount struct {
	Used              *float64 `json:"used,omitempty"`
	Limit             *float64 `json:"limit,omitempty"`
	Remaining         *float64 `json:"remaining,omitempty"`
	UsedFraction      *float64 `json:"usedFraction,omitempty"`
	RemainingFraction *float64 `json:"remainingFraction,omitempty"`
	Unit              string   `json:"unit"`
}

const (
	quotaSummaryPath    = "/v1internal:retrieveUserQuotaSummary"
	availableModelsPath = "/v1internal:fetchAvailableModels"
	hourMs              = 60 * 60 * 1000
	dayMs               = 24 * hourMs
	weekMs              = 7 * dayMs
	usageStatusOK       = "ok"
	usageStatusWarn     = "warning"
	usageStatusSpent    = "exhausted"
	usageStatusNaN      = "unknown"
)

// UsageRequest is one report request: whose token, which project, and the
// optional base URL override.
type UsageRequest struct {
	// Client issues the requests; nil uses http.DefaultClient.
	Client *http.Client
	// BaseURL overrides the endpoint list (a proxy or a test server).
	BaseURL string
	// AccessToken is the OAuth access token; the caller owns refresh.
	AccessToken string
	// ProjectID is the Cloud Code Assist project the quota is read for.
	ProjectID string
	Email     string
	AccountID string
	// ExpiresAt, when set, short-circuits an expired token the way NeoPi's
	// resolveAccessToken does instead of posting it to Google.
	ExpiresAt time.Time
	// Now is the clock used for window inference and the report timestamp.
	Now func() time.Time
}

// FetchUsage reads an account's quota report. A nil report with no error is
// NeoPi's `null`: the credential cannot be probed (no project, no or expired
// token) or the endpoints answered without a usable report.
func FetchUsage(ctx context.Context, req UsageRequest) (*Usage, error) {
	if req.ProjectID == "" {
		return nil, nil
	}
	now := time.Now
	if req.Now != nil {
		now = req.Now
	}
	accessToken := req.AccessToken
	if accessToken == "" {
		return nil, nil
	}
	if !req.ExpiresAt.IsZero() && !req.ExpiresAt.After(now()) {
		return nil, nil
	}
	nowMs := float64(now().UnixMilli())
	baseURL := strings.TrimRight(req.BaseURL, "/")
	endpoints := []string{antigravitywire.DailyEndpoint, antigravitywire.SandboxEndpoint}
	if baseURL != "" {
		endpoints = []string{baseURL}
	}
	client := req.Client
	if client == nil {
		client = http.DefaultClient
	}
	credential := UsageCredential{Provider: Provider, ProjectID: req.ProjectID, Email: req.Email, AccountID: req.AccountID}

	// The endpoint Antigravity's own quota UI uses; unlike the legacy model
	// catalog it reports the rolling five-hour and weekly buckets.
	summary, err := fetchAntigravityEndpoint(ctx, client, endpoints, quotaSummaryPath, req.ProjectID, accessToken)
	if err != nil {
		return nil, err
	}
	if summary.ok {
		if summary.payload.Type == gjson.Null {
			// buildQuotaSummaryReport reads data.groups from a null body.
			return nil, errors.New("null is not an object (evaluating 'data.groups')")
		}
		summaryReport, errReport := buildQuotaSummaryReport(summary.payload, credential, summary.endpoint, nowMs)
		if errReport != nil {
			return nil, errReport
		}
		if summaryReport != nil {
			return summaryReport, nil
		}
	}

	// Older Cloud Code Assist deployments and compatible proxies may not
	// expose retrieveUserQuotaSummary; the model-level parser stays as the
	// fallback.
	legacy, err := fetchAntigravityEndpoint(ctx, client, endpoints, availableModelsPath, req.ProjectID, accessToken)
	if err != nil {
		return nil, err
	}
	if !legacy.ok {
		// NeoPi reports a failed fallback as no report at all.
		return nil, nil
	}
	if legacy.payload.Type == gjson.Null {
		// The legacy parser reads data.models from a null body.
		return nil, errors.New("null is not an object (evaluating 'data.models')")
	}
	return buildModelQuotaReport(legacy.payload, credential, legacy.endpoint, nowMs)
}

// UsageCredential is NeoPi's UsageFetchParams credential projection.
type UsageCredential struct {
	Provider  string
	ProjectID string
	Email     string
	AccountID string
}

// usageEndpointResult is fetchAntigravityEndpoint's result.
type usageEndpointResult struct {
	ok       bool
	status   int
	endpoint string
	payload  gjson.Result
}

// fetchAntigravityEndpoint posts {project} to one path, failing over to the
// next endpoint on a connection error or a transient status.
func fetchAntigravityEndpoint(ctx context.Context, client *http.Client, endpoints []string, path, projectID, accessToken string) (usageEndpointResult, error) {
	result := usageEndpointResult{}
	if len(endpoints) == 0 {
		return result, errors.New("antigravity usage: no endpoint")
	}
	body := `{"project":` + jsonQuote(projectID) + `}`
	for index, endpoint := range endpoints {
		result.endpoint = endpoint
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint+path, strings.NewReader(body))
		if err != nil {
			return result, err
		}
		req.Header.Set("Authorization", "Bearer "+accessToken)
		req.Header.Set("Content-Type", "application/json")
		antigravitywire.SetClientHeaders(req.Header, "")
		resp, errDo := client.Do(req)
		if errDo != nil {
			if index == len(endpoints)-1 {
				return result, errDo
			}
			continue
		}
		text, errRead := readBody(resp)
		status := resp.StatusCode
		_ = resp.Body.Close()
		if errRead != nil {
			if index == len(endpoints)-1 {
				return result, errRead
			}
			continue
		}
		result.status = status
		result.ok = status >= 200 && status < 300
		if result.ok {
			// Bun's response.json() yields null for an empty body and rejects any
			// other non-JSON text, whitespace included.
			if text == "" {
				result.payload = gjson.Parse("null")
				return result, nil
			}
			if !gjson.Valid(text) {
				return result, errors.New("Failed to parse JSON")
			}
			result.payload = gjson.Parse(text)
			return result, nil
		}
		if !isTransientStatus(status) {
			return result, nil
		}
	}
	return result, nil
}

// isTransientStatus is isTransientStatus: 408, 429 and 5xx.
func isTransientStatus(status int) bool {
	return status == http.StatusRequestTimeout || status == http.StatusTooManyRequests || status >= 500
}

// quotaSummaryResponse is AntigravityQuotaSummaryResponse.
type quotaSummaryResponse struct {
	groups    []gjson.Result
	buckets   []gjson.Result
	hasGroups bool
	hasFlat   bool
}

type summaryGroup struct {
	present     bool
	displayName *string
	buckets     []gjson.Result
}

type quotaSummaryBucket struct {
	raw             gjson.Result
	bucketID        *string
	displayName     *string
	window          *string
	remaining       *float64
	remainingAmount gjson.Result
	disabled        bool
	resetTime       *string
}

// buildQuotaSummaryReport is buildQuotaSummaryReport.
func buildQuotaSummaryReport(payload gjson.Result, credential UsageCredential, endpoint string, nowMs float64) (*Usage, error) {
	groups := arrayValues(payload.Get("groups"))
	topLevel := arrayValues(payload.Get("buckets"))
	hasGroupedBuckets := false
	for _, raw := range groups {
		if len(arrayValues(raw.Get("buckets"))) > 0 {
			hasGroupedBuckets = true
			break
		}
	}
	if !hasGroupedBuckets && len(topLevel) == 0 {
		return nil, nil
	}
	report := &Usage{Provider: credential.Provider, FetchedAt: nowMs, Limits: []UsageLimit{}}
	addBucket := func(raw gjson.Result, group summaryGroup) {
		bucket := readSummaryBucket(raw)
		if bucket.disabled {
			return
		}
		descriptor := classifyWindow(bucket.window, bucket.displayName)
		var descriptorLabel *string
		if descriptor != nil {
			label := descriptor.label
			descriptorLabel = &label
		}
		window := parseWindow(quotaWindowInfo{resetTime: bucket.resetTime, windowID: bucket.window, windowLabel: descriptorLabel}, descriptor)
		amount := buildQuotaSummaryAmount(bucket)
		counterKeys := quotaSummaryCounterKeys(group, bucket)
		sharedGroup := ""
		if len(counterKeys) > 1 {
			name := "third-party"
			if bucket.bucketID != nil {
				name = *bucket.bucketID
			} else if group.displayName != nil {
				name = *group.displayName
			}
			sharedGroup = name + ":" + windowOrFallback(window, bucket.window, bucket.bucketID, "default")
		}
		bucketLabel := ""
		if bucket.bucketID != nil && strings.HasPrefix(strings.ToLower(*bucket.bucketID), "gemini-argon-") {
			bucketLabel = "Gemini (Argon)"
		}
		for _, counterKey := range counterKeys {
			windowID := windowOrFallback(window, bucket.window, bucket.bucketID, "default")
			label := ""
			switch {
			case sharedGroup != "":
				label = "Claude & GPT (shared)"
			case bucketLabel != "":
				label = bucketLabel
			case counterKey == "google":
				label = "Gemini"
			default:
				label = quotaSummaryCounterName(counterKey)
			}
			if label == "" {
				switch {
				case group.displayName != nil:
					label = *group.displayName
				case bucket.displayName != nil:
					label = *bucket.displayName
				default:
					label = "Usage"
				}
			}
			bucketID := windowID
			if bucket.bucketID != nil {
				bucketID = *bucket.bucketID
			}
			id := credential.Provider + ":" + counterKey + ":default:" + bucketID
			scope := UsageScope{
				Provider:  credential.Provider,
				AccountID: credential.AccountID,
				ProjectID: credential.ProjectID,
				WindowID:  windowID,
			}
			if sharedGroup != "" {
				scope.Shared = true
				scope.SharedGroup = sharedGroup
			}
			status := usageStatusNaN
			if amount.RemainingFraction != nil {
				status = remainingStatus(*amount.RemainingFraction)
			} else if amount.Remaining != nil && *amount.Remaining == 0 {
				status = usageStatusSpent
			}
			report.Limits = append(report.Limits, UsageLimit{ID: id, Label: label, Scope: scope, Window: window, Amount: amount, Status: status})
		}
	}
	if hasGroupedBuckets {
		for _, rawGroup := range groups {
			group := summaryGroup{present: true, buckets: arrayValues(rawGroup.Get("buckets"))}
			if name, ok := scalarString(rawGroup.Get("displayName")); ok {
				group.displayName = &name
			}
			for _, rawBucket := range group.buckets {
				addBucket(rawBucket, group)
			}
		}
	} else {
		for _, rawBucket := range topLevel {
			addBucket(rawBucket, summaryGroup{})
		}
	}

	sort.SliceStable(report.Limits, func(i, j int) bool {
		return fractionOrOne(report.Limits[i].Amount.RemainingFraction) < fractionOrOne(report.Limits[j].Amount.RemainingFraction)
	})
	report.Metadata = UsageMetadata{Endpoint: endpoint, ProjectID: credential.ProjectID, Email: credential.Email, AccountID: credential.AccountID}
	return report, nil
}

func readSummaryBucket(raw gjson.Result) quotaSummaryBucket {
	bucket := quotaSummaryBucket{raw: raw}
	if id, ok := scalarString(raw.Get("bucketId")); ok {
		bucket.bucketID = &id
	}
	if name, ok := scalarString(raw.Get("displayName")); ok {
		bucket.displayName = &name
	}
	if window, ok := scalarString(raw.Get("window")); ok {
		bucket.window = &window
	}
	if fraction := raw.Get("remainingFraction"); fraction.Type == gjson.Number {
		value := fraction.Num
		bucket.remaining = &value
	}
	bucket.remainingAmount = raw.Get("remainingAmount")
	if reset := raw.Get("resetTime"); reset.Type == gjson.String {
		bucket.resetTime = &reset.Str
	}
	bucket.disabled = raw.Get("disabled").Type == gjson.True
	return bucket
}

// buildQuotaSummaryAmount is buildQuotaSummaryAmount.
func buildQuotaSummaryAmount(bucket quotaSummaryBucket) UsageAmount {
	if remaining := clampFraction(bucket.remaining); remaining != nil {
		return percentAmount(*remaining)
	}
	amount := UsageAmount{Unit: "unknown"}
	switch bucket.remainingAmount.Type {
	case gjson.Number:
		value := bucket.remainingAmount.Num
		amount.Remaining = &value
	case gjson.String:
		if parsed, ok := jsNumber(bucket.remainingAmount.Str); ok {
			amount.Remaining = &parsed
		}
	}
	return amount
}

// quotaSummaryCounterKeys is getQuotaSummaryCounterKeys.
func quotaSummaryCounterKeys(group summaryGroup, bucket quotaSummaryBucket) []string {
	bucketID := ""
	if bucket.bucketID != nil {
		bucketID = strings.ToLower(*bucket.bucketID)
	}
	groupName := ""
	if group.displayName != nil {
		groupName = strings.ToLower(*group.displayName)
	}
	if strings.HasPrefix(bucketID, "gemini-") || strings.Contains(groupName, "gemini") {
		return []string{"google"}
	}
	if strings.HasPrefix(bucketID, "3p-") || strings.Contains(groupName, "claude") ||
		strings.Contains(groupName, "gpt") || strings.Contains(groupName, "third party") {
		// Antigravity exposes one shared third-party group; duplicating its
		// limits into the model-family scopes lets Claude and GPT requests both
		// rank it without inventing a second quota.
		return []string{"anthropic", "openai"}
	}
	return []string{"default"}
}

// quotaSummaryCounterName is getQuotaSummaryCounterName.
func quotaSummaryCounterName(counterKey string) string {
	switch counterKey {
	case "google":
		return "Google"
	case "anthropic":
		return "Anthropic"
	case "openai":
		return "OpenAI"
	}
	return ""
}

// windowDescriptor is AntigravityWindowDescriptor.
type windowDescriptor struct {
	id         string
	label      string
	durationMs *float64
}

func floatPtr(value float64) *float64 { return &value }

var (
	regexpSevenDay = regexp.MustCompile(`7[\s_-]*day`)
	regexpFiveHour = regexp.MustCompile(`5[\s_-]*hour`)
)

// classifyWindow is classifyWindow.
func classifyWindow(id, label *string) *windowDescriptor {
	idText, labelText := "", ""
	if id != nil {
		idText = *id
	}
	if label != nil {
		labelText = *label
	}
	source := strings.ToLower(idText + " " + labelText)
	if strings.Contains(source, "week") || strings.Contains(source, "7d") || regexpSevenDay.MatchString(source) {
		return &windowDescriptor{id: "weekly", label: "Weekly", durationMs: floatPtr(weekMs)}
	}
	if strings.Contains(source, "5h") || strings.Contains(source, "five hour") || regexpFiveHour.MatchString(source) {
		return &windowDescriptor{id: "5h", label: "5 Hour", durationMs: floatPtr(5 * hourMs)}
	}
	if strings.Contains(source, "day") || strings.Contains(source, "daily") || strings.Contains(source, "24h") {
		return &windowDescriptor{id: "daily", label: "Daily", durationMs: floatPtr(dayMs)}
	}
	if id != nil || label != nil {
		d := windowDescriptor{}
		if id != nil {
			d.id = *id
		} else {
			d.id = labelText
		}
		if label != nil {
			d.label = *label
		} else {
			d.label = idText
		}
		return &d
	}
	return nil
}

// inferWindowFromReset is inferWindowFromReset.
func inferWindowFromReset(resetAt *float64, nowMs float64) windowDescriptor {
	if resetAt != nil && *resetAt-nowMs > dayMs {
		return windowDescriptor{id: "weekly", label: "Weekly", durationMs: floatPtr(weekMs)}
	}
	return windowDescriptor{id: "daily", label: "Daily", durationMs: floatPtr(dayMs)}
}

// quotaWindowInfo is the subset of a quota entry parseWindow reads.
type quotaWindowInfo struct {
	resetTime   *string
	windowID    *string
	windowLabel *string
}

// parseWindow is parseWindow.
func parseWindow(info quotaWindowInfo, descriptor *windowDescriptor) *UsageWindow {
	var resetAt *float64
	if info.resetTime != nil {
		if parsed, ok := parseISOTimestamp(*info.resetTime); ok {
			resetAt = &parsed
		}
	}
	if descriptor == nil && resetAt == nil {
		return nil
	}
	window := &UsageWindow{}
	switch {
	case descriptor != nil:
		window.ID = descriptor.id
	case info.windowID != nil:
		window.ID = *info.windowID
	default:
		window.ID = "default"
	}
	switch {
	case info.windowLabel != nil:
		window.Label = *info.windowLabel
	case descriptor != nil:
		window.Label = descriptor.label
	default:
		window.Label = "Default"
	}
	if descriptor != nil {
		window.DurationMs = descriptor.durationMs
	}
	window.ResetsAt = resetAt
	return window
}

// percentAmount is buildPercentAmount.
func percentAmount(remainingFraction float64) UsageAmount {
	usedFraction := 1 - remainingFraction
	remaining := remainingFraction * 100
	used := usedFraction * 100
	limit := 100.0
	return UsageAmount{
		Unit:              "percent",
		RemainingFraction: &remainingFraction,
		UsedFraction:      &usedFraction,
		Remaining:         &remaining,
		Used:              &used,
		Limit:             &limit,
	}
}

// remainingStatus is getUsageStatus: the thresholds NeoPi reads a remaining
// fraction with (the report's status field).
func remainingStatus(remainingFraction float64) string {
	if remainingFraction <= 0 {
		return usageStatusSpent
	}
	if remainingFraction <= 0.1 {
		return usageStatusWarn
	}
	return usageStatusOK
}

// usageStatus is shared usageStatus: a USED fraction mapped to the status.
func usageStatus(usedFraction *float64) string {
	if usedFraction == nil {
		return usageStatusNaN
	}
	if *usedFraction >= 1 {
		return usageStatusSpent
	}
	if *usedFraction >= 0.9 {
		return usageStatusWarn
	}
	return usageStatusOK
}

// remainingStatusOrUnknown is getUsageStatus taking a pointer, as the legacy
// model parser calls it.
func remainingStatusOrUnknown(remainingFraction *float64) string {
	if remainingFraction == nil {
		return usageStatusNaN
	}
	return remainingStatus(*remainingFraction)
}

func clampFraction(value *float64) *float64 {
	if value == nil || math.IsInf(*value, 0) || math.IsNaN(*value) {
		return nil
	}
	clamped := math.Min(1, math.Max(0, *value))
	return &clamped
}

func fractionOrOne(value *float64) float64 {
	if value == nil {
		return 1
	}
	return *value
}

func windowOrFallback(window *UsageWindow, windowID *string, bucketID *string, fallback string) string {
	if window != nil {
		return window.ID
	}
	if windowID != nil {
		return *windowID
	}
	if bucketID != nil {
		return *bucketID
	}
	return fallback
}

func arrayValues(value gjson.Result) []gjson.Result {
	if !value.IsArray() {
		return nil
	}
	return value.Array()
}

func stringPtrOrNil(value gjson.Result) *string {
	if text, ok := scalarString(value); ok {
		return &text
	}
	return nil
}

// modelQuota is one normalized legacy quotaInfo entry.
type modelQuota struct {
	remaining   *float64
	resetTime   *string
	tier        *string
	windowID    *string
	windowLabel *string
	apiProvider *string
	modelProv   *string
}

func (q modelQuota) withDescriptor(descriptor *windowDescriptor) modelQuota {
	if descriptor == nil {
		return q
	}
	if q.windowID == nil {
		id := descriptor.id
		q.windowID = &id
	}
	if q.windowLabel == nil {
		label := descriptor.label
		q.windowLabel = &label
	}
	return q
}

// readModelQuota reads one quotaInfo entry; a non-object yields an empty entry.
func readModelQuota(raw gjson.Result) modelQuota {
	quota := modelQuota{
		resetTime:   stringPtrOrNil(raw.Get("resetTime")),
		tier:        stringPtrOrNil(raw.Get("tier")),
		windowID:    stringPtrOrNil(raw.Get("windowId")),
		windowLabel: stringPtrOrNil(raw.Get("windowLabel")),
		apiProvider: stringPtrOrNil(raw.Get("apiProvider")),
		modelProv:   stringPtrOrNil(raw.Get("modelProvider")),
	}
	if fraction := raw.Get("remainingFraction"); fraction.Type == gjson.Number {
		value := fraction.Num
		quota.remaining = &value
	}
	return quota
}

// normalizeQuotaInfos is normalizeQuotaInfos: every quota list a model entry
// carries, flattened.
func normalizeQuotaInfos(info gjson.Result) []modelQuota {
	apiProvider := stringPtrOrNil(info.Get("apiProvider"))
	modelProvider := stringPtrOrNil(info.Get("modelProvider"))
	var out []modelQuota
	addValue := func(value gjson.Result, tier *string, descriptor *windowDescriptor) {
		// addValue returns early for every falsy value: null, false, 0 and "".
		if !value.Exists() || value.Type == gjson.Null || value.Type == gjson.False ||
			(value.Type == gjson.Number && value.Num == 0) || (value.Type == gjson.String && value.Str == "") {
			return
		}
		read := func(raw gjson.Result) {
			quota := readModelQuota(raw)
			if quota.apiProvider == nil && apiProvider != nil {
				quota.apiProvider = apiProvider
			}
			if quota.modelProv == nil && modelProvider != nil {
				quota.modelProv = modelProvider
			}
			quota = quota.withDescriptor(descriptor)
			if tier != nil && *tier != "" {
				quota.tier = tier
			}
			out = append(out, quota)
		}
		if value.IsArray() {
			for _, entry := range value.Array() {
				read(entry)
			}
			return
		}
		read(value)
	}
	addValue(info.Get("quotaInfo"), nil, nil)
	addValue(info.Get("quotaInfos"), nil, nil)
	addValue(info.Get("dailyQuotaInfo"), nil, classifyWindow(stringPtr("daily"), stringPtr("Daily")))
	addValue(info.Get("dailyQuotaInfos"), nil, classifyWindow(stringPtr("daily"), stringPtr("Daily")))
	addValue(info.Get("weeklyQuotaInfo"), nil, classifyWindow(stringPtr("weekly"), stringPtr("Weekly")))
	addValue(info.Get("weeklyQuotaInfos"), nil, classifyWindow(stringPtr("weekly"), stringPtr("Weekly")))
	for _, entry := range jsEntries(info.Get("quotaInfoByTier")) {
		tier := entry.key
		addValue(entry.value, &tier, nil)
	}
	windowMap := func(name string) {
		for _, entry := range jsEntries(info.Get(name)) {
			addValue(entry.value, nil, classifyWindow(&entry.key, nil))
		}
	}
	windowMap("quotaInfoByWindow")
	windowMap("quotaInfosByWindow")
	return out
}

func stringPtr(value string) *string { return &value }

// inferWindowDescriptors is inferWindowDescriptors: unlabelled entries are
// inferred per backend counter, the latest reset of a group becoming weekly.
func inferWindowDescriptors(quotas []modelQuota, nowMs float64) []*windowDescriptor {
	descriptors := make([]*windowDescriptor, len(quotas))
	type groupKey struct{ model, api, tier string }
	groups := map[groupKey][]int{}
	var order []groupKey
	for index, quota := range quotas {
		if descriptor := classifyWindow(quota.windowID, quota.windowLabel); descriptor != nil {
			descriptors[index] = descriptor
			continue
		}
		key := groupKey{model: deref(quota.modelProv), api: deref(quota.apiProvider), tier: deref(quota.tier)}
		if _, seen := groups[key]; !seen {
			order = append(order, key)
		}
		groups[key] = append(groups[key], index)
	}
	for _, key := range order {
		members := groups[key]
		seen := map[float64]bool{}
		var resetTimes []float64
		for _, index := range members {
			if quotas[index].resetTime == nil {
				continue
			}
			if parsed, ok := parseISOTimestamp(*quotas[index].resetTime); ok && !seen[parsed] {
				seen[parsed] = true
				resetTimes = append(resetTimes, parsed)
			}
		}
		sort.Float64s(resetTimes)
		var latest *float64
		if len(resetTimes) > 1 {
			latest = &resetTimes[len(resetTimes)-1]
		}
		for _, index := range members {
			var entryReset *float64
			if quotas[index].resetTime != nil {
				if parsed, ok := parseISOTimestamp(*quotas[index].resetTime); ok {
					entryReset = &parsed
				}
			}
			if latest != nil && entryReset != nil && *entryReset == *latest {
				descriptors[index] = &windowDescriptor{id: "weekly", label: "Weekly", durationMs: floatPtr(weekMs)}
				continue
			}
			inferred := inferWindowFromReset(entryReset, nowMs)
			descriptors[index] = &inferred
		}
	}
	return descriptors
}

func deref(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

// buildAmount is buildAmount: a clamped fraction, or "blocked until reset"
// when only a reset time came back.
func buildAmount(quota modelQuota) UsageAmount {
	if fraction := clampFraction(quota.remaining); fraction != nil {
		return percentAmount(*fraction)
	}
	if quota.resetTime != nil && *quota.resetTime != "" {
		zero := 0.0
		return percentAmount(zero)
	}
	return UsageAmount{Unit: "percent"}
}

// counterName is formatCounterName.
func counterName(quota modelQuota) string {
	provider := deref(quota.modelProv)
	if provider == "" {
		provider = deref(quota.apiProvider)
	}
	switch provider {
	case "MODEL_PROVIDER_ANTHROPIC", "API_PROVIDER_ANTHROPIC_VERTEX":
		return "Anthropic"
	case "MODEL_PROVIDER_GOOGLE", "API_PROVIDER_GOOGLE_GEMINI":
		return "Google"
	case "MODEL_PROVIDER_OPENAI", "API_PROVIDER_OPENAI_VERTEX":
		return "OpenAI"
	}
	return ""
}

// dedupedQuota is one backend counter/window/tier bucket after merging.
type dedupedQuota struct {
	amount      UsageAmount
	window      *UsageWindow
	tier        *string
	tierKey     string
	windowID    string
	counterName string
	counterKey  string
}

// buildModelQuotaReport is the fetchAvailableModels fallback: per-model quota
// entries merged per backend counter, tier and window.
func buildModelQuotaReport(payload gjson.Result, credential UsageCredential, endpoint string, nowMs float64) (*Usage, error) {
	deduped := map[string]*dedupedQuota{}
	var order []string
	for _, model := range jsEntries(payload.Get("models")) {
		quotas := normalizeQuotaInfos(model.value)
		descriptors := inferWindowDescriptors(quotas, nowMs)
		for index, quota := range quotas {
			amount := buildAmount(quota)
			window := parseWindow(quotaWindowInfo{resetTime: quota.resetTime, windowID: quota.windowID, windowLabel: quota.windowLabel}, descriptors[index])
			tierKey := "default"
			if quota.tier != nil && *quota.tier != "" {
				tierKey = strings.ToLower(*quota.tier)
			}
			name := counterName(quota)
			counterKey := "default"
			if name != "" {
				counterKey = strings.ToLower(name)
			}
			windowID := "default"
			if window != nil {
				windowID = window.ID
			} else if quota.windowID != nil {
				windowID = *quota.windowID
			}
			key := counterKey + "|" + tierKey + "|" + windowID
			existing, seen := deduped[key]
			if !seen {
				deduped[key] = &dedupedQuota{amount: amount, window: window, tier: quota.tier, tierKey: tierKey,
					windowID: windowID, counterName: name, counterKey: counterKey}
				order = append(order, key)
				continue
			}
			// Keep the entry with fraction data for the bar, and any window with
			// a reset time so "resets in…" survives the merge.
			existingFraction, currentFraction := existing.amount.RemainingFraction, amount.RemainingFraction
			bestAmount := existing.amount
			bestTier := existing.tier
			if bestTier == nil {
				bestTier = quota.tier
			}
			bestWindow := existing.window
			if bestWindow == nil || bestWindow.ResetsAt == nil || *bestWindow.ResetsAt == 0 {
				if window != nil {
					bestWindow = window
				}
			}
			switch {
			case existingFraction == nil && currentFraction != nil:
				bestAmount = amount
				if quota.tier != nil {
					bestTier = quota.tier
				} else {
					bestTier = existing.tier
				}
			case existingFraction != nil && currentFraction != nil && *currentFraction < *existingFraction:
				bestAmount = amount
				if quota.tier != nil {
					bestTier = quota.tier
				} else {
					bestTier = existing.tier
				}
			}
			if windowHasReset(window) && !windowHasReset(bestWindow) {
				bestWindow = window
			}
			existing.amount, existing.window, existing.tier = bestAmount, bestWindow, bestTier
		}
	}

	// Autocomplete models report a bare { remainingFraction: 1 } with no reset
	// time; a counter with any windowed entry keeps only its windowed entries.
	metered := map[string]bool{}
	for _, key := range order {
		entry := deduped[key]
		if windowHasReset(entry.window) {
			metered[entry.counterKey+"|"+entry.tierKey] = true
		}
	}
	report := &Usage{Provider: credential.Provider, FetchedAt: nowMs, Limits: []UsageLimit{}}
	for _, key := range order {
		entry := deduped[key]
		if !windowHasReset(entry.window) && metered[entry.counterKey+"|"+entry.tierKey] {
			continue
		}
		label := "Usage"
		if entry.counterName != "" {
			label = "Usage (" + entry.counterName + ")"
		}
		scope := UsageScope{Provider: credential.Provider, AccountID: credential.AccountID,
			ProjectID: credential.ProjectID, WindowID: entry.windowID}
		if entry.tier != nil {
			scope.Tier = *entry.tier
		}
		report.Limits = append(report.Limits, UsageLimit{
			ID:     credential.Provider + ":" + entry.counterKey + ":" + entry.tierKey + ":" + entry.windowID,
			Label:  label,
			Scope:  scope,
			Window: entry.window,
			Amount: entry.amount,
			Status: remainingStatusOrUnknown(entry.amount.RemainingFraction),
		})
	}
	sort.SliceStable(report.Limits, func(i, j int) bool {
		return fractionOrOne(report.Limits[i].Amount.RemainingFraction) < fractionOrOne(report.Limits[j].Amount.RemainingFraction)
	})
	report.Metadata = UsageMetadata{Endpoint: endpoint, ProjectID: credential.ProjectID, Email: credential.Email, AccountID: credential.AccountID}
	return report, nil
}

func windowHasReset(window *UsageWindow) bool {
	return window != nil && window.ResetsAt != nil && *window.ResetsAt != 0
}

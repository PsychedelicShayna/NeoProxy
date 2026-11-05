package antigravity

import (
	"fmt"
	"math"
	"time"

	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

// ApplyUsage records a usage report on a: the account identity NeoPi's report
// carries, and the per-counter quota watermarks the conductor's antigravity
// cooldown restoration reads ("55%" percents, RFC 3339 reset instants).
func ApplyUsage(a *coreauth.Auth, usage *Usage, now time.Time) {
	if a == nil || usage == nil {
		return
	}
	if a.Metadata == nil {
		a.Metadata = map[string]any{}
	}
	if a.Attributes == nil {
		a.Attributes = map[string]string{}
	}
	if a.Quota.Signals == nil {
		a.Quota.Signals = map[string]string{}
	}
	for key, value := range map[string]string{
		"email":      usage.Metadata.Email,
		"account_id": usage.Metadata.AccountID,
		"project_id": usage.Metadata.ProjectID,
	} {
		if value == "" {
			continue
		}
		a.Metadata[key] = value
		a.Attributes[key] = value
	}
	if a.Quota.Signals == nil {
		a.Quota.Signals = map[string]string{}
	}
	for _, entry := range HealableBlockScopes(usage) {
		counterKey := entry.BlockScope
		if _, found := cutPrefix(counterKey, "counter:"); found {
			counterKey = counterKey[len("counter:"):]
		}
		if counterKey == "unknown" || len(entry.Limits) == 0 {
			continue
		}
		worstRemaining, worstUsed := math.Inf(1), math.Inf(-1)
		var resetsAt time.Time
		for _, limit := range entry.Limits {
			if limit.Amount.RemainingFraction != nil {
				worstRemaining = math.Min(worstRemaining, *limit.Amount.RemainingFraction)
			}
			if limit.Amount.UsedFraction != nil {
				worstUsed = math.Max(worstUsed, *limit.Amount.UsedFraction)
			}
			if limit.Window != nil && limit.Window.ResetsAt != nil && *limit.Window.ResetsAt > 0 {
				reset := time.UnixMilli(int64(*limit.Window.ResetsAt)).UTC()
				if resetsAt.IsZero() || reset.Before(resetsAt) {
					resetsAt = reset
				}
			}
		}
		if !math.IsInf(worstRemaining, 1) {
			a.Quota.Signals[counterKey+"_remaining_percent"] = fmt.Sprintf("%d%%", int(math.Round(worstRemaining*100)))
		}
		if !math.IsInf(worstUsed, -1) {
			a.Quota.Signals[counterKey+"_used_percent"] = fmt.Sprintf("%d%%", int(math.Round(worstUsed*100)))
		}
		if !resetsAt.IsZero() {
			a.Quota.Signals[counterKey+"_resets_at"] = resetsAt.Format(time.RFC3339)
		}
	}
	if usage.Metadata.ProjectID != "" {
		a.Quota.Signals["project_id"] = usage.Metadata.ProjectID
	}
	a.Quota.ObservedAt = now
}

func cutPrefix(text, prefix string) (string, bool) {
	if len(text) >= len(prefix) && text[:len(prefix)] == prefix {
		return text[len(prefix):], true
	}
	return text, false
}

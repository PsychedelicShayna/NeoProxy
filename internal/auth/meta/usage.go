package meta

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const (
	weekMs = int64(7 * 24 * 60 * 60 * 1000)
	// DefaultRollingMinutes is Meta's documented rolling window, used when no
	// earlier report names one.
	DefaultRollingMinutes = 300
	weeklyLimitID         = "1w"
)

// rollingLimitID matches the ids rollingWindow produces: `<minutes>m`, or
// `rolling` when Meta names no duration.
var rollingLimitID = regexp.MustCompile(`^(?:rolling|\d+m)$`)

// UsageLimit is one subscription limit of NeoPi's Muse Code usage report
// (usage/muse-code.ts): a rolling window or the weekly cap, as a percentage.
type UsageLimit struct {
	ID          string
	Label       string
	UsedPercent float64
	DurationMs  int64
	// ResetsAtMs is the reset instant in epoch milliseconds, 0 when unknown.
	ResetsAtMs int64
	Notes      []string
}

// Usage is the Muse Code subscription report: the limits the key endpoint
// names (or infers while the rolling window is idle) for one account.
type Usage struct {
	Limits    []UsageLimit
	AccountID string
	Email     string
	Tier      string
}

// UsageError is a failure NeoPi surfaces as a ProviderHttpError: the account's
// token was refused, or its subscription is inactive.
type UsageError struct {
	Status  int
	Message string
}

func (e *UsageError) Error() string { return e.Message }

// FetchUsage is museCodeUsageProvider.fetchUsage: it reads the subscription's
// usage from the key endpoint with the device token (no onboarding). previous
// is the last report for the account, which an idle response carries forward.
// fallbackEmail and fallbackAccountID are the credential's own identity, used
// when the response names none. A nil report with a nil error is NeoPi's null:
// nothing to report.
func (a *MetaAuth) FetchUsage(ctx context.Context, dcaToken string, previous *Usage, fallbackEmail, fallbackAccountID string, now time.Time) (*Usage, error) {
	payload, err := a.RequestKey(ctx, dcaToken, false)
	if err != nil {
		var keyErr *KeyError
		if asKeyError(err, &keyErr) && (keyErr.Status == http.StatusUnauthorized || keyErr.Status == http.StatusForbidden) {
			return nil, &UsageError{Status: keyErr.Status, Message: keyErr.Message}
		}
		return nil, nil
	}
	if payload.SubsActiveKnown && !payload.IsSubsActive {
		return nil, &UsageError{Status: http.StatusForbidden, Message: "Muse Code subscription is inactive"}
	}
	tier := payload.Tier()
	email := payload.NormalizedEmail()
	if email == "" {
		email = fallbackEmail
	}
	accountID := strings.TrimSpace(payload.UserID)
	if accountID == "" {
		accountID = fallbackAccountID
	}
	if accountID == "" {
		accountID = email
	}
	// Payment-required responses omit `subs_usage` too; only an explicitly
	// active subscription with no payment demand or action means an idle window.
	idle := payload.SubsActiveKnown && payload.IsSubsActive && !payload.RequirePayment &&
		strings.TrimSpace(payload.RequirePaymentActionURL) == "" && strings.TrimSpace(payload.ActionURL) == ""
	var limits []UsageLimit
	switch {
	case payload.SubsUsage != nil:
		limits = buildLimits(payload.SubsUsage)
	case idle:
		limits = buildIdleLimits(previous, now)
	}
	if len(limits) == 0 {
		return nil, nil
	}
	return &Usage{Limits: limits, AccountID: accountID, Email: email, Tier: tier}, nil
}

func asKeyError(err error, target **KeyError) bool {
	keyErr, ok := err.(*KeyError)
	if ok {
		*target = keyErr
	}
	return ok
}

func formatRollingWindowLabel(minutes float64, valid bool) string {
	if !valid {
		return "Rolling Window"
	}
	rounded := int64(math.Round(minutes))
	if rounded%60 == 0 {
		hours := rounded / 60
		if hours == 1 {
			return "1 Hour"
		}
		return fmt.Sprintf("%d Hours", hours)
	}
	if rounded == 1 {
		return "1 Minute"
	}
	return fmt.Sprintf("%d Minutes", rounded)
}

type windowShape struct {
	id         string
	label      string
	durationMs int64
}

func rollingWindow(minutes *float64) windowShape {
	if minutes == nil || math.IsNaN(*minutes) || math.IsInf(*minutes, 0) || *minutes <= 0 {
		return windowShape{id: "rolling", label: formatRollingWindowLabel(0, false)}
	}
	durationMs := int64(math.Round(*minutes * 60_000))
	return windowShape{
		id:         strconv.FormatInt(int64(math.Round(float64(durationMs)/60_000)), 10) + "m",
		label:      formatRollingWindowLabel(*minutes, true),
		durationMs: durationMs,
	}
}

// parseResetTimestamp reads resets_at: an ISO string, or an epoch in seconds or
// milliseconds.
func parseResetTimestamp(raw json.RawMessage) int64 {
	text := strings.TrimSpace(string(raw))
	if text == "" || text == "null" {
		return 0
	}
	if strings.HasPrefix(text, `"`) {
		var value string
		if json.Unmarshal(raw, &value) != nil {
			return 0
		}
		for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02T15:04:05", "2006-01-02"} {
			if parsed, err := time.Parse(layout, value); err == nil {
				return parsed.UnixMilli()
			}
		}
		return 0
	}
	var number float64
	if json.Unmarshal(raw, &number) != nil || number <= 0 || math.IsNaN(number) || math.IsInf(number, 0) {
		return 0
	}
	if number < 1_000_000_000_000 {
		number *= 1000
	}
	return int64(number)
}

func buildLimit(id, label string, usedPercent *float64, durationMs, resetsAtMs int64) (UsageLimit, bool) {
	if usedPercent == nil || math.IsNaN(*usedPercent) || math.IsInf(*usedPercent, 0) || *usedPercent < 0 {
		return UsageLimit{}, false
	}
	return UsageLimit{ID: id, Label: label, UsedPercent: math.Min(*usedPercent, 100), DurationMs: durationMs, ResetsAtMs: resetsAtMs}, true
}

func buildLimits(usage *SubscriptionUsage) []UsageLimit {
	var limits []UsageLimit
	if usage.Window != nil {
		window := rollingWindow(usage.Window.WindowDurationMins)
		if limit, ok := buildLimit(window.id, window.label, usage.Window.UsedPercent, window.durationMs, parseResetTimestamp(usage.Window.ResetsAt)); ok {
			limits = append(limits, limit)
		}
	}
	if usage.Weekly != nil {
		if limit, ok := buildLimit(weeklyLimitID, "Weekly", usage.Weekly.UsedPercent, weekMs, parseResetTimestamp(usage.Weekly.ResetsAt)); ok {
			limits = append(limits, limit)
		}
	}
	return limits
}

// buildIdleLimits is the report for a key response that omits `subs_usage`,
// which Meta does while the rolling window is idle: no request has opened one,
// so it is unused. Weekly usage only accrues inside a rolling window, so the
// previous report's weekly figure holds until its reset and is zero after it;
// a passed reset rolls forward by whole weeks. Without a previous weekly figure
// and its reset the weekly limit stays unreported.
func buildIdleLimits(previous *Usage, now time.Time) []UsageLimit {
	var previousRolling, previousWeekly *UsageLimit
	if previous != nil {
		for i := range previous.Limits {
			switch limit := &previous.Limits[i]; {
			case previousRolling == nil && rollingLimitID.MatchString(limit.ID):
				previousRolling = limit
			case limit.ID == weeklyLimitID:
				previousWeekly = limit
			}
		}
	}
	defaultMinutes := float64(DefaultRollingMinutes)
	shape := rollingWindow(&defaultMinutes)
	if previousRolling != nil {
		shape = windowShape{id: previousRolling.ID, label: previousRolling.Label, durationMs: previousRolling.DurationMs}
	}
	limits := []UsageLimit{{ID: shape.id, Label: shape.label, DurationMs: shape.durationMs, Notes: []string{"Idle: no request has opened this window"}}}
	if previousWeekly == nil || previousWeekly.ResetsAtMs == 0 {
		return limits
	}
	resetsAt := previousWeekly.ResetsAtMs
	used := previousWeekly.UsedPercent
	note := "Carried from the last reported usage; later usage not visible"
	if nowMs := now.UnixMilli(); resetsAt <= nowMs {
		resetsAt += ((nowMs-resetsAt)/weekMs + 1) * weekMs
		used = 0
		note = "Reset since the last reported usage"
	}
	return append(limits, UsageLimit{ID: weeklyLimitID, Label: "Weekly", UsedPercent: used, DurationMs: weekMs, ResetsAtMs: resetsAt, Notes: []string{note}})
}

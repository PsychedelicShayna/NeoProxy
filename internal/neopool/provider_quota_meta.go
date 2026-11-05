package neopool

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"

	metaauth "github.com/router-for-me/CLIProxyAPI/v8/internal/auth/meta"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

// errNoMetaUsage is a key response that reports no limits.
var errNoMetaUsage = errors.New("muse-code usage: the key response reports no limits")

// metaUsageMemory keeps the last Muse Code report of each account: an idle
// response carries its weekly figure forward (usage/muse-code.ts previousReport).
var metaUsageMemory sync.Map // auth ID -> *metaauth.Usage

// metaDeviceToken is the Meta account token a Muse sign-in keeps beside its
// minted key; the usage endpoint reads the subscription with it.
func metaDeviceToken(a *coreauth.Auth) string {
	if a == nil {
		return ""
	}
	if token, _ := a.Metadata["dca_token"].(string); strings.TrimSpace(token) != "" {
		return strings.TrimSpace(token)
	}
	return strings.TrimSpace(a.Attributes["dca_token"])
}

// metaUsageWindows adapts NeoPi's Muse Code report: its rolling window becomes
// the 5h or day window by length, the weekly cap the week window.
func metaUsageWindows(usage *metaauth.Usage) []Window {
	out := []Window{}
	if usage == nil {
		return out
	}
	for _, limit := range usage.Limits {
		kind := "week"
		if limit.ID != "1w" {
			switch {
			case limit.DurationMs > 36*int64(time.Hour/time.Millisecond):
				kind = "week"
			case limit.DurationMs > 6*int64(time.Hour/time.Millisecond):
				kind = "day"
			default:
				kind = "5h"
			}
		}
		resets, starts := time.Time{}, time.Time{}
		if limit.ResetsAtMs > 0 {
			resets = time.UnixMilli(limit.ResetsAtMs)
			if limit.DurationMs > 0 {
				starts = resets.Add(-time.Duration(limit.DurationMs) * time.Millisecond)
			}
		}
		out = append(out, percentWindow(kind, limit.UsedPercent, starts, resets))
	}
	return out
}

// fetchMetaUsage reads a Muse sign-in's windows. A response with nothing to
// report (NeoPi's null) leaves the cached reading in place by failing the poll.
func fetchMetaUsage(ctx context.Context, client *http.Client, a *coreauth.Auth, now time.Time) ([]Window, error) {
	var previous *metaauth.Usage
	if value, ok := metaUsageMemory.Load(a.ID); ok {
		previous, _ = value.(*metaauth.Usage)
	}
	email, _ := a.Metadata["email"].(string)
	accountID, _ := a.Metadata["account_id"].(string)
	usage, err := metaauth.NewMetaAuthWithClient(client).FetchUsage(ctx, metaDeviceToken(a), previous, strings.TrimSpace(email), strings.TrimSpace(accountID), now)
	if err != nil {
		return nil, err
	}
	if usage == nil {
		return nil, errNoMetaUsage
	}
	metaUsageMemory.Store(a.ID, usage)
	return metaUsageWindows(usage), nil
}

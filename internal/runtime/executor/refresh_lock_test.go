package executor

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func lockedTestAuth(provider string, expires time.Time) *cliproxyauth.Auth {
	return &cliproxyauth.Auth{ID: provider + "-locked", Provider: provider, Metadata: map[string]any{
		"type":                             provider,
		"access_token":                     "imported-access",
		"refresh_token":                    "must-never-be-used",
		"expired":                          expires.UTC().Format(time.RFC3339),
		"project_id":                       "proj",
		cliproxyauth.MetadataRefreshLocked: true,
	}}
}

// Every executor refresh entry, including inline token renewal, refuses a
// refresh-locked credential before doing anything else, even with a refresh
// token present and home refresh enabled.
func TestExecutorsRefuseRefreshLockedCredentials(t *testing.T) {
	expired := time.Now().Add(-time.Minute)
	for _, cfg := range []*config.Config{{}, {Home: config.HomeConfig{Enabled: true}}} {
		refreshers := map[string]func(*cliproxyauth.Auth) error{
			"codex": func(a *cliproxyauth.Auth) error {
				_, err := NewCodexExecutor(cfg).Refresh(context.Background(), a)
				return err
			},
			"claude": func(a *cliproxyauth.Auth) error {
				_, err := NewClaudeExecutor(cfg).Refresh(context.Background(), a)
				return err
			},
			"antigravity": func(a *cliproxyauth.Auth) error {
				_, err := NewAntigravityExecutor(cfg).Refresh(context.Background(), a)
				return err
			},
			"xai": func(a *cliproxyauth.Auth) error {
				_, err := NewXAIExecutor(cfg).Refresh(context.Background(), a)
				return err
			},
		}
		for provider, refresh := range refreshers {
			if err := refresh(lockedTestAuth(provider, expired)); !errors.Is(err, cliproxyauth.ErrRefreshLocked) {
				t.Fatalf("%s (home=%v): err = %v, want ErrRefreshLocked", provider, cfg.Home.Enabled, err)
			}
		}
	}
}

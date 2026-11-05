package management

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	devinauth "github.com/router-for-me/CLIProxyAPI/v8/internal/auth/devin"
	openrouterauth "github.com/router-for-me/CLIProxyAPI/v8/internal/auth/openrouter"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/misc"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

// openRouterCompatName is the openai-compatibility entry OpenRouter keys live
// in; the dashboard catalogue maps it (and its -N variants) to "openrouter".
const openRouterCompatName = "openrouter"

type openRouterOAuthService interface {
	AuthorizationURL(challenge string) string
	ExchangeCode(ctx context.Context, code, verifier string) (string, error)
	ValidateKey(ctx context.Context, key string) error
}

var newOpenRouterOAuthService = func(cfg *config.Config) openRouterOAuthService {
	return openrouterauth.NewService(util.SetProxy(&cfg.SDKConfig, &http.Client{}))
}

// RequestOpenRouterToken starts NeoPi's OpenRouter sign-in: a PKCE
// authorization-code flow whose loopback redirect (localhost:54549) is never
// served, so the code always arrives through the pasted callback. OpenRouter
// does not echo state; the session state is NeoProxy's own handle.
func (h *Handler) RequestOpenRouterToken(c *gin.Context) {
	state, errState := misc.GenerateRandomState()
	if errState != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to generate state parameter"})
		return
	}
	pkce, errPKCE := devinauth.GeneratePKCECodes()
	if errPKCE != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to generate PKCE codes"})
		return
	}
	authSvc := newOpenRouterOAuthService(h.cfg)
	RegisterOAuthSession(state, "openrouter")
	// Do not inherit the HTTP request cancellation: login continues after returning the URL.
	ctx := PopulateAuthContext(context.Background(), c)
	go h.completeOpenRouterOAuth(ctx, h.cfg.AuthDir, state, pkce.CodeVerifier, authSvc)
	c.JSON(http.StatusOK, gin.H{"status": "ok", "url": authSvc.AuthorizationURL(pkce.CodeChallenge), "state": state})
}

// completeOpenRouterOAuth waits for the pasted code (or a pasted sk-or- key,
// NeoPi's paste-key path), settles it into a durable key and adds it to the
// OpenRouter openai-compatibility entry like a pasted key.
func (h *Handler) completeOpenRouterOAuth(ctx context.Context, authDir, state, verifier string, authSvc openRouterOAuthService) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	go watchOAuthSessionCancel(ctx, cancel, state, "openrouter")

	waitFile := filepath.Join(authDir, fmt.Sprintf(".oauth-openrouter-%s.oauth", state))
	defer func() {
		if errRemove := os.Remove(waitFile); errRemove != nil && !errors.Is(errRemove, os.ErrNotExist) {
			log.Warn("failed to remove OpenRouter OAuth callback file")
		}
	}()
	result, errWait := waitOAuthCallbackFile(ctx, waitFile, state, "openrouter")
	if errWait != nil {
		if IsOAuthSessionPending(state, "openrouter") {
			SetOAuthSessionError(state, errWait.Error())
		}
		return
	}
	fail := func(message string) {
		if IsOAuthSessionPending(state, "openrouter") {
			SetOAuthSessionError(state, message)
		}
	}
	switch {
	case !IsOAuthSessionPending(state, "openrouter"):
		return
	case result.State != state:
		fail("State code error")
		return
	case result.Error != "":
		fail("OpenRouter authorization denied")
		return
	case strings.TrimSpace(result.Code) == "":
		fail("Missing authorization code")
		return
	}
	// Upstream errors can carry keys; never expose their text.
	pasted := strings.TrimSpace(result.Code)
	var key string
	if openrouterauth.IsOpenRouterKey(pasted) {
		if errValidate := authSvc.ValidateKey(ctx, pasted); errValidate != nil {
			log.WithError(errValidate).Warn("OpenRouter sign-in: pasted key rejected")
			fail("OpenRouter rejected this key. Check that the whole key was pasted and that it is still active.")
			return
		}
		key = pasted
	} else {
		// Providers may echo code#state; NeoPi exchanges only the code part.
		code, _, _ := strings.Cut(pasted, "#")
		var errExchange error
		key, errExchange = authSvc.ExchangeCode(ctx, code, verifier)
		if errExchange != nil {
			log.WithError(errExchange).Warn("OpenRouter sign-in: code exchange failed")
			fail("Failed to exchange authorization code for a key")
			return
		}
	}
	// The entry serves NeoPi's OpenRouter roster, so no model list is stored.
	if errGuard := guardOAuthSessionPendingForSave(state, "openrouter"); errGuard != nil {
		return
	}
	h.mu.Lock()
	name := compatNameForBase(h.cfg, openRouterCompatName, openrouterauth.BaseURL)
	h.mu.Unlock()
	_, errCreate := h.CreateAccountAPIKey(ctx, coreauth.AccountKey{Provider: "openai-compatibility", APIKey: key, BaseURL: openrouterauth.BaseURL, CompatName: name})
	switch {
	case errors.Is(errCreate, coreauth.ErrAccountKeyConflict):
		fail("This OpenRouter key is already in the pool.")
		return
	case errCreate != nil:
		log.WithError(errCreate).Error("OpenRouter sign-in: saving the key failed")
		fail("Failed to save the OpenRouter key")
		return
	}
	CompleteOAuthSession(state)
	log.Info("OpenRouter authentication successful")
}

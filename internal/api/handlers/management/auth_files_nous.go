package management

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/auth/nous"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

// RequestNousToken starts Nous Portal's device sign-in. The person confirms the
// code on portal.nousresearch.com; the token and its refresh token are saved as
// a nous-portal auth file. Like NeoPi's extension, sign-in does not list models:
// discovery runs when the account is registered.
func (h *Handler) RequestNousToken(c *gin.Context) {
	ctx := PopulateAuthContext(context.Background(), c)
	client := nous.NewClient(h.cfg, "")
	device, err := client.RequestDeviceCode(ctx)
	if err != nil {
		log.Errorf("Nous Portal sign-in could not start: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to generate authorization url"})
		return
	}
	state := fmt.Sprintf("nous-%d", time.Now().UnixNano())
	RegisterOAuthSession(state, nous.Provider)

	go func() {
		pollCtx, cancelPoll := context.WithCancel(ctx)
		defer cancelPoll()
		go watchOAuthSessionCancel(pollCtx, cancelPoll, state, nous.Provider)

		token, errWait := client.WaitForToken(pollCtx, device, nil)
		if errWait != nil {
			if IsOAuthSessionPending(state, nous.Provider) {
				SetOAuthSessionError(state, oauthSessionErrorWithCause("Authentication failed", errWait))
			}
			return
		}
		if !IsOAuthSessionPending(state, nous.Provider) {
			return
		}
		metadata := map[string]any{
			"type":          nous.Provider,
			"access_token":  token.AccessToken,
			"refresh_token": token.RefreshToken,
			"token_type":    "Bearer",
			"scope":         nous.Scope,
			"expired":       token.ExpiresAt.UTC().Format(time.RFC3339),
			"timestamp":     time.Now().UnixMilli(),
		}
		fileName := fmt.Sprintf("%s-%d.json", nous.Provider, time.Now().UnixMilli())
		record := &coreauth.Auth{ID: fileName, Provider: nous.Provider, FileName: fileName, Label: "Nous Portal", Metadata: metadata}
		if errGuard := guardOAuthSessionPendingForSave(state, nous.Provider); errGuard != nil {
			return
		}
		if _, errSave := h.saveTokenRecord(ctx, record); errSave != nil {
			log.Errorf("Nous Portal sign-in: saving the token failed: %v", errSave)
			SetOAuthSessionError(state, "Failed to save authentication tokens")
			return
		}
		CompleteOAuthSession(state)
	}()

	c.JSON(http.StatusOK, gin.H{"status": "ok", "url": device.VerificationURL, "state": state, "flow": "device", "user_code": device.UserCode, "expires_in": device.ExpiresIn})
}

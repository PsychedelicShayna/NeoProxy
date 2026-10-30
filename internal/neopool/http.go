package neopool

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/neo"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

// EngineLogin is the narrow existing-engine adapter needed for owned logins.
// Keeping management/plugin handlers behind this seam avoids coupling routing
// and inventory back to the HTTP execution layer.
type EngineLogin interface {
	StartOAuthV8(*gin.Context)
	GetAuthStatus(*gin.Context)
	PostOAuthCallbackV8(*gin.Context)
	SetAccountOwnershipHook(coreauth.PostAuthHook)
	CreateAccountAPIKey(context.Context, string, string) (*coreauth.Auth, error)
}
type HTTP struct {
	pool            *Store
	management      EngineLogin
	visibleAccounts func(context.Context, neo.Principal) ([]string, error)
}

func NewHTTP(pool *Store, engine EngineLogin) *HTTP {
	h := &HTTP{pool: pool, management: engine}
	if engine != nil {
		engine.SetAccountOwnershipHook(func(ctx context.Context, a *coreauth.Auth) error {
			owner := coreauth.AccountLoginOwner(ctx)
			if owner == "" {
				return nil
			}
			return pool.BindLogin(ctx, owner, a)
		})
	}
	return h
}

// SetAccountVisibility supplies the routing module's authoritative current-grant
// projection. Without it member account inventories are safely empty.
func (h *HTTP) SetAccountVisibility(fn func(context.Context, neo.Principal) ([]string, error)) {
	h.visibleAccounts = fn
}

// Register requires a group protected by neo.HTTP.SessionMiddleware, including
// its same-origin/CSRF checks. Every handler independently requires a session.
func (h *HTTP) Register(group *gin.RouterGroup) {
	group.GET("/vault", h.vault)
	group.PATCH("/vault/providers/:id", h.providerExposure)
	group.GET("/models", h.models)
	group.PATCH("/models/*id", h.modelExposure)
	group.PATCH("/vault/accounts/:id", h.assign)
	group.PUT("/vault/accounts/:id/windows/:kind", h.configureWindow)
	group.GET("/owner/stats", h.stats)
	group.POST("/vault/api-keys", h.apiKey)
	group.POST("/vault/login/callback", h.loginCallback)
	group.GET("/vault/login/status", h.loginStatus)
	group.POST("/vault/login/:provider", h.login)
	group.GET("/vault/sources", h.sourceList)
	group.POST("/vault/sources", h.sourceSave)
	group.PUT("/vault/sources/:id", h.sourceSave)
	group.POST("/vault/sources/:id/sync", h.sourceSync)
	group.DELETE("/vault/sources/:id", h.sourceDelete)
}

func session(c *gin.Context) (neo.Principal, bool) {
	p, ok := neo.PrincipalFromContext(c.Request.Context())
	if !ok || !p.Session {
		poolError(c, http.StatusUnauthorized, "dashboard session required")
		return p, false
	}
	return p, true
}
func ownerSession(c *gin.Context) (neo.Principal, bool) {
	p, ok := session(c)
	if !ok {
		return p, false
	}
	if p.Role != "admin" && p.Role != "owner" {
		poolError(c, http.StatusForbidden, "account owner authority required")
		return p, false
	}
	return p, true
}
func poolError(c *gin.Context, status int, message string) {
	c.AbortWithStatusJSON(status, gin.H{"error": gin.H{"message": message}})
}
func respondError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, ErrForbidden):
		poolError(c, 403, "account operation is not authorized")
	case errors.Is(err, ErrUnavailable):
		poolError(c, 404, "account or meter is unavailable")
	case errors.Is(err, ErrConflict):
		poolError(c, 409, "account operation conflicts with existing state")
	default:
		log.WithError(err).Error("NeoProxy pool operation failed")
		poolError(c, 500, "account operation failed")
	}
}

func (h *HTTP) vault(c *gin.Context) {
	p, ok := session(c)
	if !ok {
		return
	}
	snapshot, err := h.pool.Snapshot(c.Request.Context())
	if err != nil {
		respondError(c, err)
		return
	}
	if p.Role != "admin" {
		ids := []string{}
		if p.Role == "owner" {
			rows, errRows := h.pool.db.QueryContext(c.Request.Context(), `SELECT id FROM neo_accounts WHERE owner_id=?`, p.UserID)
			if errRows != nil {
				respondError(c, errRows)
				return
			}
			for rows.Next() {
				var id string
				if err = rows.Scan(&id); err != nil {
					rows.Close()
					respondError(c, err)
					return
				}
				ids = append(ids, id)
			}
			err = rows.Err()
			rows.Close()
			if err != nil {
				respondError(c, err)
				return
			}
		} else if h.visibleAccounts != nil {
			ids, err = h.visibleAccounts(c.Request.Context(), p)
			if err != nil {
				respondError(c, err)
				return
			}
		}
		accounts := make([]Account, 0, len(ids))
		providers := map[string]bool{}
		for _, a := range snapshot.Accounts {
			if !slices.Contains(ids, a.ID) {
				continue
			}
			if len(p.AccountIDs) > 0 && !slices.Contains(p.AccountIDs, a.ID) {
				continue
			}
			if p.Role != "owner" {
				a.OwnerID = ""
				a.Windows = []Window{}
				a.ProviderWindows = nil
			}
			accounts = append(accounts, a)
			providers[a.ProviderID] = true
		}
		snapshot.Accounts = accounts
		filtered := []Provider{}
		for _, provider := range snapshot.Providers {
			if provider.Exposed && (p.Role != "owner" || providers[provider.ID]) {
				filtered = append(filtered, provider)
			}
		}
		snapshot.Providers = filtered
	}
	c.JSON(200, gin.H{"providers": snapshot.Providers, "accounts": snapshot.Accounts})
}
func (h *HTTP) models(c *gin.Context) {
	p, ok := session(c)
	if !ok {
		return
	}
	snapshot, err := h.pool.Snapshot(c.Request.Context())
	if err != nil {
		respondError(c, err)
		return
	}
	out := snapshot.Models
	if p.Role != "admin" {
		exposed := map[string]bool{}
		for _, provider := range snapshot.Providers {
			exposed[provider.ID] = provider.Exposed
		}
		out = []Model{}
		for _, model := range snapshot.Models {
			if model.Exposed && exposed[model.ProviderID] {
				out = append(out, model)
			}
		}
	}
	c.JSON(200, out)
}
func (h *HTTP) exposure(c *gin.Context, provider bool) {
	p, ok := session(c)
	if !ok {
		return
	}
	if p.Role != "admin" {
		poolError(c, 403, "organization administrator required")
		return
	}
	var body struct {
		Exposed *bool `json:"exposed"`
	}
	if c.ShouldBindJSON(&body) != nil || body.Exposed == nil {
		poolError(c, 400, "exposed boolean is required")
		return
	}
	id := strings.TrimPrefix(c.Param("id"), "/")
	if err := h.pool.SetExposure(c.Request.Context(), id, *body.Exposed, provider); err != nil {
		respondError(c, err)
		return
	}
	c.Status(204)
}
func (h *HTTP) providerExposure(c *gin.Context) { h.exposure(c, true) }
func (h *HTTP) modelExposure(c *gin.Context)    { h.exposure(c, false) }
func (h *HTTP) assign(c *gin.Context) {
	p, ok := session(c)
	if !ok {
		return
	}
	if p.Role != "admin" {
		poolError(c, 403, "organization administrator required")
		return
	}
	var body struct {
		OwnerID string `json:"ownerId"`
	}
	if c.ShouldBindJSON(&body) != nil || body.OwnerID == "" {
		poolError(c, 400, "ownerId is required")
		return
	}
	if err := h.pool.AssignOwner(c.Request.Context(), c.Param("id"), body.OwnerID); err != nil {
		respondError(c, err)
		return
	}
	c.Status(204)
}
func (h *HTTP) configureWindow(c *gin.Context) {
	p, ok := ownerSession(c)
	if !ok {
		return
	}
	var body struct {
		Capacity int64  `json:"capacityTokens"`
		StartsAt *int64 `json:"startsAt"`
	}
	if c.ShouldBindJSON(&body) != nil || body.StartsAt == nil || body.Capacity <= 0 || body.Capacity > 1<<53 || *body.StartsAt < 0 {
		poolError(c, 400, "positive capacityTokens and nonnegative startsAt are required")
		return
	}
	if _, _, err := windowBounds(c.Param("kind"), *body.StartsAt, *body.StartsAt); err != nil {
		poolError(c, 400, "unknown window kind")
		return
	}
	if err := h.pool.ConfigureWindow(c.Request.Context(), c.Param("id"), p.UserID, c.Param("kind"), body.Capacity, *body.StartsAt, p.Role == "admin"); err != nil {
		respondError(c, err)
		return
	}
	c.Status(204)
}
func (h *HTTP) stats(c *gin.Context) {
	p, ok := ownerSession(c)
	if !ok {
		return
	}
	rows, err := h.pool.Stats(c.Request.Context(), p.UserID, p.Role == "admin")
	if err != nil {
		respondError(c, err)
		return
	}
	c.JSON(200, gin.H{"accounts": rows})
}
func (h *HTTP) apiKey(c *gin.Context) {
	_, ok := ownerSession(c)
	if !ok {
		return
	}
	if h.management == nil {
		poolError(c, 503, "engine login is unavailable")
		return
	}
	var body struct {
		ProviderID string `json:"providerId"`
		APIKey     string `json:"apiKey"`
	}
	if c.ShouldBindJSON(&body) != nil {
		poolError(c, 400, "providerId and apiKey are required")
		return
	}
	record, err := h.management.CreateAccountAPIKey(c.Request.Context(), body.ProviderID, body.APIKey)
	if errors.Is(err, coreauth.ErrAccountKeyInvalid) {
		poolError(c, 400, "unsupported provider or invalid API key")
		return
	}
	if errors.Is(err, coreauth.ErrAccountKeyConflict) {
		poolError(c, 409, "credential already configured")
		return
	}
	if err != nil {
		respondError(c, err)
		return
	}
	c.JSON(201, gin.H{"accountId": AccountID(record.ID)})
}

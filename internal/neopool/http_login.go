package neopool

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"time"

	"github.com/gin-gonic/gin"
)

// loginWriter buffers only the initial engine login response so the initiating
// user's state binding commits before its authorization URL becomes visible.
type loginWriter struct {
	gin.ResponseWriter
	body   bytes.Buffer
	status int
}

func (w *loginWriter) WriteHeader(status int) { w.status = status }
func (w *loginWriter) WriteHeaderNow()        {}
func (w *loginWriter) Write(b []byte) (int, error) {
	if w.body.Len()+len(b) > 65536 {
		return 0, io.ErrShortBuffer
	}
	return w.body.Write(b)
}
func (w *loginWriter) WriteString(text string) (int, error) { return w.Write([]byte(text)) }
func (w *loginWriter) Status() int {
	if w.status == 0 {
		return 200
	}
	return w.status
}
func (w *loginWriter) Size() int     { return w.body.Len() }
func (w *loginWriter) Written() bool { return w.body.Len() > 0 }
func (w *loginWriter) Flush()        {}

// engineContext prepares one existing management handler call on a fresh gin
// context. gin caches parsed query values per context on the first c.Query, and
// the credential middleware has already read the original pool request's query,
// so rewriting c.Request.URL in place would be invisible to the engine handler.
// Copy keeps keys, params and the engine but never the query/form caches.
func engineContext(c *gin.Context, path string, query url.Values) *gin.Context {
	inner := c.Copy()
	inner.Request = c.Request.Clone(c.Request.Context())
	inner.Request.URL.Path = path
	inner.Request.URL.RawPath = ""
	inner.Request.URL.RawQuery = query.Encode()
	inner.Request.RequestURI = inner.Request.URL.RequestURI()
	inner.Writer = c.Writer
	return inner
}

// engineErrorMessage extracts the engine's own error text from a failed
// response so pool failures stay diagnosable without exposing raw bodies.
func engineErrorMessage(body []byte) string {
	var parsed struct {
		Error   json.RawMessage `json:"error"`
		Message string          `json:"message"`
	}
	if json.Unmarshal(body, &parsed) != nil {
		return ""
	}
	var text string
	if json.Unmarshal(parsed.Error, &text) == nil && text != "" {
		return text
	}
	var nested struct {
		Message string `json:"message"`
	}
	if json.Unmarshal(parsed.Error, &nested) == nil && nested.Message != "" {
		return nested.Message
	}
	return parsed.Message
}

func (h *HTTP) login(c *gin.Context) {
	p, ok := ownerSession(c)
	if !ok {
		return
	}
	if h.management == nil {
		poolError(c, 503, "engine login is unavailable")
		return
	}
	provider := c.Param("provider")
	switch provider {
	case "codex", "anthropic", "claude", "gemini", "gemini-cli", "antigravity", "qwen", "iflow", "kimi", "devin", "xai", "grok-build", "meta", "zai", "openrouter", "cursor", "nous-portal":
	default:
		poolError(c, 400, "unsupported login provider")
		return
	}
	dispatch := provider
	if dispatch == "anthropic" {
		dispatch = "claude"
	}
	// Owners cannot smuggle engine config, filenames, or account IDs through this
	// narrow new-login operation.
	query := url.Values{"provider": {dispatch}, "is_webui": {"true"}}
	if dispatch == "codex" && c.Query("mode") == "device" {
		query.Set("mode", "device")
	}
	inner := engineContext(c, "/v8/management/oauth/auth-url", query)
	captured := &loginWriter{ResponseWriter: c.Writer}
	inner.Writer = captured
	h.management.StartOAuthV8(inner)
	if captured.Status() >= 400 {
		message := "provider login is unavailable"
		if detail := engineErrorMessage(captured.body.Bytes()); detail != "" {
			message += ": " + detail
		}
		c.AbortWithStatusJSON(captured.Status(), gin.H{"error": gin.H{"message": message, "engineStatus": captured.Status()}})
		return
	}
	var body struct {
		State string `json:"state"`
	}
	if err := json.Unmarshal(captured.body.Bytes(), &body); err != nil || body.State == "" {
		poolError(c, 502, "engine login returned no session state")
		return
	}
	tx, err := h.pool.db.BeginTx(c.Request.Context(), nil)
	if err != nil {
		respondError(c, err)
		return
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(c.Request.Context(), `DELETE FROM neo_pool_logins WHERE created_at<?`, h.pool.now().Add(-35*time.Minute).UnixMilli()); err == nil {
		_, err = tx.ExecContext(c.Request.Context(), `INSERT INTO neo_pool_logins(state,user_id,provider_id,created_at) VALUES(?,?,?,?)`, body.State, p.UserID, provider, h.pool.now().UnixMilli())
	}
	if err != nil {
		respondError(c, err)
		return
	}
	if err = tx.Commit(); err != nil {
		respondError(c, err)
		return
	}
	c.Data(captured.Status(), "application/json; charset=utf-8", captured.body.Bytes())
}
func (h *HTTP) loginOwned(c *gin.Context, state string) (string, bool) {
	p, ok := ownerSession(c)
	if !ok {
		return "", false
	}
	if h.management == nil {
		poolError(c, 503, "engine login is unavailable")
		return "", false
	}
	var provider string
	err := h.pool.db.QueryRowContext(c.Request.Context(), `SELECT provider_id FROM neo_pool_logins WHERE state=? AND user_id=? AND created_at>=?`, state, p.UserID, h.pool.now().Add(-35*time.Minute).UnixMilli()).Scan(&provider)
	if err != nil {
		poolError(c, 404, "login state is unknown or unavailable")
		return "", false
	}
	return provider, true
}
func (h *HTTP) loginStatus(c *gin.Context) {
	state := c.Query("state")
	if _, ok := h.loginOwned(c, state); !ok {
		return
	}
	h.management.GetAuthStatus(engineContext(c, c.Request.URL.Path, url.Values{"state": {state}}))
}
func (h *HTTP) loginCallback(c *gin.Context) {
	var body struct {
		Provider string `json:"provider"`
		State    string `json:"state"`
		Code     string `json:"code"`
		Error    string `json:"error"`
	}
	if c.ShouldBindJSON(&body) != nil || body.State == "" {
		poolError(c, http.StatusBadRequest, "login state is required")
		return
	}
	provider, ok := h.loginOwned(c, body.State)
	if !ok {
		return
	}
	if body.Provider != "" && body.Provider != provider {
		poolError(c, 403, "login provider does not match initiation")
		return
	}
	body.Provider = provider
	if body.Provider == "claude" {
		body.Provider = "anthropic"
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		respondError(c, err)
		return
	}
	inner := engineContext(c, c.Request.URL.Path, nil)
	inner.Request.Body = io.NopCloser(bytes.NewReader(encoded))
	inner.Request.ContentLength = int64(len(encoded))
	h.management.PostOAuthCallback(inner)
}

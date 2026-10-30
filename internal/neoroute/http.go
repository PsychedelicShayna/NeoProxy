package neoroute

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/neo"
)

// RegisterRoutes attaches only product routes to A's session-authenticated group.
// Inference keys are not dashboard sessions, even when their user is an admin.
func (s *Store) RegisterRoutes(group *gin.RouterGroup) {
	group.GET("/virtual-models", s.listVirtuals)
	group.GET("/virtual-models/:id", s.getVirtual)
	group.POST("/virtual-models", s.saveVirtual(true))
	group.PUT("/virtual-models/:id", s.saveVirtual(false))
	group.DELETE("/virtual-models/:id", s.deleteVirtual)
	group.POST("/simulate", s.simulateHTTP)
	group.GET("/usage", s.usageHTTP)
	group.GET("/requests", s.requestsHTTP)
	group.GET("/requests/:id", s.requestHTTP)
}
func sessionPrincipal(c *gin.Context) (neo.Principal, bool) {
	p, ok := neo.PrincipalFromContext(c.Request.Context())
	if !ok || !p.Session {
		c.AbortWithStatusJSON(401, gin.H{"error": gin.H{"message": "Dashboard session required"}})
		return p, false
	}
	return p, true
}
func adminPrincipal(c *gin.Context) (neo.Principal, bool) {
	p, ok := sessionPrincipal(c)
	if !ok {
		return p, false
	}
	if !p.IsAdmin() {
		c.AbortWithStatusJSON(403, gin.H{"error": gin.H{"message": "Organization administrator required"}})
		return p, false
	}
	return p, true
}
func routeHTTPError(c *gin.Context, err error) {
	status := http.StatusInternalServerError
	message := "Routing operation failed"
	code := "neoproxy_error"
	var routeErr *Error
	if errors.As(err, &routeErr) {
		status = routeErr.Status
		message = routeErr.Error()
		code = routeErr.Code
	} else if errors.Is(err, sql.ErrNoRows) {
		status = 404
		message = "Not found"
	} else if errors.Is(err, neo.ErrForbidden) {
		status = 403
		message = "Forbidden"
	}
	c.JSON(status, gin.H{"error": gin.H{"message": message, "code": code}})
}

// Keep this slice's new Lua configuration/results administrator-only. Existing
// stage visibility is a separate member-projection migration.
func withoutLuaConfiguration(vm VirtualModel) VirtualModel {
	vm.Script = ""
	for i := range vm.Stages {
		vm.Stages[i].Lua = nil
	}
	return vm
}

func policyDecisionForViewer(d Decision, p neo.Principal) Decision {
	if p.IsAdmin() {
		return d
	}
	for _, step := range d.Trail {
		if step.Policy != nil || step.Skipped == "lua-policy" || step.Skipped == "lua-cap" {
			// Effective caps and candidate budgets also contain callback results;
			// removing only the policy object would still disclose those numbers.
			d.Trail = []TrailStep{}
			break
		}
	}
	return d
}
func (s *Store) listVirtuals(c *gin.Context) {
	p, ok := sessionPrincipal(c)
	if !ok {
		return
	}
	vms, err := s.VirtualModels(c.Request.Context())
	if err != nil {
		routeHTTPError(c, err)
		return
	}
	out := make([]VirtualModel, 0, len(vms))
	for _, vm := range vms {
		if p.IsAdmin() {
			out = append(out, vm)
		} else if _, granted := grantFor(p, vm.ID); granted {
			vm = withoutLuaConfiguration(vm)
			out = append(out, vm)
		}
	}
	c.JSON(200, out)
}
func (s *Store) getVirtual(c *gin.Context) {
	p, ok := sessionPrincipal(c)
	if !ok {
		return
	}
	vm, err := s.VirtualModel(c.Request.Context(), c.Param("id"))
	if err != nil {
		routeHTTPError(c, err)
		return
	}
	if !p.IsAdmin() {
		if _, granted := grantFor(p, vm.ID); !granted {
			c.JSON(404, gin.H{"error": gin.H{"message": "Not found"}})
			return
		}
		vm = withoutLuaConfiguration(vm)
	}
	c.JSON(200, vm)
}
func (s *Store) saveVirtual(create bool) gin.HandlerFunc {
	return func(c *gin.Context) {
		if _, ok := adminPrincipal(c); !ok {
			return
		}
		var vm VirtualModel
		if err := c.ShouldBindJSON(&vm); err != nil {
			c.JSON(400, gin.H{"error": gin.H{"message": "Invalid virtual model JSON"}})
			return
		}
		if !create {
			vm.ID = c.Param("id")
		}
		saved, err := s.SaveVirtualModel(c.Request.Context(), vm, create)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				routeHTTPError(c, err)
			} else {
				c.JSON(400, gin.H{"error": gin.H{"message": err.Error()}})
			}
			return
		}
		status := 200
		if create {
			status = 201
		}
		c.JSON(status, saved)
	}
}
func (s *Store) deleteVirtual(c *gin.Context) {
	if _, ok := adminPrincipal(c); !ok {
		return
	}
	if err := s.DeleteVirtualModel(c.Request.Context(), c.Param("id")); err != nil {
		routeHTTPError(c, err)
		return
	}
	c.Status(204)
}
func (s *Store) simulateHTTP(c *gin.Context) {
	p, ok := sessionPrincipal(c)
	if !ok {
		return
	}
	var input struct {
		Asked    string          `json:"asked"`
		UserID   string          `json:"userId"`
		Protocol string          `json:"protocol"`
		Request  json.RawMessage `json:"request"`
	}
	if c.ShouldBindJSON(&input) != nil || input.Asked == "" {
		c.JSON(400, gin.H{"error": gin.H{"message": "asked is required"}})
		return
	}
	if input.Protocol != "" && input.Protocol != "openai" && input.Protocol != "openai-response" && input.Protocol != "claude" {
		c.JSON(400, gin.H{"error": gin.H{"message": "Unsupported simulation protocol"}})
		return
	}
	if input.UserID == "" {
		input.UserID = p.UserID
	}
	if !p.CanAccessUser(input.UserID) {
		c.JSON(403, gin.H{"error": gin.H{"message": "Forbidden"}})
		return
	}
	target, err := s.identity.PrincipalForUser(c.Request.Context(), input.UserID)
	if err != nil {
		routeHTTPError(c, err)
		return
	}
	if input.UserID == p.UserID {
		target = p
	}
	ctx := neo.WithPrincipal(c.Request.Context(), target)
	d, err := s.Simulate(ctx, Request{Asked: input.Asked, Protocol: input.Protocol, Body: input.Request})
	if err != nil {
		routeHTTPError(c, err)
		return
	}
	c.JSON(200, policyDecisionForViewer(d, p))
}
func (s *Store) usageHTTP(c *gin.Context) {
	p, ok := sessionPrincipal(c)
	if !ok {
		return
	}
	ctx := c.Request.Context()
	userID := p.UserID
	if p.IsAdmin() {
		userID = ""
	}
	usage, err := s.pools.Usage(ctx, userID)
	if err != nil {
		routeHTTPError(c, err)
		return
	}
	virtuals, err := s.VirtualModels(ctx)
	if err != nil {
		routeHTTPError(c, err)
		return
	}
	snapshot, err := s.pools.Snapshot(ctx)
	if err != nil {
		routeHTTPError(c, err)
		return
	}
	windows := make(map[string]map[string]string)
	for _, vm := range virtuals {
		if !p.IsAdmin() {
			if _, granted := grantFor(p, vm.ID); !granted {
				continue
			}
		}
		windows[vm.ID] = make(map[string]string)
		for _, stage := range vm.Stages {
			windows[vm.ID][stage.ID] = stage.Window
		}
	}
	type accounts map[string]float64
	type stages map[string]accounts
	type modelUsage map[string]stages
	out := map[string]modelUsage{}
	for _, u := range usage {
		if windows[u.VirtualModelID][u.StageID] != u.Window || u.VirtualModelID == "" || u.StageID == "" {
			continue
		}
		account, found := accountByID(snapshot, u.AccountID)
		if !found {
			continue
		}
		window, known := windowFor(account, u.Window, s.now().UnixMilli())
		if !known || window.StartsAt != u.StartsAt || window.ResetsAt != u.ResetsAt {
			continue
		}
		if out[u.UserID] == nil {
			out[u.UserID] = modelUsage{}
		}
		if out[u.UserID][u.VirtualModelID] == nil {
			out[u.UserID][u.VirtualModelID] = stages{}
		}
		if out[u.UserID][u.VirtualModelID][u.StageID] == nil {
			out[u.UserID][u.VirtualModelID][u.StageID] = accounts{}
		}
		out[u.UserID][u.VirtualModelID][u.StageID][u.AccountID] += float64(u.Tokens+u.Reserved) / float64(window.Capacity)
	}
	c.JSON(200, out)
}
func (s *Store) requestsHTTP(c *gin.Context) {
	p, ok := sessionPrincipal(c)
	if !ok {
		return
	}
	limit := 50
	if raw := c.Query("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > 200 {
			c.JSON(400, gin.H{"error": gin.H{"message": "limit must be 1..200"}})
			return
		}
		limit = n
	}
	query := `SELECT body FROM neoroute_requests WHERE 1=1`
	args := make([]any, 0, 6)
	if !p.IsAdmin() {
		query += ` AND user_id=?`
		args = append(args, p.UserID)
	} else if id := c.Query("userId"); id != "" {
		query += ` AND user_id=?`
		args = append(args, id)
	}
	if outcome := c.Query("outcome"); outcome != "" {
		query += ` AND outcome=?`
		args = append(args, outcome)
	}
	if asked := c.Query("asked"); asked != "" {
		query += ` AND asked=?`
		args = append(args, asked)
	}
	if raw := c.Query("before"); raw != "" {
		before, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			c.JSON(400, gin.H{"error": gin.H{"message": "Invalid before timestamp"}})
			return
		}
		query += ` AND at<?`
		args = append(args, before)
	}
	query += ` ORDER BY at DESC,id DESC LIMIT ?`
	args = append(args, limit)
	rows, err := s.db.QueryContext(c.Request.Context(), query, args...)
	if err != nil {
		routeHTTPError(c, err)
		return
	}
	defer rows.Close()
	out := make([]RequestRecord, 0)
	for rows.Next() {
		var raw []byte
		var record RequestRecord
		if err = rows.Scan(&raw); err != nil {
			routeHTTPError(c, err)
			return
		}
		if err = json.Unmarshal(raw, &record); err != nil {
			routeHTTPError(c, err)
			return
		}
		record.Decision = policyDecisionForViewer(record.Decision, p)
		out = append(out, record)
	}
	if err = rows.Err(); err != nil {
		routeHTTPError(c, err)
		return
	}
	c.JSON(200, out)
}
func (s *Store) requestHTTP(c *gin.Context) {
	p, ok := sessionPrincipal(c)
	if !ok {
		return
	}
	query := `SELECT body FROM neoroute_requests WHERE id=?`
	args := []any{c.Param("id")}
	if !p.IsAdmin() {
		query += ` AND user_id=?`
		args = append(args, p.UserID)
	}
	var raw []byte
	if err := s.db.QueryRowContext(c.Request.Context(), query, args...).Scan(&raw); err != nil {
		routeHTTPError(c, err)
		return
	}
	var record RequestRecord
	if err := json.Unmarshal(raw, &record); err != nil {
		routeHTTPError(c, err)
		return
	}
	record.Decision = policyDecisionForViewer(record.Decision, p)
	c.JSON(200, record)
}

// ModelsHTTP returns the OpenAI shape shared by all three day-one entry protocols.
func (s *Store) ModelsHTTP(c *gin.Context) {
	models, err := s.Models(c.Request.Context())
	if err != nil {
		routeHTTPError(c, err)
		return
	}
	data := make([]gin.H, 0, len(models))
	for _, id := range models {
		data = append(data, gin.H{"id": id, "object": "model", "owned_by": "neoproxy"})
	}
	c.JSON(200, gin.H{"object": "list", "data": data})
}

// ClientName extracts only a short public client label, never authorization data.
func ClientName(headers http.Header) string {
	value := headers.Get("X-Client")
	if value == "" {
		value = headers.Get("User-Agent")
	}
	if len(value) > 128 {
		value = value[:128]
	}
	return strings.ToValidUTF8(value, "")
}

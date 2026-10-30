package neopool

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"
)

func sourceSession(c *gin.Context) bool {
	if _, ok := session(c); !ok {
		return false
	}
	if _, err := sourceAdmin(c.Request.Context()); err != nil {
		poolError(c, http.StatusForbidden, "organization administrator required to manage inference sources")
		return false
	}
	return true
}

func (h *HTTP) sourceList(c *gin.Context) {
	if !sourceSession(c) {
		return
	}
	list, err := h.pool.Sources(c.Request.Context())
	if err != nil {
		respondError(c, err)
		return
	}
	c.JSON(http.StatusOK, list)
}

func (h *HTTP) sourceSave(c *gin.Context) {
	if !sourceSession(c) {
		return
	}
	var in SourceInput
	if c.ShouldBindJSON(&in) != nil {
		poolError(c, http.StatusBadRequest, "source label, URL, owner and gateway bearer are required")
		return
	}
	v, err := h.pool.SaveSource(c.Request.Context(), c.Param("id"), in)
	if err != nil {
		if errors.Is(err, ErrForbidden) {
			respondError(c, err)
		} else {
			poolError(c, http.StatusBadRequest, "invalid source configuration; use a valid URL, active account owner and a bearer (required for a new or changed URL)")
		}
		return
	}
	if v.Enabled {
		_ = h.pool.SyncSource(c.Request.Context(), v.ID)
	}
	h.sourceResult(c, v.ID)
}

func (h *HTTP) sourceSync(c *gin.Context) {
	if !sourceSession(c) {
		return
	}
	if err := h.pool.SyncSource(c.Request.Context(), c.Param("id")); err != nil {
		// The durable safe status describes failed discovery without exposing the
		// gateway response or bearer. Return it so the dashboard can offer retry.
		list, listErr := h.pool.Sources(c.Request.Context())
		if listErr != nil {
			respondError(c, listErr)
			return
		}
		for _, v := range list {
			if v.ID == c.Param("id") {
				c.JSON(http.StatusOK, v)
				return
			}
		}
		poolError(c, http.StatusNotFound, "source is unavailable")
		return
	}
	h.sourceResult(c, c.Param("id"))
}

func (h *HTTP) sourceResult(c *gin.Context, id string) {
	list, err := h.pool.Sources(c.Request.Context())
	if err != nil {
		respondError(c, err)
		return
	}
	for _, v := range list {
		if v.ID == id {
			c.JSON(http.StatusOK, v)
			return
		}
	}
	poolError(c, http.StatusNotFound, "source is unavailable")
}

func (h *HTTP) sourceDelete(c *gin.Context) {
	if !sourceSession(c) {
		return
	}
	if err := h.pool.DeleteSource(c.Request.Context(), c.Param("id")); err != nil {
		respondError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

// Package neopool owns account inventory, ownership and metered token accounting.
// Credentials and refresh remain exclusively in the existing engine auth manager.
package neopool

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
)

var (
	ErrForbidden   = errors.New("account operation is not authorized")
	ErrUnavailable = errors.New("account or measured window is unavailable")
	ErrBudget      = errors.New("account or stage budget exhausted")
	ErrConflict    = errors.New("request identity conflicts with an existing attempt")
)

// AccountID masks engine IDs, which can contain a full email or filename.
func AccountID(engineAuthID string) string {
	sum := sha256.Sum256([]byte(engineAuthID))
	return "acct_" + hex.EncodeToString(sum[:16])
}

type Provider struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Vendor  string `json:"vendor"`
	Kind    string `json:"kind"`
	Exposed bool   `json:"exposed"`
}
type Model struct {
	ID          string   `json:"id"`
	ProviderID  string   `json:"providerId"`
	Name        string   `json:"name"`
	Display     string   `json:"display"`
	Served      string   `json:"served"`
	Tags        []string `json:"tags"`
	Context     int      `json:"context"`
	VirtualOnly bool     `json:"virtualOnly,omitempty"`
	Exposed     bool     `json:"exposed"`
}
type Account struct {
	ID              string   `json:"id"`
	EngineAuthID    string   `json:"-"`
	ProviderID      string   `json:"providerId"`
	OwnerID         string   `json:"ownerId"`
	Label           string   `json:"label"`
	Plan            string   `json:"plan"`
	State           string   `json:"state"`
	CooldownUntil   int64    `json:"cooldownUntil,omitempty"`
	SourceID        string   `json:"sourceId,omitempty"`
	ProviderWindows []Window `json:"providerWindows,omitempty"`
	ModelIDs        []string `json:"modelIds"`
	Windows         []Window `json:"windows"`
}

// Window describes a locally configured token meter, NOT a claimed upstream quota.
// Missing provider measurements are represented by no windows, never zero use.
type Window struct {
	Kind       string  `json:"kind"`
	Known      bool    `json:"known"`
	Source     string  `json:"source"`
	Unit       string  `json:"unit"`
	Capacity   int64   `json:"capacity"`
	UsedTokens int64   `json:"usedTokens"`
	Used       float64 `json:"used"`
	StartsAt   int64   `json:"startsAt"`
	ResetsAt   int64   `json:"resetsAt"`
}
type Snapshot struct {
	Providers []Provider `json:"providers"`
	Accounts  []Account  `json:"accounts"`
	Models    []Model    `json:"models"`
}
type Usage struct {
	UserID         string  `json:"userId"`
	VirtualModelID string  `json:"virtualModelId"`
	StageID        string  `json:"stageId"`
	AccountID      string  `json:"accountId"`
	Window         string  `json:"window"`
	StartsAt       int64   `json:"startsAt"`
	ResetsAt       int64   `json:"resetsAt"`
	Tokens         int64   `json:"tokens"`
	Reserved       int64   `json:"reserved"`
	Fraction       float64 `json:"fraction"`
}

// Reservation.ID is a globally unique execution-attempt identity. Repeated IDs
// are idempotent only when all fields match. Tokens is a conservative upper bound.
// BudgetFree direct/passthrough attempts do not reserve a budget.
type Reservation struct {
	ID             string
	RequestID      string
	UserID         string
	VirtualModelID string
	StageID        string
	AccountID      string
	AccountIDs     []string
	ScopeOwnerID   string
	Window         string
	Tokens         int64
	PerAccount     float64
	Cap            float64
	Shared         bool
	BudgetFree     bool
}

// Settlement is final. Known usage replaces the reservation; missing usage
// conservatively charges its upper bound, separately marked as estimated.
// CachedTokens is a subset of InputTokens, not an additional billable token count.
type Settlement struct {
	ID           string
	AccountID    string
	InputTokens  int64
	OutputTokens int64
	CachedTokens int64
	UsageKnown   bool
}
type AccountStats struct {
	AccountID       string `json:"accountId"`
	OwnerID         string `json:"ownerId"`
	InputTokens     int64  `json:"inputTokens"`
	OutputTokens    int64  `json:"outputTokens"`
	CachedTokens    int64  `json:"cachedTokens"`
	EstimatedTokens int64  `json:"estimatedTokens"`
	ReservedTokens  int64  `json:"reservedTokens"`
	UnknownAttempts int64  `json:"unknownAttempts"`
	Attempts        int64  `json:"attempts"`
}

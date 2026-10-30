// Package neoroute owns virtual-model policy, resolution, and execution attribution.
package neoroute

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
)

// AccountScope is either the literal "all" or an explicit ordered account list.
type AccountScope struct {
	All bool
	IDs []string
}

func (s AccountScope) MarshalJSON() ([]byte, error) {
	if s.All {
		return []byte(`"all"`), nil
	}
	if s.IDs == nil {
		return []byte(`[]`), nil
	}
	return json.Marshal(s.IDs)
}
func (s *AccountScope) UnmarshalJSON(b []byte) error {
	var all string
	if json.Unmarshal(b, &all) == nil {
		if all != "all" {
			return fmt.Errorf("accounts must be all or an account list")
		}
		s.All = true
		s.IDs = nil
		return nil
	}
	var ids []string
	if err := json.Unmarshal(b, &ids); err != nil {
		return err
	}
	if ids == nil {
		return fmt.Errorf("accounts cannot be null")
	}
	s.All = false
	s.IDs = ids
	return nil
}

type VirtualModel struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Served      string   `json:"served"`
	Description string   `json:"description"`
	Stages      []Stage  `json:"stages"`
	Terminal    Terminal `json:"terminal"`
	Script      string   `json:"script,omitempty"`
	UpdatedAt   int64    `json:"updatedAt"`
	UpdatedBy   string   `json:"updatedBy"`
}
type Stage struct {
	ID         string       `json:"id"`
	Model      string       `json:"model"`
	Accounts   AccountScope `json:"accounts"`
	Draw       string       `json:"draw"`
	PerAccount float64      `json:"perAccount"`
	Cap        float64      `json:"cap"`
	Window     string       `json:"window"`
	Per        string       `json:"per"`
	Effort     *string      `json:"effort"`
	Lua        *LuaPolicy   `json:"lua,omitempty"`
}

// LuaPolicy names functions in VirtualModel.Script. Limits are absolute window
// fractions before grant scaling, intersected with the stage's static ceilings.
type LuaPolicy struct {
	Eligible   string `json:"eligible,omitempty"`
	PerAccount string `json:"perAccount,omitempty"`
	Cap        string `json:"cap,omitempty"`
}
type Terminal struct {
	Kind    string `json:"kind"`
	Message string `json:"message"`
	Model   string `json:"model,omitempty"`
}
type Candidate struct {
	AccountID string  `json:"accountId"`
	Used      float64 `json:"used"`
	Budget    float64 `json:"budget"`
}
type TrailStep struct {
	Kind          string              `json:"kind"`
	Index         int                 `json:"index,omitempty"`
	StageID       string              `json:"stageId,omitempty"`
	Model         string              `json:"model,omitempty"`
	Total         float64             `json:"total,omitempty"`
	Cap           float64             `json:"cap,omitempty"`
	Candidates    []Candidate         `json:"candidates,omitempty"`
	Chosen        string              `json:"chosen,omitempty"`
	Skipped       string              `json:"skipped,omitempty"`
	Terminal      *Terminal           `json:"terminal,omitempty"`
	ProviderID    string              `json:"providerId,omitempty"`
	VirtualModel  string              `json:"virtualModel,omitempty"`
	AccountID     string              `json:"accountId,omitempty"`
	Asked         string              `json:"asked,omitempty"`
	FailureStatus int                 `json:"failureStatus,omitempty"`
	Policy        *ScriptPolicyResult `json:"policy,omitempty"`
}

// Stage zero values are meaningful: preserve index 0, a zero cap and an empty
// candidate list while keeping the other trail variants compact.
func (t TrailStep) MarshalJSON() ([]byte, error) {
	type plain TrailStep
	if t.Kind != "stage" {
		return json.Marshal(plain(t))
	}
	candidates := t.Candidates
	if candidates == nil {
		candidates = []Candidate{}
	}
	return json.Marshal(struct {
		plain
		Index      int         `json:"index"`
		Total      float64     `json:"total"`
		Cap        float64     `json:"cap"`
		Candidates []Candidate `json:"candidates"`
	}{plain(t), t.Index, t.Total, t.Cap, candidates})
}

type Decision struct {
	Outcome      string      `json:"outcome"`
	Direct       bool        `json:"direct,omitempty"`
	VirtualModel string      `json:"virtualModel,omitempty"`
	StageIndex   *int        `json:"stageIndex,omitempty"`
	StageID      string      `json:"stageId,omitempty"`
	Model        string      `json:"model,omitempty"`
	AccountID    string      `json:"accountId,omitempty"`
	Effort       *string     `json:"effort,omitempty"`
	Reason       string      `json:"reason,omitempty"`
	Message      string      `json:"message,omitempty"`
	ResetsAt     int64       `json:"resetsAt,omitempty"`
	Trail        []TrailStep `json:"trail"`
}

// Request describes client input, not a privileged provider selection. Identity is
// read exclusively from neo.Principal in Context; callers must not manufacture it.
type Request struct {
	Asked    string
	Protocol string
	Body     []byte
	Stream   bool
	Client   string
}

// Selection is the validated, reserved account selection passed to the existing
// engine. The adapter MUST pin AuthID and Provider on every inner engine retry.
type Selection struct {
	Asked     string
	Provider  string
	Model     string
	AuthID    string
	Effort    *string
	RequestID string
	AttemptID string
	Decision  Decision
}
type Tokens struct {
	In     int64 `json:"in"`
	Out    int64 `json:"out"`
	Cached int64 `json:"cached"`
}
type Chunk struct {
	Data []byte
	Err  error
}

// Execution is either a complete Body or a stream. Stream closes once; terminal
// errors travel as chunks. The engine's actual usage reporter settles reservations.
type Execution struct {
	Body    []byte
	Headers http.Header
	Chunks  <-chan Chunk
}
type ExecuteFunc func(context.Context, Selection) (*Execution, error)

// Resolver is the handler seam. Run tries later eligible stages after upstream
// failure only before the first emitted chunk. Simulate never reserves or rotates.
type Resolver interface {
	Run(context.Context, Request, ExecuteFunc) (*Execution, error)
	Simulate(context.Context, Request) (Decision, error)
	Models(context.Context) ([]string, error)
}

// ScriptHook receives only value snapshots, never credentials or database handles.
// A proposal is advisory: C rechecks all authority, eligibility and budgets and
// reserves atomically. Errors and configured scripts without a hook fail closed.
type ScriptHook interface {
	Evaluate(context.Context, ScriptInput) (ScriptProposal, error)
}

// ScriptMaxInput bounds a scripted request before copying its business body.
const ScriptMaxInput = 256 << 10

type ScriptInput struct {
	Source   string `json:"-"`
	Asked    string `json:"asked"`
	Protocol string `json:"protocol"`
	UserID   string `json:"userId"`
	// Request is copied client business JSON before credential/header injection.
	Request      json.RawMessage `json:"request"`
	VirtualModel VirtualModel    `json:"virtualModel"`
	Stages       []ScriptStage   `json:"stages"`
	Accounts     []ScriptAccount `json:"accounts"`
}
type ScriptStage struct {
	StageID    string      `json:"stageId"`
	Model      string      `json:"model"`
	Candidates []Candidate `json:"candidates"`
	Total      float64     `json:"total"`
	Cap        float64     `json:"cap"`
	Skipped    string      `json:"skipped,omitempty"`
	Lua        *LuaPolicy  `json:"lua,omitempty"`
	Allotment  float64     `json:"allotment"`
	Window     string      `json:"window"`
}
type ScriptAccount struct {
	AccountID       string         `json:"accountId"`
	ProviderID      string         `json:"providerId"`
	State           string         `json:"state"`
	Windows         []ScriptWindow `json:"windows"`
	SourceID        string         `json:"sourceId,omitempty"`
	ProviderWindows []ScriptWindow `json:"providerWindows"`
}
type ScriptWindow struct {
	Kind     string  `json:"kind"`
	Known    bool    `json:"known"`
	Used     float64 `json:"used"`
	ResetsAt int64   `json:"resetsAt"`
}
type ScriptProposal struct {
	StageID   string               `json:"stageId"`
	AccountID string               `json:"accountId"`
	Policies  []ScriptPolicyResult `json:"policies,omitempty"`
}

// Policy results are advisory reductions, keyed only to authorized candidates.
type ScriptPolicyResult struct {
	StageID  string                `json:"stageId"`
	Cap      *float64              `json:"cap,omitempty"`
	Accounts []ScriptAccountPolicy `json:"accounts"`
}
type ScriptAccountPolicy struct {
	AccountID string   `json:"accountId"`
	Eligible  bool     `json:"eligible"`
	Limit     *float64 `json:"limit,omitempty"`
}

// Error carries the stable HTTP status/code and full authorized decision.
type Error struct {
	Status   int
	Code     string
	Decision Decision
}

func (e *Error) Error() string {
	if e.Decision.Message != "" {
		return e.Decision.Message
	}
	return e.Decision.Reason
}
func (e *Error) StatusCode() int { return e.Status }

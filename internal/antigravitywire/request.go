package antigravitywire

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"math/big"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/jsjson"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
)

// ForcedToolDirective is google-antigravity-forced-tool.md: Cloud Code Assist
// drops `toolConfig` on Antigravity's Gemini routes, so a forced tool choice
// is restated in the transcript.
const ForcedToolDirective = "TOOL-ONLY TURN. This turn accepts a tool call and nothing else; a text reply here is discarded unread and you will be re-prompted. Emit the tool call now."

// ThinkingLevel is a Gemini ThinkingLevel enum value on the wire.
type ThinkingLevel string

// ToolChoice is GoogleGeminiCliOptions.toolChoice: Mode is "auto", "none" or
// "any"; Allowed forces one named tool (wire mode ANY plus an allow-list).
type ToolChoice struct {
	Mode    string
	Allowed []string
}

// Thinking is GoogleGeminiCliOptions.thinking.
type Thinking struct {
	Enabled bool
	// Level or Budget configure an enabled request; Level wins when set.
	Level  ThinkingLevel
	Budget *int
	// SuppressLevel or SuppressBudget make a thinking-off request explicit on
	// the wire (models with thinking.suppressWhenOff); Suppress turns it on.
	Suppress       bool
	SuppressLevel  ThinkingLevel
	SuppressBudget *int
}

// Options are the request options buildRequest reads.
type Options struct {
	Temperature, TopP, TopK, PresencePenalty *float64
	MaxTokens                                *float64
	ToolChoice                               *ToolChoice
	Thinking                                 Thinking
	HideThinkingSummary                      bool
	// RequestModelID is the effort-routed wire id (resolveWireModelId); empty
	// falls back to the model's own.
	RequestModelID string
}

// SessionState is AntigravityProviderSessionState: the per-conversation
// request-envelope identity that mirrors the real Antigravity client.
type SessionState struct {
	mu               sync.Mutex
	LastGoodEndpoint string
	agentID          string
	trajectoryID     string
	sessionID        string
	stepIndex        int
	requestCount     int
	lastExecutionID  string
}

// LastGood returns the endpoint the last fully successful request used.
func (s *SessionState) LastGood() string {
	if s == nil {
		return ""
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.LastGoodEndpoint
}

// SetLastGood records (or clears, with "") the preferred endpoint.
func (s *SessionState) SetLastGood(endpoint string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.LastGoodEndpoint = endpoint
	s.mu.Unlock()
}

// SetLastExecutionID commits the response id of a fully successful attempt as
// the next request's labels.last_execution_id (overwritten even when empty so
// a response without an id cannot leave a stale value).
func (s *SessionState) SetLastExecutionID(id string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.lastExecutionID = id
	s.mu.Unlock()
}

// Clock and identifier sources, replaceable by tests.
type Sources struct {
	Now       func() time.Time
	NewUUID   func() string
	RandomInt func(max *big.Int) *big.Int
}

// DefaultSources are the production sources (Date.now, crypto.randomUUID,
// crypto random 63-bit integers).
func DefaultSources() Sources {
	return Sources{Now: time.Now, NewUUID: uuid.NewString, RandomInt: func(max *big.Int) *big.Int {
		value, err := rand.Int(rand.Reader, max)
		if err != nil {
			return big.NewInt(time.Now().UnixNano())
		}
		return value
	}}
}

var (
	int63Mask          = new(big.Int).Sub(new(big.Int).Lsh(big.NewInt(1), 63), big.NewInt(1))
	antigravityRandMax = func() *big.Int { v, _ := new(big.Int).SetString("9000000000000000000", 10); return v }()
)

// randomSignedDecimalSessionID is randomSignedDecimalSessionId: `-` and a
// random value below 9e18.
func (s Sources) randomSignedDecimalSessionID() string {
	return "-" + s.RandomInt(antigravityRandMax).String()
}

// deriveSignedDecimalFromHash is deriveSignedDecimalFromHash: the first eight
// SHA-256 bytes as a 63-bit integer.
func deriveSignedDecimalFromHash(text string) string {
	digest := sha256.Sum256([]byte(text))
	value := new(big.Int).SetUint64(binary.BigEndian.Uint64(digest[:8]))
	return "-" + value.And(value, int63Mask).String()
}

// Input is everything buildRequest reads that is not an option.
type Input struct {
	Model     *registry.NeoPiModel
	ProjectID string
	// Contents are the converted Gemini contents (convertMessages).
	Contents []json.RawMessage
	// SystemPrompts are normalizeSystemPrompts(context.systemPrompt).
	SystemPrompts []string
	// Tools is the converted `tools` array (convertTools after
	// normalizeAntigravityTools), or nil when the context declares none.
	Tools json.RawMessage
	// ContextMessages counts context.messages; FirstUserText is the text
	// deriveAntigravitySessionId hashes when no session state exists.
	ContextMessages int
	FirstUserText   string
	Options         Options
	// State is the conversation's session state; nil builds ephemeral ids.
	State   *SessionState
	Sources Sources
}

func isAnthropicClass(model *registry.NeoPiModel) bool {
	return model.GeminiCompat != nil && model.GeminiCompat.IdentityClass == "anthropic"
}

// Profile is getAntigravityModelWireProfile: the discovered profile for the
// wire id, else the offline fallback table.
func Profile(model *registry.NeoPiModel, wireModelID string) *registry.NeoPiAntigravityWireProfile {
	if profile, ok := model.AntigravityWireProfiles[wireModelID]; ok {
		return &profile
	}
	if profile, ok := fallbackProfiles[wireModelID]; ok {
		return &profile
	}
	return nil
}

// fallbackProfiles is ANTIGRAVITY_MODEL_WIRE_PROFILES, the offline fallback.
var fallbackProfiles = map[string]registry.NeoPiAntigravityWireProfile{
	"gemini-3.5-flash-extra-low": {ModelEnum: "MODEL_PLACEHOLDER_M187", MaxOutputTokens: 65536},
	"gemini-3.5-flash-low":       {ModelEnum: "MODEL_PLACEHOLDER_M20", MaxOutputTokens: 65536},
	"gemini-3-flash-agent":       {ModelEnum: "MODEL_PLACEHOLDER_M132", MaxOutputTokens: 65536},
	"gemini-3.1-pro-low":         {ModelEnum: "MODEL_PLACEHOLDER_M36", MaxOutputTokens: 65535},
	"gemini-pro-agent":           {ModelEnum: "MODEL_PLACEHOLDER_M16", MaxOutputTokens: 65535},
	"claude-sonnet-4-6":          {MaxOutputTokens: 64000},
	"claude-opus-4-6-thinking":   {MaxOutputTokens: 64000},
}

// IsReviewedTierWire reports isReviewedTierVariantWireId for this provider;
// the registry owns the reviewed variant table.
var IsReviewedTierWire = registry.AntigravityIsReviewedTierWireID

// Envelope is AntigravityRequestEnvelope.
type Envelope struct {
	SessionID string
	RequestID string
	Labels    json.RawMessage
}

// buildEnvelope is buildAntigravityRequestEnvelope.
func buildEnvelope(in *Input, profile *registry.NeoPiAntigravityWireProfile) Envelope {
	sources := in.Sources
	contextSteps := max(1, in.ContextMessages)
	requestIndex := 0
	state := in.State
	var agentID, trajectoryID, sessionID, lastExecution string
	step := contextSteps
	if state != nil {
		state.mu.Lock()
		if state.agentID == "" {
			state.agentID = sources.NewUUID()
		}
		if state.trajectoryID == "" {
			state.trajectoryID = sources.NewUUID()
		}
		if state.sessionID == "" {
			state.sessionID = sources.randomSignedDecimalSessionID()
		}
		state.stepIndex = max(contextSteps, state.stepIndex+1)
		requestIndex = state.requestCount
		state.requestCount = requestIndex + 1
		agentID, trajectoryID, sessionID = state.agentID, state.trajectoryID, state.sessionID
		step, lastExecution = state.stepIndex, state.lastExecutionID
		state.mu.Unlock()
	} else {
		agentID, trajectoryID = sources.NewUUID(), sources.NewUUID()
		if strings.TrimSpace(in.FirstUserText) != "" {
			sessionID = deriveSignedDecimalFromHash(in.FirstUserText)
		} else {
			sessionID = sources.randomSignedDecimalSessionID()
		}
	}
	requestID := "agent/" + agentID + "/" + strconv.FormatInt(sources.Now().UnixMilli(), 10) + "/" + trajectoryID + "/" + strconv.Itoa(step)
	claude := isAnthropicClass(in.Model)
	fields := []jsjson.Field{{Name: "cascade_id", Value: jsjson.Marshal(agentID)}}
	if lastExecution != "" {
		fields = append(fields, jsjson.Field{Name: "last_execution_id", Value: jsjson.Marshal(lastExecution)})
	}
	fields = append(fields, jsjson.Field{Name: "last_step_index", Value: jsjson.Marshal(strconv.Itoa(step - 1))})
	if profile != nil && profile.ModelEnum != "" {
		fields = append(fields, jsjson.Field{Name: "model_enum", Value: jsjson.Marshal(profile.ModelEnum)})
	}
	usageLabel := strconv.FormatBool(claude)
	if in.Model.GeminiCompat != nil && in.Model.GeminiCompat.AntigravityUsageLabel != "" {
		usageLabel = in.Model.GeminiCompat.AntigravityUsageLabel
	}
	fields = append(fields,
		jsjson.Field{Name: "request_id", Value: jsjson.Marshal(trajectoryID + "-" + strconv.Itoa(requestIndex))},
		jsjson.Field{Name: "root_cascade_id", Value: jsjson.Marshal(agentID)},
		jsjson.Field{Name: "trajectory_id", Value: jsjson.Marshal(trajectoryID)},
		jsjson.Field{Name: "used_claude", Value: jsjson.Marshal(usageLabel)},
		jsjson.Field{Name: "used_claude_conservative", Value: jsjson.Marshal(usageLabel)},
		jsjson.Field{Name: "used_non_gemini_model", Value: jsjson.Marshal(strconv.FormatBool(!identityIs(in.Model, "gemini")))},
	)
	return Envelope{SessionID: sessionID, RequestID: requestID, Labels: jsjson.Object(fields...)}
}

func identityIs(model *registry.NeoPiModel, class string) bool {
	return model.GeminiCompat != nil && model.GeminiCompat.IdentityClass == class
}

// toWellFormed is String.prototype.toWellFormed for Go strings.
func toWellFormed(text string) string {
	if utf8.ValidString(text) {
		return text
	}
	return strings.ToValidUTF8(text, "\uFFFD")
}

// BuildRequest is buildRequest for the Antigravity path: the Cloud Code
// Assist envelope with NeoPi's key order. It returns the serialized body.
func BuildRequest(in *Input) json.RawMessage {
	model, options := in.Model, in.Options
	compat := model.GeminiCompat
	if compat == nil {
		compat = &registry.NeoPiGeminiCompat{}
	}
	claude := isAnthropicClass(model)

	// generationConfig keeps insertion order; thinkingConfig stays a field
	// whose slot later reassignments preserve.
	type generationField struct {
		name  string
		value json.RawMessage
	}
	var generation []generationField
	setGeneration := func(name string, value json.RawMessage) {
		for i := range generation {
			if generation[i].name == name {
				generation[i].value = value
				return
			}
		}
		generation = append(generation, generationField{name, value})
	}
	number := func(value float64) json.RawMessage { return json.RawMessage(jsjson.Number(value)) }
	if options.Temperature != nil {
		setGeneration("temperature", number(*options.Temperature))
	}
	if options.MaxTokens != nil {
		setGeneration("maxOutputTokens", number(*options.MaxTokens))
	}
	if options.TopP != nil {
		setGeneration("topP", number(*options.TopP))
	}
	if options.TopK != nil {
		setGeneration("topK", number(*options.TopK))
	}
	if options.PresencePenalty != nil {
		setGeneration("presencePenalty", number(*options.PresencePenalty))
	}

	type thinkingConfig struct {
		includeThoughts bool
		level           ThinkingLevel
		budget          *int
	}
	var thinking *thinkingConfig
	thinkingJSON := func(config *thinkingConfig) json.RawMessage {
		fields := []jsjson.Field{{Name: "includeThoughts", Value: jsjson.Marshal(config.includeThoughts)}}
		if config.level != "" {
			fields = append(fields, jsjson.Field{Name: "thinkingLevel", Value: jsjson.Marshal(string(config.level))})
		} else if config.budget != nil {
			fields = append(fields, jsjson.Field{Name: "thinkingBudget", Value: jsjson.Marshal(*config.budget)})
		}
		return jsjson.Object(fields...)
	}
	if options.Thinking.Enabled && model.Reasoning {
		thinking = &thinkingConfig{includeThoughts: !options.HideThinkingSummary}
		if options.Thinking.Level != "" {
			thinking.level = options.Thinking.Level
		} else if options.Thinking.Budget != nil {
			thinking.budget = options.Thinking.Budget
		}
	} else if options.Thinking.Suppress && model.Reasoning {
		// Explicit off: omitting thinkingConfig re-applies the per-id baked
		// server default (the model silently thinks and bills the tokens).
		thinking = &thinkingConfig{includeThoughts: false}
		if options.Thinking.SuppressLevel != "" {
			thinking.level = options.Thinking.SuppressLevel
		} else if options.Thinking.SuppressBudget != nil {
			thinking.budget = options.Thinking.SuppressBudget
		}
	}
	if thinking != nil {
		setGeneration("thinkingConfig", thinkingJSON(thinking))
	}

	contents := append([]json.RawMessage(nil), in.Contents...)
	var systemInstruction json.RawMessage
	if len(in.SystemPrompts) > 0 {
		parts := make([]json.RawMessage, len(in.SystemPrompts))
		for i, text := range in.SystemPrompts {
			parts[i] = jsjson.Object(jsjson.Field{Name: "text", Value: jsjson.Marshal(text)})
		}
		systemInstruction = jsjson.Object(
			jsjson.Field{Name: "role", Value: jsjson.Marshal("user")},
			jsjson.Field{Name: "parts", Value: jsonArray(parts)},
		)
	}

	validated := jsjson.Object(jsjson.Field{Name: "functionCallingConfig", Value: jsjson.Object(
		jsjson.Field{Name: "mode", Value: jsjson.Marshal("VALIDATED")})})
	var toolConfig json.RawMessage
	if len(in.Tools) > 0 {
		if choice := options.ToolChoice; choice != nil {
			if choice.Allowed == nil {
				if mode := mapToolChoice(choice.Mode); mode != "AUTO" {
					toolConfig = jsjson.Object(jsjson.Field{Name: "functionCallingConfig", Value: jsjson.Object(
						jsjson.Field{Name: "mode", Value: jsjson.Marshal(mode)})})
				}
			} else {
				toolConfig = jsjson.Object(jsjson.Field{Name: "functionCallingConfig", Value: jsjson.Object(
					jsjson.Field{Name: "mode", Value: jsjson.Marshal("ANY")},
					jsjson.Field{Name: "allowedFunctionNames", Value: jsjson.Marshal(choice.Allowed)})})
			}
			// Cloud Code Assist drops `toolConfig` on Antigravity's Gemini
			// routes; Claude routes implement it.
			if !claude && toolConfig != nil && functionCallingMode(toolConfig) == "ANY" {
				contents = append(contents, jsjson.Object(
					jsjson.Field{Name: "role", Value: jsjson.Marshal("user")},
					jsjson.Field{Name: "parts", Value: jsonArray([]json.RawMessage{jsjson.Object(
						jsjson.Field{Name: "text", Value: jsjson.Marshal(ForcedToolDirective)})})}))
			}
		}
		// Antigravity's default tool mode is VALIDATED; an explicit non-auto
		// tool choice above wins.
		if toolConfig == nil {
			toolConfig = validated
		}
	}
	// Claude on Antigravity always forces VALIDATED, even with no tools declared.
	if claude && compat.AntigravityClaudeToolMode {
		toolConfig = validated
	}

	wireModelID := options.RequestModelID
	if wireModelID == "" {
		wireModelID = model.RequestModelID
	}
	if wireModelID == "" {
		wireModelID = model.ID
	}
	profile := Profile(model, wireModelID)
	if profile != nil && profile.MaxOutputTokens > 0 {
		setGeneration("maxOutputTokens", json.RawMessage(strconv.Itoa(profile.MaxOutputTokens)))
	}
	if profile != nil && thinking != nil && options.Thinking.Enabled && IsReviewedTierWire(wireModelID) {
		// A tier SKU fixes its own thinking strength; the effort only chose
		// the SKU. Send it the way the SKU advertises: a level when it has
		// one, else its budget (`-1` is dynamic).
		if profile.ThinkingLevel != "" {
			setGeneration("thinkingConfig", thinkingJSON(&thinkingConfig{includeThoughts: thinking.includeThoughts, level: ThinkingLevel(profile.ThinkingLevel)}))
		} else if profile.ThinkingBudget != nil {
			setGeneration("thinkingConfig", thinkingJSON(&thinkingConfig{includeThoughts: thinking.includeThoughts, budget: profile.ThinkingBudget}))
		}
	}
	envelope := buildEnvelope(in, profile)

	request := []jsjson.Field{{Name: "contents", Value: jsonArray(contents)}}
	if systemInstruction != nil {
		request = append(request, jsjson.Field{Name: "systemInstruction", Value: systemInstruction})
	}
	if len(in.Tools) > 0 {
		request = append(request, jsjson.Field{Name: "tools", Value: in.Tools})
	}
	if toolConfig != nil {
		request = append(request, jsjson.Field{Name: "toolConfig", Value: toolConfig})
	}
	request = append(request, jsjson.Field{Name: "labels", Value: envelope.Labels})
	if len(generation) > 0 {
		fields := make([]jsjson.Field, len(generation))
		for i, field := range generation {
			fields[i] = jsjson.Field{Name: field.name, Value: field.value}
		}
		request = append(request, jsjson.Field{Name: "generationConfig", Value: jsjson.Object(fields...)})
	}
	request = append(request, jsjson.Field{Name: "sessionId", Value: jsjson.Marshal(envelope.SessionID)})
	return jsjson.Object(
		jsjson.Field{Name: "project", Value: jsjson.Marshal(in.ProjectID)},
		jsjson.Field{Name: "requestId", Value: jsjson.Marshal(envelope.RequestID)},
		jsjson.Field{Name: "request", Value: jsjson.Object(request...)},
		jsjson.Field{Name: "model", Value: jsjson.Marshal(wireModelID)},
		jsjson.Field{Name: "userAgent", Value: jsjson.Marshal("antigravity")},
	)
}

func functionCallingMode(toolConfig json.RawMessage) string {
	var decoded struct {
		FunctionCallingConfig struct {
			Mode string `json:"mode"`
		} `json:"functionCallingConfig"`
	}
	_ = json.Unmarshal(toolConfig, &decoded)
	return decoded.FunctionCallingConfig.Mode
}

// mapToolChoice is mapToolChoice.
func mapToolChoice(choice string) string {
	switch choice {
	case "none":
		return "NONE"
	case "any":
		return "ANY"
	}
	return "AUTO"
}

func jsonArray(items []json.RawMessage) json.RawMessage {
	var out []byte
	out = append(out, '[')
	for i, item := range items {
		if i > 0 {
			out = append(out, ',')
		}
		out = append(out, item...)
	}
	return append(out, ']')
}

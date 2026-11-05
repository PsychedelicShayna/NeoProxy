package registry

import (
	"maps"
	"slices"
)

// NeoPiModel is a model as NeoPi's catalog resolves it (pi-catalog buildModel):
// the wire API, limits, effort ladder and the compat facts its OpenAI-family
// request builders branch on. Providers ported from NeoPi register it on
// ModelInfo.NeoPi; their executors shape requests from it alone.
type NeoPiModel struct {
	ID            string         `json:"id"`
	Name          string         `json:"name"`
	API           string         `json:"api"`
	Provider      string         `json:"provider"`
	BaseURL       string         `json:"baseUrl"`
	Reasoning     bool           `json:"reasoning"`
	Input         []string       `json:"input"`
	ContextWindow int            `json:"contextWindow"`
	MaxTokens     int            `json:"maxTokens"`
	Thinking      *NeoPiThinking `json:"thinking,omitempty"`
	Compat        *NeoPiCompat   `json:"compat,omitempty"`
	// AnthropicCompat and CompletionsCompat carry the compat NeoPi resolves for
	// an anthropic-messages or openai-completions row respectively.
	AnthropicCompat   *NeoPiAnthropicCompat   `json:"anthropicCompat,omitempty"`
	CompletionsCompat *NeoPiCompletionsCompat `json:"completionsCompat,omitempty"`
	// RequestModelID is the default wire id of a collapsed family row.
	RequestModelID string `json:"requestModelId,omitempty"`
	// DevinCompat is the compat NeoPi resolves for a devin-agent row.
	DevinCompat *NeoPiDevinCompat `json:"devinCompat,omitempty"`
}

// NeoPiDevinCompat is DevinCompat: whether the row is an AssignModel router
// and whether it takes parallel tool calls.
type NeoPiDevinCompat struct {
	ModelRouter               bool `json:"modelRouter"`
	SupportsParallelToolCalls bool `json:"supportsParallelToolCalls"`
}

// NeoPiThinking is the model's effort ladder (ThinkingConfig efforts/effortMap)
// and, for budget-based providers, how an effort reaches the wire.
type NeoPiThinking struct {
	Efforts   []string          `json:"efforts"`
	EffortMap map[string]string `json:"effortMap,omitempty"`
	// Mode is NeoPi's ThinkingConfig.mode ("budget", "anthropic-budget-effort", ...).
	Mode string `json:"mode,omitempty"`
	// RequiresEffort models always think: no effort or thinking-off sends the lowest effort.
	RequiresEffort  bool  `json:"requiresEffort,omitempty"`
	SupportsDisplay *bool `json:"supportsDisplay,omitempty"`
	// EffortRouting maps an effort (or "off") to the wire id that serves it.
	EffortRouting map[string]string `json:"effortRouting,omitempty"`
	DefaultLevel  string            `json:"defaultLevel,omitempty"`
	// PrefixBinding is ThinkingConfig.prefixBinding.
	PrefixBinding bool `json:"prefixBinding,omitempty"`
}

// NeoPiAnthropicCompat is the resolved compat NeoPi's Anthropic Messages
// provider reads for a non-Anthropic model (ResolvedAnthropicCompat).
type NeoPiAnthropicCompat struct {
	OfficialEndpoint                   bool `json:"officialEndpoint"`
	FirstPartyProvider                 bool `json:"firstPartyProvider"`
	InjectClaudeCodeInstruction        bool `json:"injectClaudeCodeInstruction"`
	ReplayUnsignedThinking             bool `json:"replayUnsignedThinking"`
	RequiresToolResultID               bool `json:"requiresToolResultId"`
	DisableStrictTools                 bool `json:"disableStrictTools"`
	SupportsContextManagement          bool `json:"supportsContextManagement"`
	SupportsOutputEffort               bool `json:"supportsOutputEffort"`
	SupportsSamplingParams             bool `json:"supportsSamplingParams"`
	SupportsForcedToolChoice           bool `json:"supportsForcedToolChoice"`
	SupportsEagerToolInputStreaming    bool `json:"supportsEagerToolInputStreaming"`
	SupportsLongCacheRetention         bool `json:"supportsLongCacheRetention"`
	SupportsTurnScopedSystem           bool `json:"supportsTurnScopedSystem"`
	SupportsMidConversationToolChanges bool `json:"supportsMidConversationToolChanges"`
	SupportsPerMessageEffort           bool `json:"supportsPerMessageEffort"`
	SupportsThinkingBindingControls    bool `json:"supportsThinkingBindingControls"`
	SupportsBetweenToolsThinking       bool `json:"supportsBetweenToolsThinking"`
}

// NeoPiCompletionsCompat is the resolved compat NeoPi's OpenAI Completions
// provider reads (ResolvedOpenAICompat), under NeoPi's names.
type NeoPiCompletionsCompat struct {
	ThinkingFormat                              string            `json:"thinkingFormat"`
	MaxTokensField                              string            `json:"maxTokensField"`
	ReasoningContentField                       string            `json:"reasoningContentField"`
	ReasoningDisableMode                        string            `json:"reasoningDisableMode"`
	ReasoningEffortMap                          map[string]string `json:"reasoningEffortMap,omitempty"`
	StreamMarkupHealingPattern                  string            `json:"streamMarkupHealingPattern"`
	ToolStrictMode                              string            `json:"toolStrictMode"`
	WireModelIDMode                             string            `json:"wireModelIdMode"`
	AlwaysSendMaxTokens                         bool              `json:"alwaysSendMaxTokens"`
	ClampOutputToModelMax                       bool              `json:"clampOutputToModelMax"`
	SupportsDeveloperRole                       bool              `json:"supportsDeveloperRole"`
	SupportsStrictMode                          bool              `json:"supportsStrictMode"`
	SupportsStore                               bool              `json:"supportsStore"`
	SupportsUsageInStreaming                    bool              `json:"supportsUsageInStreaming"`
	SupportsSamplingParams                      bool              `json:"supportsSamplingParams"`
	SupportsPenaltyAndStopParams                bool              `json:"supportsPenaltyAndStopParams"`
	SupportsReasoningEffort                     bool              `json:"supportsReasoningEffort"`
	SupportsToolChoice                          bool              `json:"supportsToolChoice"`
	SupportsForcedToolChoice                    bool              `json:"supportsForcedToolChoice"`
	SupportsNamedToolChoice                     bool              `json:"supportsNamedToolChoice"`
	ZaiReasoningEffortDialect                   bool              `json:"zaiReasoningEffortDialect"`
	RequiresReasoningContentForToolCalls        bool              `json:"requiresReasoningContentForToolCalls"`
	AllowsSyntheticReasoningContentForToolCalls bool              `json:"allowsSyntheticReasoningContentForToolCalls"`
	ReplayReasoningContent                      bool              `json:"replayReasoningContent"`
}

// NeoPiCompat carries the resolved OpenAI compat fields NeoPi's Responses
// request builder reads (ResolvedOpenAIResponsesCompat), under NeoPi's names.
type NeoPiCompat struct {
	SupportsDeveloperRole                        bool              `json:"supportsDeveloperRole"`
	SupportsStrictMode                           bool              `json:"supportsStrictMode"`
	SupportsReasoningEffort                      bool              `json:"supportsReasoningEffort"`
	OmitReasoningEffort                          bool              `json:"omitReasoningEffort"`
	ReasoningEffortMap                           map[string]string `json:"reasoningEffortMap,omitempty"`
	ReasoningDisableMode                         string            `json:"reasoningDisableMode"`
	SupportsReasoningSummary                     bool              `json:"supportsReasoningSummary"`
	IncludeEncryptedReasoning                    bool              `json:"includeEncryptedReasoning"`
	FilterReasoningHistory                       bool              `json:"filterReasoningHistory"`
	RequiresReasoningContentForToolCalls         bool              `json:"requiresReasoningContentForToolCalls"`
	RequiresReasoningContentForAllAssistantTurns bool              `json:"requiresReasoningContentForAllAssistantTurns"`
	AllowsSyntheticReasoningContentForToolCalls  bool              `json:"allowsSyntheticReasoningContentForToolCalls"`
	DisableReasoningOnForcedToolChoice           bool              `json:"disableReasoningOnForcedToolChoice"`
	DisableReasoningOnToolChoice                 bool              `json:"disableReasoningOnToolChoice"`
	DisableReasoningWithTools                    bool              `json:"disableReasoningWithTools"`
	SupportsImageDetailOriginal                  bool              `json:"supportsImageDetailOriginal"`
	SupportsSamplingParams                       bool              `json:"supportsSamplingParams"`
	SupportsPenaltyAndStopParams                 bool              `json:"supportsPenaltyAndStopParams"`
	SupportsToolChoice                           bool              `json:"supportsToolChoice"`
	SupportsForcedToolChoice                     bool              `json:"supportsForcedToolChoice"`
	SupportsNamedToolChoice                      bool              `json:"supportsNamedToolChoice"`
	SupportsConfigurationUpdate                  bool              `json:"supportsConfigurationUpdate"`
	SupportsLongPromptCacheRetention             bool              `json:"supportsLongPromptCacheRetention"`
	SupportsObfuscationOptOut                    bool              `json:"supportsObfuscationOptOut"`
	StrictResponsesPairing                       bool              `json:"strictResponsesPairing"`
	StoreResponses                               bool              `json:"storeResponses"`
	OfficialEndpoint                             bool              `json:"officialEndpoint"`
	IsOpenRouterHost                             bool              `json:"isOpenRouterHost"`
	IsVercelGatewayHost                          bool              `json:"isVercelGatewayHost"`
	WireModelIDMode                              string            `json:"wireModelIdMode"`
	AlwaysSendMaxTokens                          bool              `json:"alwaysSendMaxTokens"`
	ClampOutputToModelMax                        bool              `json:"clampOutputToModelMax"`
	RejectRootObjectUnion                        bool              `json:"rejectRootObjectUnion"`
	PromptCacheSessionHeader                     string            `json:"promptCacheSessionHeader,omitempty"`
	// CacheControlFormat is "anthropic" for Anthropic models behind OpenRouter.
	CacheControlFormat string `json:"cacheControlFormat,omitempty"`
	// StripImageInput drops image parts for models that reject them.
	StripImageInput bool `json:"stripImageInput"`
	// ToolSchemaFlavor is "moonshot-mfjs" for Moonshot-family tool schemas.
	ToolSchemaFlavor string `json:"toolSchemaFlavor,omitempty"`
	// RetryWithoutStrictOnGrammarError retries once without strict tools on a grammar-size failure.
	RetryWithoutStrictOnGrammarError bool `json:"retryWithoutStrictOnGrammarError"`
}

// Clone deep-copies the record so registry readers cannot alias its maps.
func (m *NeoPiModel) Clone() *NeoPiModel {
	if m == nil {
		return nil
	}
	out := *m
	out.Input = slices.Clone(m.Input)
	out.Thinking = m.Thinking.Clone()
	if m.Compat != nil {
		compat := *m.Compat
		compat.ReasoningEffortMap = maps.Clone(m.Compat.ReasoningEffortMap)
		out.Compat = &compat
	}
	if m.AnthropicCompat != nil {
		compat := *m.AnthropicCompat
		out.AnthropicCompat = &compat
	}
	if m.CompletionsCompat != nil {
		compat := *m.CompletionsCompat
		compat.ReasoningEffortMap = maps.Clone(m.CompletionsCompat.ReasoningEffortMap)
		out.CompletionsCompat = &compat
	}
	if m.DevinCompat != nil {
		compat := *m.DevinCompat
		out.DevinCompat = &compat
	}
	return &out
}

// Clone deep-copies the effort ladder.
func (t *NeoPiThinking) Clone() *NeoPiThinking {
	if t == nil {
		return nil
	}
	out := *t
	out.Efforts = slices.Clone(t.Efforts)
	out.EffortMap = maps.Clone(t.EffortMap)
	out.EffortRouting = maps.Clone(t.EffortRouting)
	if t.SupportsDisplay != nil {
		display := *t.SupportsDisplay
		out.SupportsDisplay = &display
	}
	return &out
}

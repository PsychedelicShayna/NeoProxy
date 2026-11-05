package executor

import (
	_ "embed"
	"encoding/json"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
)

// Captured directly from NeoPi's bundled catalog and neutral discovery models.
// Unknown IDs use the same neutral discovery defaults plus reviewed taxonomy rules.
//
//go:embed testdata/anthropic_npi_profiles.json
var neoPiProfileJSON []byte

type neoPiProfile struct {
	Reasoning bool  `json:"reasoning"`
	MaxTokens int64 `json:"maxTokens"`
	Thinking  struct {
		Mode            string `json:"mode"`
		SupportsDisplay *bool  `json:"supportsDisplay"`
		PrefixBinding   bool   `json:"prefixBinding"`
	} `json:"thinking"`
	Compat struct {
		SupportsContextManagement          bool   `json:"supportsContextManagement"`
		SupportsEagerToolInputStreaming    bool   `json:"supportsEagerToolInputStreaming"`
		SupportsLongCacheRetention         bool   `json:"supportsLongCacheRetention"`
		SupportsTurnScopedSystem           bool   `json:"supportsTurnScopedSystem"`
		SupportsMidConversationToolChanges bool   `json:"supportsMidConversationToolChanges"`
		SupportsPerMessageEffort           bool   `json:"supportsPerMessageEffort"`
		SupportsThinkingBindingControls    bool   `json:"supportsThinkingBindingControls"`
		SupportsBetweenToolsThinking       bool   `json:"supportsBetweenToolsThinking"`
		SupportsForcedToolChoice           bool   `json:"supportsForcedToolChoice"`
		SupportsSamplingParams             bool   `json:"supportsSamplingParams"`
		InjectClaudeCodeInstruction        bool   `json:"injectClaudeCodeInstruction"`
		DisabledThinking                   string `json:"disabledThinking"`
	} `json:"compat"`
	// thirdParty is set only for NeoPi catalog rows served by another
	// provider over Anthropic Messages (Z.ai); Claude's own rows never carry it.
	thirdParty *neoPiThirdParty
}

// neoPiThirdParty is what NeoPi's Anthropic provider reads from a
// non-Anthropic model: its effort ladder and the compat facts that change the
// wire relative to Anthropic's own endpoint.
type neoPiThirdParty struct {
	efforts []string
	// effortMap is the model's thinking.effortMap (NeoPi's
	// mapEffortToAnthropicAdaptiveEffort reads it).
	effortMap      map[string]string
	requiresEffort bool
	outputEffort   bool
	toolResultID   bool
	images         bool
	// promptCacheUserID: NeoPi sends Kimi Code's prompt-cache key as
	// metadata.user_id when the caller sets none.
	promptCacheUserID bool
	// directOptions: the provider hands NeoPi's request options straight to the
	// Anthropic transport (Kimi), skipping streamSimple's adaptive effort mapping
	// and its thinking-budget raise of max_tokens.
	directOptions bool
	// headers are provider identity headers (Kimi's fingerprint) applied over
	// the default set.
	headers http.Header
}

var neoPiProfiles = func() map[string]neoPiProfile {
	var profiles map[string]neoPiProfile
	if err := json.Unmarshal(neoPiProfileJSON, &profiles); err != nil {
		panic(err)
	}
	return profiles
}()

var neoPiModelRevision = regexp.MustCompile(`(?i)claude-(opus|sonnet|haiku|fable|mythos)-([0-9]+)(?:[-.]([0-9]{1,2}))?(?:[^0-9]|$)`)
var neoPiModelDate = regexp.MustCompile(`-[0-9]{8}$`)

func neoPiResolveProfile(model string) neoPiProfile {
	if profile, ok := neoPiProfiles[model]; ok {
		return profile
	}
	canonical := model
	if info := registry.LookupModelInfo(model, "claude"); info != nil && info.MetadataModelID != "" {
		canonical = info.MetadataModelID
	}
	canonical = strings.ToLower(strings.TrimSpace(canonical))
	if index := strings.LastIndexByte(canonical, '/'); index >= 0 {
		canonical = canonical[index+1:]
	}
	if index := strings.IndexByte(canonical, '['); index >= 0 {
		canonical = canonical[:index]
	}
	canonical = neoPiModelDate.ReplaceAllString(canonical, "")
	if profile, ok := neoPiProfiles[canonical]; ok {
		return profile
	}
	// Dynamic /v1/models rows deliberately start non-reasoning and text-only.
	// A new model never inherits a bundled model's hidden reasoning contract.
	profile := neoPiProfiles["claude-unfamiliar"]
	match := neoPiModelRevision.FindStringSubmatch(canonical)
	if match == nil {
		return profile
	}
	revision, _ := strconv.ParseFloat(match[2]+"."+match[3], 64)
	family := match[1]
	compat := &profile.Compat
	if revision >= 4.7 && revision < 10 && family == "opus" || revision >= 5 && revision < 10 && (family == "sonnet" || family == "fable" || family == "mythos") || revision >= 5.5 && revision < 10 && family == "haiku" {
		compat.SupportsSamplingParams = false
	}
	if family == "opus" && revision >= 4.8 || family == "sonnet" && revision >= 5.5 || (family == "fable" || family == "mythos") && revision >= 5 || family == "haiku" && revision >= 5.5 && revision < 6 {
		compat.SupportsTurnScopedSystem = true
		compat.SupportsMidConversationToolChanges = true
	}
	compat.SupportsBetweenToolsThinking = family == "sonnet" && revision >= 5.5 && revision < 6
	compat.SupportsPerMessageEffort = family == "opus" && revision >= 5 || family == "sonnet" && revision >= 5.5 || (family == "fable" || family == "mythos") && revision >= 5.1 || family == "haiku" && revision >= 5.5 && revision < 6
	compat.SupportsThinkingBindingControls = family == "fable" && revision >= 5.1 || family == "sonnet" && revision >= 5.5 || (family == "opus" || family == "haiku") && revision >= 5.5 && revision < 6
	compat.SupportsForcedToolChoice = !(family == "fable" || family == "mythos" || (family == "opus" || family == "sonnet") && revision >= 5.5 && revision < 6)
	if family == "fable" || family == "mythos" {
		profile.MaxTokens = 128000
	}
	if family == "haiku" && revision >= 5.5 && revision < 6 {
		return neoPiProfiles["claude-haiku-5-5"]
	}
	return profile
}

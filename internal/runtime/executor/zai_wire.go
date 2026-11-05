package executor

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/jsjson"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/openaiwire"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/tidwall/gjson"
)

// neoPiEffortBudgets is NeoPi's ANTHROPIC_THINKING: the thinking budget a
// budget-mode Anthropic Messages model receives for each effort.
var neoPiEffortBudgets = map[string]int64{"minimal": 1024, "low": 4096, "medium": 8192, "high": 16384, "xhigh": 32768, "max": 32768}

const (
	neoPiNonVisionImagePlaceholder = "[image omitted: model does not support vision]"
	neoPiEmptyErrorToolResult      = "Tool failed with no output."
	neoPiImageOnlyPrefix           = "(see attached image)"
)

// neoPiThirdPartyProfile is NeoPi's resolved Anthropic Messages model for a
// non-Anthropic catalog row.
func neoPiThirdPartyProfile(model *registry.NeoPiModel) neoPiProfile {
	var profile neoPiProfile
	profile.Reasoning = model.Reasoning
	profile.MaxTokens = int64(model.MaxTokens)
	party := &neoPiThirdParty{images: false}
	for _, input := range model.Input {
		party.images = party.images || input == "image"
	}
	if thinking := model.Thinking; thinking != nil {
		profile.Thinking.Mode = thinking.Mode
		profile.Thinking.SupportsDisplay = thinking.SupportsDisplay
		party.efforts = append([]string(nil), thinking.Efforts...)
		party.effortMap = thinking.EffortMap
		if thinking.Mode == "anthropic-adaptive" && len(thinking.EffortMap) > 0 {
			party.tagOnly = true
			for _, mapped := range thinking.EffortMap {
				party.tagOnly = party.tagOnly && mapped == "adaptive"
			}
		}
		party.requiresEffort = thinking.RequiresEffort
	}
	// NeoPi keys this on the provider: Kimi shares its affinity identity
	// across its two transports.
	party.promptCacheUserID = model.Provider == "kimi-code"
	party.directOptions = model.Provider == "kimi-code"
	if compat := model.AnthropicCompat; compat != nil {
		profile.Compat.SupportsContextManagement = compat.SupportsContextManagement
		profile.Compat.SupportsEagerToolInputStreaming = compat.SupportsEagerToolInputStreaming
		profile.Compat.SupportsLongCacheRetention = compat.SupportsLongCacheRetention
		profile.Compat.SupportsTurnScopedSystem = compat.SupportsTurnScopedSystem
		profile.Compat.SupportsMidConversationToolChanges = compat.SupportsMidConversationToolChanges
		profile.Compat.SupportsPerMessageEffort = compat.SupportsPerMessageEffort
		profile.Compat.SupportsThinkingBindingControls = compat.SupportsThinkingBindingControls
		profile.Compat.SupportsBetweenToolsThinking = compat.SupportsBetweenToolsThinking
		profile.Compat.SupportsForcedToolChoice = compat.SupportsForcedToolChoice
		profile.Compat.SupportsSamplingParams = compat.SupportsSamplingParams
		profile.Compat.InjectClaudeCodeInstruction = compat.InjectClaudeCodeInstruction
		party.outputEffort = compat.SupportsOutputEffort
		party.toolResultID = compat.RequiresToolResultID
	}
	profile.thirdParty = party
	return profile
}

// neoPiThirdPartyHeaders is buildAnthropicHeaders for an API key on a
// non-Anthropic endpoint: Bearer auth (applied later), NeoPi's own user agent
// and only the betas the request's fields need.
func neoPiThirdPartyHeaders(body []byte, session string, thinking bool, profile neoPiProfile) http.Header {
	var betas []string
	// NeoPi attaches the beta whenever the request carries an output effort:
	// requested thinking, or the pinned lowest effort of an adaptive-only model.
	if profile.Reasoning && profile.thirdParty.outputEffort && !profile.thirdParty.tagOnly && (thinking || gjson.GetBytes(body, "output_config.effort").Exists()) {
		betas = append(betas, "effort-2025-11-24")
	}
	if profile.Thinking.SupportsDisplay == nil || !*profile.Thinking.SupportsDisplay {
		betas = append(betas, "interleaved-thinking-2025-05-14")
	}
	for _, edit := range gjson.GetBytes(body, "context_management.edits").Array() {
		if edit.Get("type").String() == "clear_thinking_20251015" {
			betas = append(betas, "context-management-2025-06-27")
			break
		}
	}
	h := make(http.Header)
	h.Set("Accept", "text/event-stream")
	h.Set("Accept-Encoding", "gzip, deflate, br, zstd")
	h.Set("Connection", "keep-alive")
	h.Set("Content-Type", "application/json")
	if len(betas) > 0 {
		h.Set("anthropic-beta", strings.Join(betas, ","))
	}
	h.Set("anthropic-dangerous-direct-browser-access", "true")
	h.Set("anthropic-version", "2023-06-01")
	h.Set("User-Agent", openaiwire.NeoPiUserAgent)
	h.Set("x-app", "cli")
	if session != "" {
		h.Set("X-Claude-Code-Session-Id", session)
	}
	for name, values := range profile.thirdParty.headers {
		h[name] = append([]string(nil), values...)
	}
	return h
}

// neoPiThirdPartyMessages applies NeoPi's convertMessages content rules for a
// non-Anthropic model to downstream Messages turns: blank user turns drop,
// user and tool_result content is rebuilt for the model's input modalities,
// and a tool_result carries the `id` alias the provider requires.
func neoPiThirdPartyMessages(input []byte, party *neoPiThirdParty) []byte {
	root := gjson.ParseBytes(input)
	messages := root.Get("messages")
	if !messages.IsArray() {
		return input
	}
	out := make([]json.RawMessage, 0, len(messages.Array()))
	for _, message := range messages.Array() {
		if message.Get("role").String() != "user" {
			out = append(out, json.RawMessage(message.Raw))
			continue
		}
		content := message.Get("content")
		if content.Type == gjson.String {
			if strings.TrimSpace(content.String()) == "" {
				continue
			}
			out = append(out, json.RawMessage(message.Raw))
			continue
		}
		var blocks []json.RawMessage
		var plain []gjson.Result
		hasToolResult := false
		for _, block := range content.Array() {
			if block.Get("type").String() == "tool_result" {
				hasToolResult = true
				blocks = append(blocks, neoPiThirdPartyToolResult(block, party))
				continue
			}
			plain = append(plain, block)
		}
		raw := []byte(message.Raw)
		if !hasToolResult {
			converted, text := neoPiThirdPartyContent(plain, party.images)
			if converted == nil {
				if strings.TrimSpace(text) == "" {
					continue
				}
				out = append(out, neoPiSet(raw, "content", text))
				continue
			}
			if len(converted) == 0 {
				continue
			}
			out = append(out, neoPiSetRaw(raw, "content", jsjson.Marshal(converted)))
			continue
		}
		if len(plain) > 0 {
			converted, text := neoPiThirdPartyContent(plain, party.images)
			if converted == nil && strings.TrimSpace(text) != "" {
				blocks = append(blocks, neoPiTextBlock(text))
			}
			blocks = append(blocks, converted...)
		}
		out = append(out, neoPiSetRaw(raw, "content", jsjson.Marshal(blocks)))
	}
	return neoPiSetRaw(input, "messages", jsjson.Marshal(out))
}

// neoPiThirdPartyContent is convertContentBlocks: a text-only model gets one
// string (nil blocks), a vision model gets text and image blocks.
func neoPiThirdPartyContent(content []gjson.Result, images bool) ([]json.RawMessage, string) {
	blocks := []json.RawMessage{}
	var texts []string
	sawText, sawImage := false, false
	for _, block := range content {
		switch block.Get("type").String() {
		case "text":
			text := block.Get("text").String()
			if strings.TrimSpace(text) == "" {
				continue
			}
			sawText = true
			texts = append(texts, text)
			blocks = append(blocks, neoPiTextBlock(text))
		case "image":
			if !images {
				texts = append(texts, neoPiNonVisionImagePlaceholder)
				blocks = append(blocks, neoPiTextBlock(neoPiNonVisionImagePlaceholder))
				continue
			}
			sawImage = true
			blocks = append(blocks, jsjson.Object(jsjson.Field{Name: "type", Value: jsjson.Marshal("image")}, jsjson.Field{Name: "source", Value: []byte(block.Get("source").Raw)}))
		default:
			blocks = append(blocks, json.RawMessage(block.Raw))
		}
	}
	if !images {
		return nil, strings.Join(texts, "\n")
	}
	if sawImage && !sawText {
		blocks = append([]json.RawMessage{neoPiTextBlock(neoPiImageOnlyPrefix)}, blocks...)
	}
	return blocks, ""
}

// neoPiThirdPartyToolResult is buildToolResultBlock.
func neoPiThirdPartyToolResult(block gjson.Result, party *neoPiThirdParty) json.RawMessage {
	content := block.Get("content")
	parts := content.Array()
	if content.Type == gjson.String {
		parts = []gjson.Result{gjson.Parse(string(neoPiTextBlock(content.String())))}
	}
	converted, text := neoPiThirdPartyContent(parts, party.images)
	isError := block.Get("is_error").Bool()
	var wire []byte
	if converted == nil {
		if isError && strings.TrimSpace(text) == "" {
			text = neoPiEmptyErrorToolResult
		}
		wire = jsjson.Marshal(text)
	} else {
		switch {
		// An empty array becomes "" first; strict endpoints reject [] (code 1213).
		case len(converted) == 0 && isError:
			wire = jsjson.Marshal(neoPiEmptyErrorToolResult)
		case len(converted) == 0:
			wire = jsjson.Marshal("")
		default:
			wire = jsjson.Marshal(converted)
		}
	}
	id := block.Get("tool_use_id").String()
	fields := []jsjson.Field{{Name: "type", Value: jsjson.Marshal("tool_result")}, {Name: "tool_use_id", Value: jsjson.Marshal(id)}, {Name: "content", Value: wire}, {Name: "is_error", Value: jsjson.Marshal(isError)}}
	if party.toolResultID {
		fields = append(fields, jsjson.Field{Name: "id", Value: jsjson.Marshal(id)})
	}
	return jsjson.Object(fields...)
}

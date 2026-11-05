package executor

import (
	"encoding/json"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/antigravitywire"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/codexwire"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/googlewire"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/jsjson"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/tidwall/gjson"
)

// NeoPi's Antigravity provider receives NeoPi messages, not wire bodies: the
// request arrives here as Anthropic Messages (the dialect every downstream
// format translates to), becomes NeoPi's history with every assistant turn
// treated as the model's own, and is shaped by buildRequest with
// convertMessages/convertTools from google-shared.

// antigravitySystemPrompts is normalizeSystemPrompts over the Claude body's
// system field, which the google-gemini-cli request builder then re-normalizes.
func antigravitySystemPrompts(body gjson.Result) []string {
	var prompts []string
	system := body.Get("system")
	switch {
	case system.Type == gjson.String:
		prompts = append(prompts, system.Str)
	case system.IsArray():
		for _, block := range system.Array() {
			if block.Get("type").String() == "text" {
				prompts = append(prompts, block.Get("text").String())
			}
		}
	}
	kept := prompts[:0]
	for _, prompt := range prompts {
		if strings.TrimSpace(prompt) != "" {
			kept = append(kept, prompt)
		}
	}
	return kept
}

// antigravityImage is one Claude image block as NeoPi's ImageContent.
func antigravityImage(block gjson.Result) (googlewire.Block, bool) {
	source := block.Get("source")
	switch source.Get("type").String() {
	case "base64":
		return googlewire.Block{Type: googlewire.BlockImage, MimeType: source.Get("media_type").String(), Data: source.Get("data").String()}, true
	case "url":
		return googlewire.Block{Type: googlewire.BlockImage, MimeType: block.Get("source.media_type").String(), URL: source.Get("url").String()}, true
	}
	return googlewire.Block{}, false
}

// antigravityBlocks maps one Claude content block array onto NeoPi blocks.
func antigravityBlocks(content gjson.Result) []googlewire.Block {
	if content.Type == gjson.String {
		return []googlewire.Block{{Type: googlewire.BlockText, Text: content.Str}}
	}
	var blocks []googlewire.Block
	for _, block := range content.Array() {
		switch block.Get("type").String() {
		case "text":
			blocks = append(blocks, googlewire.Block{Type: googlewire.BlockText, Text: block.Get("text").String(), Signature: block.Get("textSignature").String()})
		case "image":
			if image, ok := antigravityImage(block); ok {
				blocks = append(blocks, image)
			}
		case "thinking":
			blocks = append(blocks, googlewire.Block{Type: googlewire.BlockThinking, Text: block.Get("thinking").String(),
				Signature: block.Get("signature").String()})
		case "tool_use":
			arguments := json.RawMessage("{}")
			if input := block.Get("input"); input.Exists() && input.Type != gjson.Null {
				arguments = jsjson.Normalize([]byte(input.Raw))
			}
			blocks = append(blocks, googlewire.Block{Type: googlewire.BlockToolCall, ID: block.Get("id").String(),
				Name: block.Get("name").String(), Arguments: arguments, Signature: block.Get("thoughtSignature").String()})
		}
	}
	return blocks
}

// antigravityContext is the Claude body as NeoPi's Context: user and assistant
// turns in order, tool_result blocks as their own toolResult messages, and the
// tools as NeoPi Tool definitions.
func antigravityContext(body gjson.Result, model *registry.NeoPiModel) googlewire.Context {
	ctx := googlewire.Context{SystemPrompts: antigravitySystemPrompts(body)}
	toolNames := map[string]string{}
	for _, message := range body.Get("messages").Array() {
		content := message.Get("content")
		if message.Get("role").String() == "assistant" {
			blocks := antigravityBlocks(content)
			for _, block := range blocks {
				if block.Type == googlewire.BlockToolCall {
					toolNames[block.ID] = block.Name
				}
			}
			ctx.Messages = append(ctx.Messages, googlewire.Message{Role: googlewire.RoleAssistant, Content: blocks,
				Provider: model.Provider, API: model.API, Model: model.ID, StopReason: assistantStopReason(blocks)})
			continue
		}
		if content.Type == gjson.String {
			ctx.Messages = append(ctx.Messages, googlewire.Message{Role: googlewire.RoleUser, Text: content.Str, IsString: true})
			continue
		}
		var pending []googlewire.Block
		flush := func() {
			if len(pending) > 0 {
				ctx.Messages = append(ctx.Messages, googlewire.Message{Role: googlewire.RoleUser, Content: pending})
				pending = nil
			}
		}
		for _, block := range content.Array() {
			if block.Get("type").String() != "tool_result" {
				pending = append(pending, antigravityBlocks(gjson.Parse("["+block.Raw+"]"))...)
				continue
			}
			flush()
			id := block.Get("tool_use_id").String()
			ctx.Messages = append(ctx.Messages, googlewire.Message{Role: googlewire.RoleToolResult, ToolCallID: id,
				ToolName: toolNames[id], IsError: block.Get("is_error").Bool(),
				Content: antigravityBlocks(block.Get("content"))})
		}
		flush()
		if len(content.Array()) == 0 {
			ctx.Messages = append(ctx.Messages, googlewire.Message{Role: googlewire.RoleUser})
		}
	}
	for _, tool := range body.Get("tools").Array() {
		parameters := []byte(tool.Get("input_schema").Raw)
		if len(parameters) == 0 {
			parameters = []byte(`{"type":"object","properties":{}}`)
		}
		ctx.Tools = append(ctx.Tools, googlewire.Tool{Name: tool.Get("name").String(),
			Description: tool.Get("description").String(), Parameters: parameters})
	}
	return ctx
}

// antigravityPrepareImages applies the model's declared image contract before
// google-shared converts user and tool-result blocks to the wire format.
func antigravityPrepareImages(ctx *googlewire.Context, model *registry.NeoPiModel) {
	compat := model.GeminiCompat
	if compat == nil || (compat.MaxImageDimension == 0 && compat.MaxImagePayloadBytes == 0) {
		return
	}
	count := 0
	for i := range ctx.Messages {
		message := &ctx.Messages[i]
		if message.Role != googlewire.RoleUser && message.Role != googlewire.RoleDeveloper && message.Role != googlewire.RoleToolResult {
			continue
		}
		for j := range message.Content {
			if message.Content[j].Type == googlewire.BlockImage {
				count++
			}
		}
	}
	if count == 0 {
		return
	}
	caps := antigravitywire.CapsForCount(count, compat.MaxImageDimension, compat.MaxImagePayloadBytes)
	for i := range ctx.Messages {
		message := &ctx.Messages[i]
		if message.Role != googlewire.RoleUser && message.Role != googlewire.RoleDeveloper && message.Role != googlewire.RoleToolResult {
			continue
		}
		for j := range message.Content {
			block := &message.Content[j]
			if block.Type != googlewire.BlockImage {
				continue
			}
			image := antigravitywire.Image{Data: block.Data, MimeType: block.MimeType, URL: block.URL, ProviderFile: block.ProviderFileURI}
			if antigravitywire.ClampImages([]*antigravitywire.Image{&image}, caps) != 0 {
				block.Data, block.MimeType, block.URL, block.ProviderFileURI = image.Data, image.MimeType, image.URL, ""
			}
		}
	}
}

// assistantStopReason marks a replayed assistant turn that ended in a tool
// call, which transformMessages reads as its own.
func assistantStopReason(blocks []googlewire.Block) string {
	for _, block := range blocks {
		if block.Type == googlewire.BlockToolCall {
			return "toolUse"
		}
	}
	return "stop"
}

// antigravityGoogleModel is NeoPi's built Model as googlewire reads it.
func antigravityGoogleModel(model *registry.NeoPiModel) *googlewire.Model {
	compat := model.GeminiCompat
	if compat == nil {
		compat = &registry.NeoPiGeminiCompat{}
	}
	google := &googlewire.Model{ID: model.ID, Provider: model.Provider, API: model.API, Input: model.Input,
		Reasoning: model.Reasoning, MaxTokens: model.MaxTokens, NameMaxLength: 64,
		Compat: googlewire.Compat{
			MultimodalFunctionResponse:                      compat.MultimodalFunctionResponse,
			RequiresSkipThoughtSignature:                    compat.RequiresSkipThoughtSignature,
			RequiresSkipThoughtSignatureOnFirstFunctionCall: compat.RequiresSkipThoughtSignatureOnFirstFunctionCall,
			SupportsFunctionPartID:                          compat.SupportsFunctionPartID,
			DropUnsignedThinking:                            compat.DropUnsignedThinking,
			CCALegacyParametersSchema:                       compat.CCALegacyParametersSchema,
		}}
	if model.Thinking != nil {
		google.Thinking = &googlewire.Thinking{Efforts: model.Thinking.Efforts, Mode: model.Thinking.Mode,
			RequiresEffort: model.Thinking.RequiresEffort, SuppressWhenOff: model.Thinking.SuppressWhenOff,
			EffortBudgets: model.Thinking.EffortBudgets}
	}
	return google
}

// antigravityTools is convertTools followed by normalizeAntigravityTools: on
// this wire every declaration carries `parameters`, the Cloud Code Assist
// schema form. Claude models are converted straight from the tool wire schema;
// Gemini models go through normalizeSchemaForGoogle first, as NeoPi's
// parametersJsonSchema does, and both land on normalizeSchemaForCCA.
func antigravityTools(model *registry.NeoPiModel, tools []googlewire.Tool) json.RawMessage {
	if len(tools) == 0 {
		return nil
	}
	legacy := model.GeminiCompat != nil && model.GeminiCompat.CCALegacyParametersSchema
	declarations := make([]json.RawMessage, len(tools))
	for i, tool := range tools {
		wire := codexwire.ToolWireSchema(gjson.ParseBytes(tool.Parameters))
		schema := codexwire.NormalizeSchemaForCCA(wire)
		if !legacy {
			schema = codexwire.NormalizeSchemaForGoogleThenCCA(wire)
		}
		declarations[i] = jsjson.Object(
			jsjson.Field{Name: "name", Value: jsjson.Marshal(tool.Name)},
			jsjson.Field{Name: "description", Value: jsjson.Marshal(tool.Description)},
			jsjson.Field{Name: "parameters", Value: schema},
		)
	}
	return jsjson.Marshal([]json.RawMessage{jsjson.Object(jsjson.Field{Name: "functionDeclarations", Value: jsjson.Marshal(declarations)})})
}

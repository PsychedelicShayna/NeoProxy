package googlewire

import (
	"encoding/json"
	"net/url"
	"path"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/codexwire"
	"github.com/tidwall/gjson"
)

// foreignProvider names the origin of assistant turns whose Google
// provenance cannot be shown; NeoPi treats those as another model's.
const foreignProvider = "openai-compat"

// thoughtSignaturePaths are where an OpenAI Chat tool call carries the thought
// signature NeoProxy handed out with the call.
var thoughtSignaturePaths = []string{
	"extra_content.google.thought_signature",
	"function.extra_content.google.thought_signature",
	"thoughtSignature",
	"thought_signature",
}

func dataURLImage(raw string) (mime, data, uri string) {
	if strings.HasPrefix(raw, "data:") {
		header, payload, ok := strings.Cut(raw[len("data:"):], ",")
		if ok {
			mime, _, _ = strings.Cut(header, ";")
			if mime == "" {
				mime = "text/plain"
			}
			return mime, payload, ""
		}
	}
	mime = "image/jpeg"
	if parsed, err := url.Parse(raw); err == nil {
		switch strings.ToLower(path.Ext(parsed.Path)) {
		case ".png":
			mime = "image/png"
		case ".gif":
			mime = "image/gif"
		case ".webp":
			mime = "image/webp"
		case ".heic":
			mime = "image/heic"
		case ".heif":
			mime = "image/heif"
		}
	}
	return mime, "", raw
}

func openAIBlocks(content gjson.Result) []Block {
	var blocks []Block
	for _, item := range content.Array() {
		switch item.Get("type").String() {
		case "text", "input_text", "output_text":
			blocks = append(blocks, Block{Type: BlockText, Text: item.Get("text").String()})
		case "image_url", "input_image":
			raw := item.Get("image_url.url").String()
			if raw == "" {
				raw = item.Get("image_url").String()
			}
			if raw == "" {
				continue
			}
			mime, data, uri := dataURLImage(raw)
			blocks = append(blocks, Block{Type: BlockImage, MimeType: mime, Data: data, URL: uri})
		}
	}
	return blocks
}

func joinedText(content gjson.Result) string {
	if content.Type == gjson.String {
		return content.String()
	}
	var texts []string
	for _, block := range openAIBlocks(content) {
		if block.Type == BlockText {
			texts = append(texts, block.Text)
		}
	}
	return strings.Join(texts, "\n")
}

// FromOpenAIChat reads the OpenAI Chat Completions body a client sent
// NeoProxy (after thinking and payload translation) as the NeoPi context and
// SimpleStreamOptions a NeoPi agent would have used for it.
//
// An assistant turn that carries a Google thought signature on a tool call is
// the model's own (its signature is replayed); every other assistant turn is
// foreign, so its signatures are dropped and its thinking is demoted to text,
// exactly as NeoPi treats history from another model.
func FromOpenAIChat(body []byte, model *Model) (Context, Options, error) {
	root := gjson.ParseBytes(body)
	var ctx Context
	toolNames := map[string]string{}
	for _, msg := range root.Get("messages").Array() {
		switch role := msg.Get("role").String(); role {
		case "system", "developer":
			ctx.SystemPrompts = append(ctx.SystemPrompts, joinedText(msg.Get("content")))
		case "user":
			content := msg.Get("content")
			if content.Type == gjson.String {
				ctx.Messages = append(ctx.Messages, Message{Role: RoleUser, IsString: true, Text: content.String()})
			} else {
				ctx.Messages = append(ctx.Messages, Message{Role: RoleUser, Content: openAIBlocks(content)})
			}
		case "assistant":
			out := Message{Role: RoleAssistant, Provider: foreignProvider, API: model.API, Model: model.ID, StopReason: "stop"}
			for _, name := range []string{"reasoning_content", "reasoning"} {
				if reasoning := msg.Get(name); reasoning.Type == gjson.String && reasoning.String() != "" {
					out.Content = append(out.Content, Block{Type: BlockThinking, Text: reasoning.String()})
					break
				}
			}
			if text := joinedText(msg.Get("content")); text != "" {
				out.Content = append(out.Content, Block{Type: BlockText, Text: text})
			}
			for _, call := range msg.Get("tool_calls").Array() {
				block := Block{Type: BlockToolCall, ID: call.Get("id").String(), Name: call.Get("function.name").String(), Arguments: json.RawMessage("{}")}
				arguments := call.Get("function.arguments")
				raw := arguments.Raw
				if arguments.Type == gjson.String {
					raw = arguments.String()
				}
				if gjson.Valid(raw) && gjson.Parse(raw).IsObject() {
					block.Arguments = json.RawMessage(raw)
				}
				for _, p := range thoughtSignaturePaths {
					if signature := call.Get(p); signature.Exists() && signature.String() != "" {
						value := signature.String()
						if model.NormalizeSignature != nil {
							var ok bool
							if value, ok = model.NormalizeSignature(value); !ok {
								continue
							}
						}
						block.Signature = value
						out.Provider = model.Provider
						break
					}
				}
				toolNames[block.ID] = block.Name
				out.StopReason = "toolUse"
				out.Content = append(out.Content, block)
			}
			ctx.Messages = append(ctx.Messages, out)
		case "tool":
			id := msg.Get("tool_call_id").String()
			ctx.Messages = append(ctx.Messages, Message{Role: RoleToolResult, ToolCallID: id, ToolName: toolNames[id], Content: toolResultBlocks(msg.Get("content"))})
		}
	}
	for _, tool := range root.Get("tools").Array() {
		if tool.Get("type").String() != "function" {
			continue
		}
		function := tool.Get("function")
		parameters := json.RawMessage(function.Get("parameters").Raw)
		if len(parameters) == 0 {
			parameters = json.RawMessage(`{"type":"object","properties":{}}`)
		}
		ctx.Tools = append(ctx.Tools, Tool{Name: function.Get("name").String(), Description: function.Get("description").String(), Parameters: parameters})
	}

	var opts Options
	float := func(name string) *float64 {
		if value := root.Get(name); value.Type == gjson.Number {
			number := value.Float()
			return &number
		}
		return nil
	}
	opts.Temperature, opts.TopP, opts.TopK, opts.PresencePenalty = float("temperature"), float("top_p"), float("top_k"), float("presence_penalty")
	for _, name := range []string{"max_completion_tokens", "max_tokens"} {
		if value := root.Get(name).Int(); value > 0 {
			capped := int(value)
			opts.MaxTokens = &capped
			break
		}
	}
	switch effort := strings.ToLower(strings.TrimSpace(root.Get("reasoning_effort").String())); effort {
	case "":
	case "none", "off":
		opts.DisableReasoning = true
	default:
		var levels []string
		if model.Thinking != nil {
			levels = model.Thinking.Efforts
		}
		if model.Reasoning && len(levels) > 0 {
			opts.Reasoning = codexwire.ClampEffort(levels, effort)
		} else if model.Reasoning {
			opts.Reasoning = effort
		}
	}
	switch choice := root.Get("tool_choice"); {
	case choice.Type == gjson.String:
		switch choice.String() {
		case "required", "any":
			opts.ToolChoice = ToolChoice{Mode: "any"}
		case "auto", "none":
			opts.ToolChoice = ToolChoice{Mode: choice.String()}
		}
	case choice.IsObject():
		if name := choice.Get("function.name").String(); name != "" {
			opts.ToolChoice = ToolChoice{Mode: "named", Name: name}
		} else if name := choice.Get("name").String(); name != "" {
			opts.ToolChoice = ToolChoice{Mode: "named", Name: name}
		}
	}
	opts.ServiceTier = root.Get("service_tier").String()
	return ctx, opts, nil
}

func toolResultBlocks(content gjson.Result) []Block {
	if content.Type == gjson.String {
		return []Block{{Type: BlockText, Text: content.String()}}
	}
	return openAIBlocks(content)
}

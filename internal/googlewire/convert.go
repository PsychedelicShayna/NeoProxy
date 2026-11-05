package googlewire

import (
	"encoding/json"
	"regexp"
	"sort"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/codexwire"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/jsjson"
	"github.com/tidwall/gjson"
)

// NonVisionImagePlaceholder is NON_VISION_IMAGE_PLACEHOLDER.
const NonVisionImagePlaceholder = "[image omitted: model does not support vision]"

var base64SignaturePattern = regexp.MustCompile(`^[A-Za-z0-9+/]+={0,2}$`)

func isValidThoughtSignature(signature string) bool {
	if signature == "" || len(signature)%4 != 0 {
		return false
	}
	return base64SignaturePattern.MatchString(signature)
}

// resolveThoughtSignature keeps a signature only from the same provider and
// model, and only when it is valid base64.
func resolveThoughtSignature(isSame bool, signature string) string {
	if isSame && isValidThoughtSignature(signature) {
		return signature
	}
	return ""
}

func field(name string, value json.RawMessage) jsjson.Field {
	return jsjson.Field{Name: name, Value: value}
}

func str(value string) json.RawMessage { return jsjson.Marshal(value) }

func imagePart(block Block) json.RawMessage {
	switch {
	case block.ProviderFileURI != "":
		return jsjson.Object(field("fileData", jsjson.Object(field("fileUri", str(block.ProviderFileURI)), field("mimeType", str(block.MimeType)))))
	case block.URL != "":
		return jsjson.Object(field("fileData", jsjson.Object(field("fileUri", str(block.URL)), field("mimeType", str(block.MimeType)))))
	}
	return jsjson.Object(field("inlineData", jsjson.Object(field("mimeType", str(block.MimeType)), field("data", str(block.Data)))))
}

type part struct {
	raw              json.RawMessage
	functionResponse bool
	// ordinal is the call ordinal of the call a functionResponse answers.
	ordinal    int
	hasOrdinal bool
}

type content struct {
	role  string
	parts []*part
}

// sortKnownInPlace sorts the items whose key is known into key order within
// the slots they occupy; others stay put.
func sortKnownInPlace[T any](items []T, keyOf func(T) (int, bool)) {
	type entry struct {
		position, key int
		item          T
	}
	var known []entry
	for position, item := range items {
		if key, ok := keyOf(item); ok {
			known = append(known, entry{position, key, item})
		}
	}
	if len(known) < 2 {
		return
	}
	ordered := make([]entry, len(known))
	copy(ordered, known)
	sort.SliceStable(ordered, func(i, j int) bool { return ordered[i].key < ordered[j].key })
	for i, slot := range known {
		items[slot.position] = ordered[i].item
	}
}

func partsJSON(parts []*part) json.RawMessage {
	raws := make([]json.RawMessage, len(parts))
	for i, p := range parts {
		raws[i] = p.raw
	}
	return jsjson.Marshal(raws)
}

// ConvertMessages is convertMessages: NeoPi messages to Gemini Content[].
func ConvertMessages(model *Model, messages []Message) []json.RawMessage {
	var contents []*content
	emittedNames := map[string]string{}
	ordinals := map[string]int{}
	nextOrdinal := 0

	type pendingImages struct {
		ordinal    int
		hasOrdinal bool
		parts      []*part
	}
	var pending []pendingImages
	flush := func() {
		if len(pending) == 0 {
			return
		}
		sortKnownInPlace(pending, func(g pendingImages) (int, bool) { return g.ordinal, g.hasOrdinal })
		var parts []*part
		for _, group := range pending {
			parts = append(parts, group.parts...)
		}
		contents = append(contents, &content{role: "user", parts: parts})
		pending = nil
	}

	supportsImages := model.supportsImages()
	for _, msg := range TransformMessages(messages, model) {
		if msg.Role != RoleToolResult {
			flush()
		}
		switch msg.Role {
		case RoleUser, RoleDeveloper:
			if msg.IsString {
				if strings.TrimSpace(msg.Text) == "" {
					continue
				}
				contents = append(contents, &content{role: "user", parts: []*part{{raw: jsjson.Object(field("text", str(toWellFormed(msg.Text))))}}})
				continue
			}
			var parts []*part
			omitted := false
			for _, item := range msg.Content {
				switch {
				case item.Type == BlockText:
					text := toWellFormed(item.Text)
					if strings.TrimSpace(text) == "" {
						continue
					}
					parts = append(parts, &part{raw: jsjson.Object(field("text", str(text)))})
				case supportsImages:
					parts = append(parts, &part{raw: imagePart(item)})
				default:
					omitted = true
				}
			}
			if omitted {
				parts = append(parts, &part{raw: jsjson.Object(field("text", str(NonVisionImagePlaceholder)))})
			}
			if len(parts) == 0 {
				continue
			}
			contents = append(contents, &content{role: "user", parts: parts})
		case RoleAssistant:
			var parts []*part
			isSame := msg.Provider == model.Provider && msg.Model == model.ID
			firstCall := true
			for _, block := range msg.Content {
				switch block.Type {
				case BlockText:
					if strings.TrimSpace(block.Text) == "" {
						continue
					}
					signature := resolveThoughtSignature(isSame, block.Signature)
					fields := []jsjson.Field{field("text", str(toWellFormed(block.Text)))}
					if signature != "" {
						fields = append(fields, field("thoughtSignature", str(signature)))
					}
					parts = append(parts, &part{raw: jsjson.Object(fields...)})
				case BlockThinking:
					signature := resolveThoughtSignature(isSame, block.Signature)
					if strings.TrimSpace(block.Text) == "" && signature == "" {
						continue
					}
					if model.Compat.DropUnsignedThinking && signature == "" {
						continue
					}
					if isSame {
						fields := []jsjson.Field{field("thought", json.RawMessage("true")), field("text", str(toWellFormed(block.Text)))}
						if signature != "" {
							fields = append(fields, field("thoughtSignature", str(signature)))
						}
						parts = append(parts, &part{raw: jsjson.Object(fields...)})
					} else {
						parts = append(parts, &part{raw: jsjson.Object(field("text", str(RenderDemotedThinking(model.ID, block.Text))))})
					}
				case BlockToolCall:
					emittedNames[block.ID] = block.Name
					ordinals[block.ID] = nextOrdinal
					nextOrdinal++
					signature := resolveThoughtSignature(isSame, block.Signature)
					requiresFallback := model.Compat.RequiresSkipThoughtSignature || firstCall && model.Compat.RequiresSkipThoughtSignatureOnFirstFunctionCall
					if signature == "" && requiresFallback {
						signature = SkipThoughtSignature
					}
					firstCall = false
					args := json.RawMessage("{}")
					if len(block.Arguments) > 0 && gjson.ValidBytes(block.Arguments) {
						args = jsjson.Normalize(block.Arguments)
					}
					call := []jsjson.Field{field("name", str(block.Name)), field("args", args)}
					if model.Compat.SupportsFunctionPartID {
						call = append(call, field("id", str(block.ID)))
					}
					fields := []jsjson.Field{field("functionCall", jsjson.Object(call...))}
					if signature != "" {
						fields = append(fields, field("thoughtSignature", str(signature)))
					}
					parts = append(parts, &part{raw: jsjson.Object(fields...)})
				}
			}
			if len(parts) == 0 {
				continue
			}
			contents = append(contents, &content{role: "model", parts: parts})
		case RoleToolResult:
			var texts []string
			var images []Block
			sawImage := false
			for _, block := range msg.Content {
				switch block.Type {
				case BlockText:
					texts = append(texts, block.Text)
				case BlockImage:
					sawImage = true
					if supportsImages {
						images = append(images, block)
					}
				}
			}
			textResult := strings.Join(texts, "\n")
			omittedImages := !supportsImages && sawImage
			hasText := textResult != ""
			hasImages := len(images) > 0
			multimodal := model.Compat.MultimodalFunctionResponse

			var responseValue string
			switch {
			case omittedImages:
				var kept []string
				if hasText {
					kept = append(kept, toWellFormed(textResult))
				}
				kept = append(kept, NonVisionImagePlaceholder)
				responseValue = strings.Join(kept, "\n")
			case hasText:
				responseValue = toWellFormed(textResult)
			case hasImages:
				responseValue = "(see attached image)"
			}
			imageParts := make([]json.RawMessage, len(images))
			for i, image := range images {
				imageParts[i] = imagePart(image)
			}
			name := msg.ToolName
			if emitted, ok := emittedNames[msg.ToolCallID]; ok {
				name = emitted
			}
			key := "output"
			if msg.IsError {
				key = "error"
			}
			response := []jsjson.Field{field("name", str(name)), field("response", jsjson.Object(field(key, str(responseValue))))}
			if hasImages && multimodal {
				response = append(response, field("parts", jsjson.Marshal(imageParts)))
			}
			if model.Compat.SupportsFunctionPartID {
				response = append(response, field("id", str(msg.ToolCallID)))
			}
			responsePart := &part{raw: jsjson.Object(field("functionResponse", jsjson.Object(response...))), functionResponse: true}
			if ordinal, ok := ordinals[msg.ToolCallID]; ok {
				responsePart.ordinal, responsePart.hasOrdinal = ordinal, true
			}

			merged := false
			if n := len(contents); n > 0 && contents[n-1].role == "user" {
				for _, p := range contents[n-1].parts {
					if p.functionResponse {
						contents[n-1].parts = append(contents[n-1].parts, responsePart)
						merged = true
						break
					}
				}
			}
			if !merged {
				contents = append(contents, &content{role: "user", parts: []*part{responsePart}})
			}
			if hasImages && !multimodal {
				group := pendingImages{parts: []*part{{raw: jsjson.Object(field("text", str("Tool result image:")))}}}
				if ordinal, ok := ordinals[msg.ToolCallID]; ok {
					group.ordinal, group.hasOrdinal = ordinal, true
				}
				for _, raw := range imageParts {
					group.parts = append(group.parts, &part{raw: raw})
				}
				pending = append(pending, group)
			}
		}
	}
	flush()
	out := make([]json.RawMessage, 0, len(contents))
	for _, c := range contents {
		sortKnownInPlace(c.parts, func(p *part) (int, bool) { return p.ordinal, p.hasOrdinal })
		out = append(out, jsjson.Object(field("role", str(c.role)), field("parts", partsJSON(c.parts))))
	}
	return out
}

// ConvertTools is convertTools: one functionDeclarations group, parameters as
// parametersJsonSchema (normalized for Google) or, for Claude behind Cloud
// Code Assist, the legacy `parameters` field produced by legacy. A nil result
// means no tools.
func ConvertTools(model *Model, tools []Tool, legacy func(json.RawMessage) json.RawMessage) json.RawMessage {
	if len(tools) == 0 {
		return nil
	}
	declarations := make([]json.RawMessage, len(tools))
	for i, tool := range tools {
		wire := codexwire.ToolWireSchema(gjson.ParseBytes(tool.Parameters))
		fields := []jsjson.Field{field("name", str(tool.Name)), field("description", str(tool.Description))}
		if model.Compat.CCALegacyParametersSchema && legacy != nil {
			fields = append(fields, field("parameters", legacy(wire)))
		} else {
			fields = append(fields, field("parametersJsonSchema", codexwire.NormalizeSchemaForGoogle(wire)))
		}
		declarations[i] = jsjson.Object(fields...)
	}
	return jsjson.Marshal([]json.RawMessage{jsjson.Object(field("functionDeclarations", jsjson.Marshal(declarations)))})
}

// Package googlewire is the Go port of NeoPi's Google generateContent wire
// layer (ai/src/providers/google-shared.ts and transform-messages.ts): message
// transformation and conversion to Gemini Content[], tool declarations, thinking
// and sampling options, usage and error shaping. It speaks NeoPi's own message
// model so that every Google surface built on it (the Gemini API key provider,
// the Cloud Code Assist wire) sends what NeoPi sends.
package googlewire

import "encoding/json"

// SkipThoughtSignature is the bypass sentinel Gemini 3 accepts in place of a
// signature NeoPi does not hold.
const SkipThoughtSignature = "skip_thought_signature_validator"

// Message roles.
const (
	RoleUser       = "user"
	RoleDeveloper  = "developer"
	RoleAssistant  = "assistant"
	RoleToolResult = "toolResult"
)

// Block kinds.
const (
	BlockText     = "text"
	BlockThinking = "thinking"
	BlockToolCall = "toolCall"
	BlockImage    = "image"
)

// Block is one content block of a NeoPi message.
type Block struct {
	Type string
	// Text carries a text block's text and a thinking block's thinking.
	Text string
	// Signature is a text block's textSignature, a thinking block's
	// thinkingSignature, or a tool call's thoughtSignature.
	Signature string
	// ID, Name and Arguments describe a tool call; Arguments is a JSON object.
	ID, Name  string
	Arguments json.RawMessage
	// MimeType and Data describe an image; URL, when set, replaces Data.
	MimeType, Data, URL string
	// ProviderFileURI is a Google file reference (providerFile.uri).
	ProviderFileURI string
	// Demoted marks text synthesized from foreign thinking.
	Demoted bool
}

// Message is a NeoPi user, developer, assistant or toolResult message.
type Message struct {
	Role string
	// Text is the message when it is a plain string (user/developer only).
	Text string
	// IsString reports that Text, not Content, carries the message.
	IsString bool
	Content  []Block

	// Assistant fields.
	Provider, API, Model string
	StopReason           string

	// ToolResult fields.
	ToolCallID, ToolName string
	IsError              bool
}

// Tool is a NeoPi tool definition; Parameters is its JSON Schema.
type Tool struct {
	Name, Description string
	Parameters        json.RawMessage
}

// Context is NeoPi's request context.
type Context struct {
	SystemPrompts []string
	Messages      []Message
	Tools         []Tool
}

// Thinking is a model's thinking ladder (ThinkingConfig).
type Thinking struct {
	Efforts         []string
	Mode            string
	RequiresEffort  bool
	SuppressWhenOff bool
	EffortBudgets   map[string]int
}

// Compat is the resolved Google compat NeoPi's converters read.
type Compat struct {
	MultimodalFunctionResponse                      bool
	RequiresSkipThoughtSignature                    bool
	RequiresSkipThoughtSignatureOnFirstFunctionCall bool
	SupportsFunctionPartID                          bool
	DropUnsignedThinking                            bool
	CCALegacyParametersSchema                       bool
}

// Model is the target model as the converters read it.
type Model struct {
	ID, Provider, API string
	Input             []string
	Reasoning         bool
	MaxTokens         int
	Thinking          *Thinking
	Compat            Compat
	// NameCharset is the tool-call name charset the wire accepts ("google").
	// NameMaxLength bounds a replayed tool-call name.
	NameMaxLength int
	// NormalizeSignature, when set, decides whether a thought signature a
	// downstream client replays is Google's (returning its replayable form);
	// a signature another provider minted leaves the turn foreign.
	NormalizeSignature func(signature string) (string, bool)
}

func (m *Model) supportsImages() bool {
	for _, input := range m.Input {
		if input == "image" {
			return true
		}
	}
	return false
}

// Package neopidialect holds NeoPi's dialect machinery for the visible text
// channel: the always-on leaked-thinking healer (StreamMarkupHealing with the
// "thinking" pattern), the thinking-fence stripper, and the demoted-thinking
// renderer. Each file is a port of the NeoPi source named in its comments, and the
// tests replay captures produced by running NeoPi's own code under Bun.
package neopidialect

// Event kinds emitted by Healer.
const (
	KindText     = "text"
	KindThinking = "thinking"
)

// HealEvent is one cleaned event in stream order: visible text or a thinking
// delta healed out of leaked reasoning markup.
type HealEvent struct {
	Kind string
	Text string
}

// Healer consumes streamed visible text and emits cleaned text and thinking
// events. It is NeoPi's `new StreamMarkupHealing({ pattern: "thinking" })`: a
// ThinkingInbandScanner that heals leaked `<think>`, `<thinking>`, `<scratchpad>`,
// ```thinking fences and Gemma/Harmony channels out of the visible channel.
//
// Feed only one stream channel (the visible text). A Healer is not safe for
// concurrent use; use one per stream.
type Healer struct {
	scanner *thinkingScanner
}

// NewThinkingHealer returns a Healer for one visible-text stream.
func NewThinkingHealer() *Healer {
	return &Healer{scanner: newThinkingScanner()}
}

// Feed consumes a chunk and returns the cleaned events (StreamMarkupHealing.feedEvents).
func (h *Healer) Feed(text string) []HealEvent {
	if text == "" {
		return nil
	}
	return convertScanEvents(h.scanner.feed(text))
}

// Flush returns held-back stream-end fragments as ordered events
// (StreamMarkupHealing.flushEvents); an unterminated thinking block is emitted as
// thinking.
func (h *Healer) Flush() []HealEvent {
	return convertScanEvents(h.scanner.flush())
}

func convertScanEvents(events []scanEvent) []HealEvent {
	var out []HealEvent
	for _, event := range events {
		switch event.kind {
		case scanText:
			out = append(out, HealEvent{Kind: KindText, Text: event.text})
		case scanThinkingDelta:
			if event.text != "" {
				out = append(out, HealEvent{Kind: KindThinking, Text: event.text})
			}
		case scanThinkingStart, scanThinkingEnd:
		}
	}
	return out
}

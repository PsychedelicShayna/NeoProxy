package neopidialect

import "strings"

// RenderDemotedThinking wraps a prior-turn reasoning string for demotion into
// native conversation history (port of NeoPi's renderDemotedThinking,
// dialect/demotion.ts): the cross-provider case where the target cannot replay it
// as a structured thinking block. Anthropic-dialect models receive bare prose;
// Harmony and Gemma fall back to a plain `<think>` block; every other dialect
// uses its inline-safe tag or fence form. No trailing delimiter is appended.
func RenderDemotedThinking(modelID, text string) string {
	if text == "" {
		return ""
	}
	text = strings.ToValidUTF8(text, "\uFFFD")
	switch dialect := PreferredDialect(modelID); dialect {
	case DialectAnthropic:
		return text
	case DialectHarmony, DialectGemma:
		return "<think>\n" + text + "\n</think>"
	case DialectGemini:
		return "```thinking\n" + text + "\n```"
	case DialectHermes:
		return renderDelimitedThinking("<think>", "</think>", text)
	case DialectMiniMax, DialectXML:
		return renderDelimitedThinking("<thinking>", "</thinking>", text)
	default: // deepseek, glm, kimi, qwen3
		return "<think>\n" + text + "\n</think>"
	}
}

func isASCIIWhitespace(c byte) bool {
	return c == 9 || c == 10 || c == 11 || c == 12 || c == 13 || c == 32
}

func trimASCIIStart(text string, start, end int) int {
	cursor := start
	for cursor < end && isASCIIWhitespace(text[cursor]) {
		cursor++
	}
	return cursor
}

func trimASCIIEnd(text string, start, end int) int {
	cursor := end
	for cursor > start && isASCIIWhitespace(text[cursor-1]) {
		cursor--
	}
	return cursor
}

func findDelimitedThinkingClose(open, closeTag, text string, start, end int) int {
	depth := 1
	cursor := start
	for cursor < end {
		nextClose := indexFrom(text, closeTag, cursor)
		if nextClose < 0 || nextClose >= end {
			return -1
		}
		nextOpen := indexFrom(text, open, cursor)
		if nextOpen >= 0 && nextOpen < nextClose {
			depth++
			cursor = nextOpen + len(open)
			continue
		}
		depth--
		if depth == 0 {
			return nextClose
		}
		cursor = nextClose + len(closeTag)
	}
	return -1
}

// indexFrom is JavaScript's indexOf(needle, from).
func indexFrom(s, needle string, from int) int {
	if from > len(s) {
		from = len(s)
	}
	i := strings.Index(s[from:], needle)
	if i < 0 {
		return -1
	}
	return from + i
}

// unwrapDelimitedThinking strips already-delimited thinking blocks (possibly
// nested or repeated) so re-rendering never double-wraps them.
func unwrapDelimitedThinking(open, closeTag, text string) string {
	end := trimASCIIEnd(text, 0, len(text))
	cursor := trimASCIIStart(text, 0, end)
	if cursor >= end || !strings.HasPrefix(text[cursor:], open) {
		return text
	}
	var segments []string
	for cursor < end {
		if !strings.HasPrefix(text[cursor:], open) {
			return text
		}
		innerStart := cursor + len(open)
		innerEnd := findDelimitedThinkingClose(open, closeTag, text, innerStart, end)
		if innerEnd < 0 {
			return text
		}
		trimmedInnerEnd := trimASCIIEnd(text, innerStart, innerEnd)
		trimmedInnerStart := trimASCIIStart(text, innerStart, trimmedInnerEnd)
		segments = append(segments, unwrapDelimitedThinking(open, closeTag, text[trimmedInnerStart:trimmedInnerEnd]))
		cursor = trimASCIIStart(text, innerEnd+len(closeTag), end)
	}
	return strings.Join(segments, "\n")
}

func renderDelimitedThinking(open, closeTag, text string) string {
	if text == "" {
		return ""
	}
	return open + "\n" + unwrapDelimitedThinking(open, closeTag, text) + "\n" + closeTag
}

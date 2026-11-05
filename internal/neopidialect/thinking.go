package neopidialect

import "strings"

// scanKind enumerates the in-band scan events of NeoPi's InbandScanEvent that the
// thinking scanner produces. impliedThinkingEnd is omitted: it is only emitted
// when the scanner runs with impliedOpen, which StreamMarkupHealing never sets.
type scanKind int

const (
	scanText scanKind = iota
	scanThinkingStart
	scanThinkingDelta
	scanThinkingEnd
)

// scanEvent is one scanner event. text carries the text for scanText and
// scanThinkingDelta; for scanThinkingEnd it carries the whole block's thinking.
type scanEvent struct {
	kind scanKind
	text string
}

type reasoningTag struct {
	open   string
	close  string
	fenced bool
}

// reasoningTags lists every dialect's in-band thinking section in its canonical
// renderThinking form (NeoPi dialect/thinking.ts TAGS). The DeepSeek `<think>`
// tag is also an implied-open tag in NeoPi, which only matters to scanners built
// with impliedOpen; this port never enables it.
var reasoningTags = []reasoningTag{
	{open: "<think>", close: "</think>"},
	{open: "<thinking>", close: "</thinking>"},
	{open: "<scratchpad>", close: "</scratchpad>"},
	{open: "```thinking\n", close: "```", fenced: true},                          // gemini fenced thinking
	{open: "<|channel>thought\n", close: "<channel|>"},                           // gemma reasoning channel
	{open: "<|start|>assistant<|channel|>analysis<|message|>", close: "<|end|>"}, // harmony analysis (rendered)
	{open: "<|channel|>analysis<|message|>", close: "<|end|>"},                   // harmony analysis (bare leak)
}

const backtick = '`'

// thinkingScanner heals reasoning a model leaked into its visible text channel
// back into thinking events (port of NeoPi's ThinkingInbandScanner without the
// impliedOpen mode).
type thinkingScanner struct {
	buffer   string
	closeTag string
	thinking strings.Builder
	// fenced is the fence-aware close-matcher while inside a ```thinking block.
	fenced *fencedThinkingScanner
	// codeTicks is the backtick count that opened the Markdown code span/fence we
	// are inside; 0 when not in code.
	codeTicks int
	// codeFenced is true when codeTicks opened a fenced block (closes on a fence
	// line), not an inline span.
	codeFenced bool
	// lineIndent is the leading-space count on the current output line, or -1 once
	// a non-space character has appeared. Starts at 0 (line start) so a fence
	// opening the stream, or one indented up to 3 spaces, is recognized.
	lineIndent int
}

func newThinkingScanner() *thinkingScanner {
	return &thinkingScanner{}
}

func (s *thinkingScanner) feed(text string) []scanEvent {
	if text == "" {
		return nil
	}
	s.buffer += text
	return s.consume(false)
}

func (s *thinkingScanner) flush() []scanEvent {
	events := s.consume(true)
	if s.buffer == "" {
		return events
	}
	if s.closeTag != "" {
		events = s.emitThinking(s.buffer, events)
		events = append(events, scanEvent{kind: scanThinkingEnd, text: s.thinking.String()})
	} else {
		events = append(events, scanEvent{kind: scanText, text: s.buffer})
	}
	s.buffer = ""
	s.closeTag = ""
	return events
}

func (s *thinkingScanner) endThinking(events []scanEvent) []scanEvent {
	events = append(events, scanEvent{kind: scanThinkingEnd, text: s.thinking.String()})
	s.thinking.Reset()
	s.closeTag = ""
	return events
}

func (s *thinkingScanner) consume(final bool) []scanEvent {
	var events []scanEvent
	for {
		if s.fenced != nil {
			// Run even with an empty buffer so a held partial close flushes on final.
			result := s.fenced.feed(s.buffer, final)
			if result.closed {
				s.buffer = result.rest
			} else {
				s.buffer = ""
			}
			events = s.emitThinking(result.thinking, events)
			if result.closed || final {
				events = s.endThinking(events)
				s.fenced = nil
			}
			if s.fenced != nil {
				break
			}
			continue
		}
		if s.buffer == "" {
			break
		}
		if s.closeTag != "" {
			close := strings.Index(s.buffer, s.closeTag)
			if close == -1 {
				hold := 0
				if !final {
					hold = partialSuffixOverlap(s.buffer, s.closeTag)
				}
				events = s.emitThinking(s.buffer[:len(s.buffer)-hold], events)
				s.buffer = s.buffer[len(s.buffer)-hold:]
				break
			}
			events = s.emitThinking(s.buffer[:close], events)
			s.buffer = s.buffer[close+len(s.closeTag):]
			events = s.endThinking(events)
			continue
		}
		if s.codeTicks > 0 {
			var closed bool
			events, closed = s.emitCode(final, events)
			if closed {
				continue
			}
			break
		}

		hit := scanVisible(s.buffer, final)
		if hit.kind == hitNone {
			events = s.emitText(s.buffer, events)
			s.buffer = ""
			break
		}
		if hit.index > 0 {
			events = s.emitText(s.buffer[:hit.index], events)
		}
		if hit.kind == hitHold {
			s.buffer = s.buffer[hit.index:]
			break
		}
		if hit.kind == hitCode {
			fenced := hit.ticks >= 3 && s.lineIndent >= 0 && s.lineIndent <= 3
			events = s.emitText(s.buffer[hit.index:hit.index+hit.ticks], events)
			s.buffer = s.buffer[hit.index+hit.ticks:]
			s.codeTicks = hit.ticks
			s.codeFenced = fenced
			continue
		}
		s.buffer = s.buffer[hit.index+len(hit.tag.open):]
		s.closeTag = hit.tag.close
		s.thinking.Reset()
		if hit.tag.fenced {
			s.fenced = &fencedThinkingScanner{}
		}
		events = append(events, scanEvent{kind: scanThinkingStart})
	}
	return events
}

// emitCode emits buffered content while inside a Markdown code region,
// suppressing reasoning-tag detection. A fenced block closes only on a fence line
// (a line of backticks at least as long as the opener); an inline span closes on
// the first backtick run of exactly the opener length. It returns true when the
// region closed and the loop should continue, false when it held back and should
// break.
func (s *thinkingScanner) emitCode(final bool, events []scanEvent) ([]scanEvent, bool) {
	if s.codeFenced {
		end := findFenceCloseEnd(s.buffer, s.codeTicks, final)
		if end != -1 {
			events = s.emitText(s.buffer[:end], events)
			s.buffer = s.buffer[end:]
			s.codeTicks = 0
			s.codeFenced = false
			return events, true
		}
		if final {
			events = s.emitText(s.buffer, events)
			s.buffer = ""
			s.codeTicks = 0
			s.codeFenced = false
			return events, false
		}
		// Stream committed lines; hold only the last (possibly partial) fence line.
		if lastNl := strings.LastIndexByte(s.buffer, '\n'); lastNl != -1 {
			events = s.emitText(s.buffer[:lastNl+1], events)
			s.buffer = s.buffer[lastNl+1:]
		}
		return events, false
	}
	close := findBacktickRun(s.buffer, 0, s.codeTicks)
	if close != -1 && (final || close+s.codeTicks < len(s.buffer)) {
		events = s.emitText(s.buffer[:close+s.codeTicks], events)
		s.buffer = s.buffer[close+s.codeTicks:]
		s.codeTicks = 0
		return events, true
	}
	// No committed close yet: emit text, holding a trailing backtick run that may
	// still grow into, or past, the closing delimiter.
	hold := 0
	if !final {
		hold = trailingBacktickRun(s.buffer)
	}
	events = s.emitText(s.buffer[:len(s.buffer)-hold], events)
	s.buffer = s.buffer[len(s.buffer)-hold:]
	if final {
		s.codeTicks = 0
	}
	return events, false
}

func (s *thinkingScanner) emitText(text string, events []scanEvent) []scanEvent {
	if text == "" {
		return events
	}
	s.lineIndent = trailingLineIndent(text, s.lineIndent)
	return append(events, scanEvent{kind: scanText, text: text})
}

func (s *thinkingScanner) emitThinking(delta string, events []scanEvent) []scanEvent {
	if delta == "" {
		return events
	}
	s.thinking.WriteString(delta)
	return append(events, scanEvent{kind: scanThinkingDelta, text: delta})
}

type hitKind int

const (
	hitNone hitKind = iota
	hitTag
	hitCode
	hitHold
)

// visibleHit is the outcome of scanning idle visible text for the next
// reasoning-tag or code-span boundary.
type visibleHit struct {
	kind  hitKind
	index int
	tag   reasoningTag
	ticks int
}

// scanVisible walks idle visible text for the earliest boundary: a leaked
// reasoning-tag open, a Markdown code-span/fence opener (a backtick run), or, when
// more chunks may follow, a held partial delimiter at the buffer tail.
//
// Reasoning tags win at any position so the gemini ```thinking fence is healed
// instead of being read as a code fence. Backtick runs enter code mode so a
// literal `<think>` inside inline code or a fenced block stays visible text.
//
// Every delimiter is ASCII and starts with '<' or a backtick, so scanning bytes
// finds exactly the boundaries NeoPi finds scanning UTF-16 code units; NeoPi's
// "only the tail can be a prefix" index bound is implied by the length check on
// the held rest.
func scanVisible(buffer string, final bool) visibleHit {
	for i := 0; i < len(buffer); i++ {
		c := buffer[i]
		if c != '<' && c != backtick {
			continue
		}
		for _, tag := range reasoningTags {
			if strings.HasPrefix(buffer[i:], tag.open) {
				return visibleHit{kind: hitTag, index: i, tag: tag}
			}
		}
		if !final {
			rest := buffer[i:]
			for _, tag := range reasoningTags {
				if len(tag.open) > len(rest) && strings.HasPrefix(tag.open, rest) {
					return visibleHit{kind: hitHold, index: i}
				}
			}
		}
		if c == backtick {
			ticks := backtickRun(buffer, i)
			if !final && i+ticks == len(buffer) {
				return visibleHit{kind: hitHold, index: i}
			}
			return visibleHit{kind: hitCode, index: i, ticks: ticks}
		}
	}
	return visibleHit{kind: hitNone}
}

// backtickRun is the length of the maximal backtick run beginning at from.
func backtickRun(buffer string, from int) int {
	end := from
	for end < len(buffer) && buffer[end] == backtick {
		end++
	}
	return end - from
}

// findBacktickRun is the index of the first maximal backtick run of exactly ticks
// at or after from, else -1.
func findBacktickRun(buffer string, from, ticks int) int {
	for i := indexByteFrom(buffer, backtick, from); i != -1; i = indexByteFrom(buffer, backtick, i) {
		run := backtickRun(buffer, i)
		if run == ticks {
			return i
		}
		i += run
	}
	return -1
}

// trailingBacktickRun is the length of a backtick run that ends at the buffer
// tail; 0 when the tail is not a backtick.
func trailingBacktickRun(buffer string) int {
	start := len(buffer)
	for start > 0 && buffer[start-1] == backtick {
		start--
	}
	return len(buffer) - start
}

// trailingLineIndent is the leading-space count of the line at the tail of text,
// continuing from the prior line's indent state. It returns -1 once any non-space
// character has appeared on the current line.
func trailingLineIndent(text string, prior int) int {
	lastNl := strings.LastIndexByte(text, '\n')
	indent := prior
	if lastNl != -1 {
		indent = 0
	}
	for i := lastNl + 1; i < len(text); i++ {
		if indent == -1 {
			break
		}
		if text[i] == ' ' {
			indent++
		} else {
			indent = -1
		}
	}
	return indent
}

// findFenceCloseEnd is the index just past the first closing fence line for a
// fenced block opened with ticks backticks, or -1 when none is committed yet. A
// closing fence is a whole line whose trimmed content is only backticks, at least
// ticks of them. A line without a terminating newline is committed only when
// final (no more input can extend it into a non-fence line).
func findFenceCloseEnd(buffer string, ticks int, final bool) int {
	for start := 0; start <= len(buffer); {
		nl := indexByteFrom(buffer, '\n', start)
		terminated := nl != -1
		end := len(buffer)
		if terminated {
			end = nl
		}
		line := jsTrim(buffer[start:end])
		if len(line) >= ticks && isAllBackticks(line) && (terminated || final) {
			if terminated {
				return nl + 1
			}
			return len(buffer)
		}
		if !terminated {
			break
		}
		start = nl + 1
	}
	return -1
}

// isAllBackticks is true when text is non-empty and every character is a backtick.
func isAllBackticks(text string) bool {
	return text != "" && strings.Trim(text, "`") == ""
}

// partialSuffixOverlap is the length of the longest proper prefix of tag that is
// a suffix of text (NeoPi dialect/coercion.ts partialSuffixOverlap).
func partialSuffixOverlap(text, tag string) int {
	for k := min(len(text), len(tag)-1); k > 0; k-- {
		if strings.HasSuffix(text, tag[:k]) {
			return k
		}
	}
	return 0
}

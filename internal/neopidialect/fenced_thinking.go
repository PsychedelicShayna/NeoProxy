package neopidialect

import "strings"

// fencedResult is the outcome of feeding bytes to fencedThinkingScanner.
type fencedResult struct {
	// thinking is the thinking text to emit for this feed (may be empty).
	thinking string
	// closed is true once the thinking closer has been consumed.
	closed bool
	// rest is the text after the closing fence (visible reply); meaningful only when closed.
	rest string
}

// fencedThinkingScanner is a stateful, line-oriented close-matcher for one
// ```thinking block that respects nested Markdown code fences (port of NeoPi's
// FencedThinkingScanner, dialect/fenced-thinking.ts). A top-level fence whose info
// string is a single language token opens a nested block; a bare fence, or a fence
// glued to prose, closes the thinking and the prose is the visible reply.
type fencedThinkingScanner struct {
	buffer string
	// inner is the fence run that opened the current nested code block, or "" at top level.
	inner string
	// emitted counts the leading bytes of the incomplete first line already returned as thinking.
	emitted int
}

// fenceLead splits a line into its backtick run and remainder, mirroring
// /^ {0,3}(`*)([\s\S]*)$/: up to three leading spaces, then a (possibly empty)
// run of backticks, then everything else.
func fenceLead(line string) (ticks int, rest string) {
	i := 0
	for i < len(line) && i < 3 && line[i] == ' ' {
		i++
	}
	start := i
	for i < len(line) && line[i] == '`' {
		i++
	}
	return i - start, line[i:]
}

// isLangToken mirrors /^[A-Za-z0-9_+#-]+$/.
func isLangToken(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		case c == '_', c == '+', c == '#', c == '-':
		default:
			return false
		}
	}
	return true
}

// feed appends text and returns thinking deltas plus close state. When final,
// the held tail resolves: a bare ``` or a ```<reply> fence closes the block
// (remainder becomes rest), otherwise it is unterminated thinking.
func (f *fencedThinkingScanner) feed(text string, final bool) fencedResult {
	f.buffer += text
	var thinking strings.Builder
	for {
		nl := strings.IndexByte(f.buffer, '\n')
		if nl == -1 {
			break
		}
		line := f.buffer[:nl]
		if f.inner == "" {
			if rest, ok := closeRest(line); ok {
				// Closer bytes are always held, so emitted is 0 and nothing leaked.
				reply := rest + f.buffer[nl:] // keep the newline with the reply
				f.reset()
				return fencedResult{thinking: thinking.String(), closed: true, rest: reply}
			}
		}
		// Content line (including an inner-fence open/close).
		thinking.WriteString(f.buffer[f.emitted : nl+1])
		f.updateInner(line)
		f.buffer = f.buffer[nl+1:]
		f.emitted = 0
	}

	tail := f.buffer
	if f.inner != "" {
		// Inside a nested block every byte is thinking content: emit eagerly,
		// keeping it buffered until the newline classifies the line.
		thinking.WriteString(tail[f.emitted:])
		f.emitted = len(tail)
		return fencedResult{thinking: thinking.String()}
	}

	if final {
		if rest, ok := closeRestFinal(tail); ok {
			f.reset()
			return fencedResult{thinking: thinking.String(), closed: true, rest: rest}
		}
	} else {
		if rest, ok := closeRestStreamingTail(tail); ok {
			f.reset()
			return fencedResult{thinking: thinking.String(), closed: true, rest: rest}
		}
		if mustHold(tail) {
			return fencedResult{thinking: thinking.String()}
		}
	}
	// Either final (flush the remainder) or a line that can no longer be a fence.
	thinking.WriteString(tail[f.emitted:])
	if final {
		f.reset()
	} else {
		f.emitted = len(tail)
	}
	return fencedResult{thinking: thinking.String()}
}

// closeRest is the complete-line close test. A bare backtick fence closes
// thinking; a language-token fence line opens an inner block; a prose-like
// remainder is the inline visible reply.
func closeRest(line string) (string, bool) {
	ticks, rest := fenceLead(line)
	if ticks < 3 {
		return "", false
	}
	if rest == "" || jsTrim(rest) == "" {
		return "", true // bare close (only whitespace)
	}
	if isLangToken(rest) {
		return "", false // language-tagged inner opener
	}
	return rest, true
}

// closeRestFinal is the final tail close test: EOF disambiguates any top-level
// backtick run as the closer.
func closeRestFinal(tail string) (string, bool) {
	ticks, rest := fenceLead(tail)
	if ticks < 3 {
		return "", false
	}
	if jsTrim(rest) == "" {
		return "", true
	}
	return rest, true
}

// closeRestStreamingTail is the streaming tail close test: only a prose-like
// inline reply resolves the close.
func closeRestStreamingTail(tail string) (string, bool) {
	ticks, rest := fenceLead(tail)
	if ticks < 3 {
		return "", false
	}
	if rest == "" || jsTrim(rest) == "" || isLangToken(rest) {
		return "", false
	}
	return rest, true
}

// mustHold reports whether a top-level trailing partial is still undecided.
func mustHold(tail string) bool {
	ticks, rest := fenceLead(tail)
	// A growing backtick run could still reach a fence. A complete run plus a
	// language-token prefix is also undecided until a newline confirms an inner
	// opener or a non-token character confirms an inline close.
	if rest == "" || jsTrim(rest) == "" {
		return ticks >= 1 || onlyLeadSpaces(tail)
	}
	return ticks >= 3 && isLangToken(rest)
}

// onlyLeadSpaces mirrors /^ {0,3}$/.
func onlyLeadSpaces(s string) bool {
	if len(s) > 3 {
		return false
	}
	return strings.Trim(s, " ") == ""
}

func (f *fencedThinkingScanner) reset() {
	f.buffer = ""
	f.inner = ""
	f.emitted = 0
}

// fenceLine mirrors /^ {0,3}(`{3,}|~{3,})(.*)$/ for a line without "\n": the
// run is the maximal fence run and the info string must not contain the
// characters `.` refuses to match (CR, U+2028, U+2029).
func fenceLine(line string) (run, info string, ok bool) {
	i := 0
	for i < len(line) && i < 3 && line[i] == ' ' {
		i++
	}
	if i >= len(line) || (line[i] != '`' && line[i] != '~') {
		return "", "", false
	}
	c := line[i]
	j := i
	for j < len(line) && line[j] == c {
		j++
	}
	if j-i < 3 {
		return "", "", false
	}
	info = line[j:]
	if strings.ContainsAny(info, "\r\u2028\u2029") {
		return "", "", false
	}
	return line[i:j], info, true
}

// updateInner toggles nested-fence state for a completed content line.
func (f *fencedThinkingScanner) updateInner(line string) {
	run, rawInfo, ok := fenceLine(line)
	if !ok {
		return
	}
	info := jsTrim(rawInfo)
	if f.inner == "" {
		// A top-level closer was already handled by closeRest, so this opens a
		// nested code block (tilde fence, or backtick fence with a language token).
		f.inner = run
	} else if run[0] == f.inner[0] && len(run) >= len(f.inner) && info == "" {
		// Closing fence: same char, at least as long, no info string.
		f.inner = ""
	}
}

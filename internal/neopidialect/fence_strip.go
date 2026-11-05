package neopidialect

import "strings"

// FenceStripper strips self-referential reasoning-fence delimiters that a
// provider leaks inside a structured thinking part (port of NeoPi's
// ThinkingFenceStripper, dialect/thinking-fence-strip.ts). It removes only a
// standalone opener line: up to 3 leading spaces, at least 3 backticks, then
// `thinking` or `reasoning` (ASCII case-insensitive), optional trailing spaces or
// tabs and an optional CR. Inline mentions, language-tagged fences and bare
// closers are left untouched.
//
// Streaming-safe: deltas may split a line anywhere. One FenceStripper per
// thinking block; Push every thinking delta and drain the tail with Flush. It is
// not safe for concurrent use.
type FenceStripper struct {
	// carry is the buffered content of the current line still being classified.
	carry strings.Builder
	// passthrough is true once the current line is known not to be an opener; it
	// passes through until the newline.
	passthrough bool
}

// NewFenceStripper returns a stripper for one thinking block.
func NewFenceStripper() *FenceStripper {
	return &FenceStripper{}
}

// Push consumes one thinking delta and returns the sanitized text to emit (may be empty).
func (f *FenceStripper) Push(chunk string) string {
	var out strings.Builder
	for _, ch := range chunk {
		if f.passthrough {
			out.WriteRune(ch)
			if ch == '\n' {
				f.passthrough = false
			}
			continue
		}
		if ch == '\n' {
			carry := f.carry.String()
			if !isOpenerLine(carry) {
				out.WriteString(carry)
				out.WriteByte('\n')
			}
			f.carry.Reset()
			continue
		}
		f.carry.WriteRune(ch)
		if carry := f.carry.String(); !couldBeOpenerPrefix(carry) {
			out.WriteString(carry)
			f.carry.Reset()
			f.passthrough = true
		}
	}
	return out.String()
}

// Flush drains any held partial line at block end and returns the text to emit (may be empty).
func (f *FenceStripper) Flush() string {
	carry := f.carry.String()
	f.carry.Reset()
	f.passthrough = false
	if isOpenerLine(carry) {
		return ""
	}
	return carry
}

// isOpenerLine mirrors /^ {0,3}`{3,}(?:thinking|reasoning)[ \t]*\r?$/i.
func isOpenerLine(line string) bool {
	ticks, rest := fenceLead(line)
	if ticks < 3 {
		return false
	}
	word, ok := cutFoldPrefix(rest, "thinking")
	if !ok {
		word, ok = cutFoldPrefix(rest, "reasoning")
		if !ok {
			return false
		}
	}
	word = strings.TrimLeft(word, " \t")
	word = strings.TrimSuffix(word, "\r")
	return word == ""
}

// cutFoldPrefix cuts an ASCII-case-insensitive prefix. JavaScript's non-unicode
// /i flag never folds a non-ASCII character to an ASCII one, so byte-wise ASCII
// folding is exact.
func cutFoldPrefix(s, prefix string) (string, bool) {
	if len(s) < len(prefix) {
		return "", false
	}
	for i := 0; i < len(prefix); i++ {
		c := s[i]
		if c >= 'A' && c <= 'Z' {
			c += 'a' - 'A'
		}
		if c != prefix[i] {
			return "", false
		}
	}
	return s[len(prefix):], true
}

// couldBeOpenerPrefix reports whether line (a partial, newline-not-yet-seen) can
// still grow into an opener line.
func couldBeOpenerPrefix(line string) bool {
	// Tolerate a pending CR from a split CRLF.
	s := strings.TrimSuffix(line, "\r")
	ticks, rest := fenceLead(s)
	if rest == "" {
		return true // still consuming leading spaces / backticks
	}
	if ticks < 3 {
		return false // a non-backtick char appeared before 3 backticks: never a fence
	}
	word := jsLower(strings.TrimRight(rest, " \t"))
	return strings.HasPrefix("thinking", word) || strings.HasPrefix("reasoning", word)
}

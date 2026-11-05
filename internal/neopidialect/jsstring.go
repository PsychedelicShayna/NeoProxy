package neopidialect

import "strings"

// isJSWhitespace reports whether r is trimmed by ECMAScript String.prototype.trim
// (WhiteSpace plus LineTerminator). It differs from unicode.IsSpace: U+FEFF is
// whitespace in JavaScript while U+0085 is not.
func isJSWhitespace(r rune) bool {
	switch r {
	case '\t', '\n', '\v', '\f', '\r', ' ', 0x00a0, 0x1680, 0x2028, 0x2029, 0x202f, 0x205f, 0x3000, 0xfeff:
		return true
	}
	return r >= 0x2000 && r <= 0x200a
}

// jsTrim mirrors String.prototype.trim.
func jsTrim(s string) string {
	return strings.TrimFunc(s, isJSWhitespace)
}

// jsLower mirrors String.prototype.toLowerCase for the characters that matter to
// the ASCII keyword comparisons in this package: Go maps U+0130 to a plain "i"
// while JavaScript's full case mapping yields "i" + U+0307.
func jsLower(s string) string {
	return strings.ToLower(strings.ReplaceAll(s, "\u0130", "i\u0307"))
}

// indexByteFrom is JavaScript's indexOf(char, from) for a single byte.
func indexByteFrom(s string, c byte, from int) int {
	if from >= len(s) {
		return -1
	}
	i := strings.IndexByte(s[from:], c)
	if i < 0 {
		return -1
	}
	return from + i
}

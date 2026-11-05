package antigravity

import (
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/tidwall/gjson"
)

// The helpers below reproduce the JavaScript value semantics NeoPi's code
// leans on (string slicing in UTF-16 units, Number formatting, object key
// order, JSON.stringify, Date.parse), so error text and reports match.

// sliceUTF16 is `text.slice(0, n)`: the first n UTF-16 code units.
func sliceUTF16(text string, n int) string {
	if len(text) <= n {
		return text
	}
	units := 0
	for i, r := range text {
		width := 1
		if r >= 0x10000 {
			width = 2
		}
		if units+width > n {
			return text[:i]
		}
		units += width
	}
	return text
}

// jsNumberString is String(number) for a finite value.
func jsNumberString(value float64) string {
	if value == 0 {
		return "0"
	}
	abs := math.Abs(value)
	if abs >= 1e21 || abs < 1e-6 {
		text := strconv.FormatFloat(value, 'e', -1, 64)
		mantissa, exponent, _ := strings.Cut(text, "e")
		sign := exponent[:1]
		digits := strings.TrimLeft(exponent[1:], "0")
		if digits == "" {
			digits = "0"
		}
		return mantissa + "e" + sign + digits
	}
	return strconv.FormatFloat(value, 'f', -1, 64)
}

// scalarString is common.ts scalarString: a non-empty string, or a finite
// number rendered as a string.
func scalarString(value gjson.Result) (string, bool) {
	switch value.Type {
	case gjson.String:
		return value.Str, value.Str != ""
	case gjson.Number:
		if !math.IsInf(value.Num, 0) && !math.IsNaN(value.Num) {
			return jsNumberString(value.Num), true
		}
	}
	return "", false
}

type jsEntry struct {
	key   string
	value gjson.Result
}

var arrayIndexKey = regexp.MustCompile(`^(0|[1-9][0-9]*)$`)

// jsEntries is Object.entries order for a parsed JSON object: integer-like
// keys ascending first, then the rest in insertion order, a repeated key
// keeping its first position with the last value.
func jsEntries(object gjson.Result) []jsEntry {
	if !object.IsObject() {
		return nil
	}
	var entries []jsEntry
	position := map[string]int{}
	object.ForEach(func(key, value gjson.Result) bool {
		if at, seen := position[key.Str]; seen {
			entries[at].value = value
			return true
		}
		position[key.Str] = len(entries)
		entries = append(entries, jsEntry{key: key.Str, value: value})
		return true
	})
	var indexed, named []jsEntry
	for _, entry := range entries {
		if n, err := strconv.ParseUint(entry.key, 10, 64); err == nil && arrayIndexKey.MatchString(entry.key) && n < 4294967295 {
			indexed = append(indexed, entry)
		} else {
			named = append(named, entry)
		}
	}
	sort.SliceStable(indexed, func(i, j int) bool {
		a, _ := strconv.ParseUint(indexed[i].key, 10, 64)
		b, _ := strconv.ParseUint(indexed[j].key, 10, 64)
		return a < b
	})
	return append(indexed, named...)
}

// jsStringify is JSON.stringify of a parsed JSON value.
func jsStringify(value gjson.Result) string {
	var out strings.Builder
	writeJSON(&out, value)
	return out.String()
}

func writeJSON(out *strings.Builder, value gjson.Result) {
	switch value.Type {
	case gjson.String:
		writeJSONString(out, value.Str)
	case gjson.Number:
		if math.IsInf(value.Num, 0) || math.IsNaN(value.Num) {
			out.WriteString("null")
		} else {
			out.WriteString(jsNumberString(value.Num))
		}
	case gjson.True:
		out.WriteString("true")
	case gjson.False:
		out.WriteString("false")
	case gjson.JSON:
		if value.IsArray() {
			out.WriteByte('[')
			for i, element := range value.Array() {
				if i > 0 {
					out.WriteByte(',')
				}
				writeJSON(out, element)
			}
			out.WriteByte(']')
			return
		}
		out.WriteByte('{')
		for i, entry := range jsEntries(value) {
			if i > 0 {
				out.WriteByte(',')
			}
			writeJSONString(out, entry.key)
			out.WriteByte(':')
			writeJSON(out, entry.value)
		}
		out.WriteByte('}')
	default:
		out.WriteString("null")
	}
}

func writeJSONString(out *strings.Builder, text string) {
	const hexDigits = "0123456789abcdef"
	out.WriteByte('"')
	for i := 0; i < len(text); {
		r, width := utf8.DecodeRuneInString(text[i:])
		switch {
		case r == utf8.RuneError && width == 1:
			out.WriteString(`\ufffd`)
		case r == '"':
			out.WriteString(`\"`)
		case r == '\\':
			out.WriteString(`\\`)
		case r == '\b':
			out.WriteString(`\b`)
		case r == '\f':
			out.WriteString(`\f`)
		case r == '\n':
			out.WriteString(`\n`)
		case r == '\r':
			out.WriteString(`\r`)
		case r == '\t':
			out.WriteString(`\t`)
		case r < 0x20:
			out.WriteString(`\u00`)
			out.WriteByte(hexDigits[r>>4])
			out.WriteByte(hexDigits[r&15])
		default:
			out.WriteString(text[i : i+width])
		}
		i += width
	}
	out.WriteByte('"')
}

var numberLiteral = regexp.MustCompile(`^[+-]?(?:[0-9]+\.?[0-9]*|\.[0-9]+)(?:[eE][+-]?[0-9]+)?$`)

// jsNumber is Number(text); ok is false for NaN and infinities.
func jsNumber(text string) (float64, bool) {
	text = strings.Trim(text, " \t\n\r\v\f\u00a0\u2028\u2029\ufeff")
	if text == "" {
		return 0, true
	}
	if len(text) > 2 && text[0] == '0' {
		base := map[byte]int{'x': 16, 'X': 16, 'o': 8, 'O': 8, 'b': 2, 'B': 2}[text[1]]
		if base != 0 {
			n, err := strconv.ParseUint(text[2:], base, 64)
			return float64(n), err == nil
		}
	}
	if !numberLiteral.MatchString(text) {
		return 0, false
	}
	value, err := strconv.ParseFloat(text, 64)
	if err != nil || math.IsInf(value, 0) || math.IsNaN(value) {
		return 0, false
	}
	return value, true
}

// parseISOTimestamp is shared.ts parseIsoTimestamp: Date.parse of a
// non-empty string in epoch milliseconds. Forms without an offset are local
// time, except a bare date, which is UTC.
func parseISOTimestamp(text string) (float64, bool) {
	if text == "" {
		return 0, false
	}
	for _, form := range []struct {
		layout string
		local  bool
	}{
		{"2006-01-02T15:04:05Z07:00", false},
		{"2006-01-02T15:04Z07:00", false},
		{"2006-01-02", false},
		{"2006-01-02T15:04:05", true},
		{"2006-01-02T15:04", true},
		{"2006-01-02 15:04:05Z07:00", false},
		{"2006-01-02 15:04:05", true},
		{time.RFC1123, false},
		{time.RFC1123Z, false},
	} {
		location := time.UTC
		if form.local {
			location = time.Local
		}
		if parsed, err := time.ParseInLocation(form.layout, text, location); err == nil {
			return float64(parsed.UnixMilli()), true
		}
	}
	return 0, false
}

package antigravity

import (
	"regexp"
	"strings"
)

// CallbackInput is parseCallbackInput's result: a nil field is NeoPi's
// `undefined`.
type CallbackInput struct {
	Code  *string
	State *string
}

var urlScheme = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9+.\-]*:`)

// ParseCallbackInput is parseCallbackInput (registry/oauth/callback-server.ts):
// it reads the code and state out of a pasted redirect URL, a pasted query
// string or a bare code that may carry `#state`.
func ParseCallbackInput(input string) CallbackInput {
	value := strings.TrimSpace(input)
	if value == "" {
		return CallbackInput{}
	}
	// `new URL(value)` accepts anything with a scheme; only its query counts.
	if urlScheme.MatchString(value) {
		query := value
		if hash := strings.IndexByte(query, '#'); hash >= 0 {
			query = query[:hash]
		}
		if mark := strings.IndexByte(query, '?'); mark >= 0 {
			query = query[mark+1:]
		} else {
			query = ""
		}
		return CallbackInput{Code: searchParam(query, "code"), State: searchParam(query, "state")}
	}
	if strings.Contains(value, "code=") {
		query := value
		if query[0] == '?' || query[0] == '#' {
			query = query[1:]
		}
		return CallbackInput{Code: searchParam(query, "code"), State: searchParam(query, "state")}
	}
	// Assume a raw code, possibly with the state after '#'. JS `split("#", 2)`
	// keeps the first two pieces and drops the rest.
	parts := strings.Split(value, "#")
	result := CallbackInput{Code: &parts[0]}
	if len(parts) > 1 {
		result.State = &parts[1]
	}
	return result
}

// searchParam is URLSearchParams.get: the first value of a name, or nil.
func searchParam(query, name string) *string {
	if query == "" {
		return nil
	}
	for _, pair := range strings.Split(query, "&") {
		if pair == "" {
			continue
		}
		key, value, _ := strings.Cut(pair, "=")
		if formDecode(key) == name {
			decoded := formDecode(value)
			return &decoded
		}
	}
	return nil
}

// formDecode is the application/x-www-form-urlencoded decoder: '+' is a space
// and a malformed percent escape stays literal.
func formDecode(text string) string {
	if !strings.ContainsAny(text, "+%") {
		return text
	}
	out := make([]byte, 0, len(text))
	for i := 0; i < len(text); i++ {
		switch c := text[i]; {
		case c == '+':
			out = append(out, ' ')
		case c == '%' && i+2 < len(text) && isHex(text[i+1]) && isHex(text[i+2]):
			out = append(out, unhex(text[i+1])<<4|unhex(text[i+2]))
			i += 2
		default:
			out = append(out, c)
		}
	}
	return string(out)
}

func isHex(c byte) bool {
	return c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F'
}

func unhex(c byte) byte {
	switch {
	case c >= '0' && c <= '9':
		return c - '0'
	case c >= 'a' && c <= 'f':
		return c - 'a' + 10
	}
	return c - 'A' + 10
}

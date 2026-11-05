package antigravity

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"strconv"
)

// PKCECodes is NeoPi's generatePKCE pair: a 96-byte random verifier and its
// S256 challenge, both base64url without padding.
type PKCECodes struct {
	Verifier  string
	Challenge string
}

// GeneratePKCE returns a fresh verifier and challenge.
func GeneratePKCE() (*PKCECodes, error) {
	raw := make([]byte, 96)
	if _, err := rand.Read(raw); err != nil {
		return nil, fmt.Errorf("antigravity pkce: %w", err)
	}
	return pkceFromBytes(raw), nil
}

func pkceFromBytes(raw []byte) *PKCECodes {
	verifier := base64.RawURLEncoding.EncodeToString(raw)
	hash := sha256.Sum256([]byte(verifier))
	return &PKCECodes{Verifier: verifier, Challenge: base64.RawURLEncoding.EncodeToString(hash[:])}
}

// GenerateState is OAuthCallbackFlow.generateState: 16 random bytes as hex.
func GenerateState() (string, error) {
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("antigravity state: %w", err)
	}
	return hex.EncodeToString(raw), nil
}

// RedirectURI is the callback URL the rule advertises for a bound port.
func RedirectURI(port int) string {
	return "http://" + CallbackHost + ":" + strconv.Itoa(port) + CallbackPath
}

// formEscape is the application/x-www-form-urlencoded percent-encoding
// URLSearchParams applies: everything but ASCII alphanumerics and `*-._` is
// escaped, with a space as `+`. net/url's QueryEscape differs on `*` and `~`.
func formEscape(value string) string {
	const hexDigits = "0123456789ABCDEF"
	out := make([]byte, 0, len(value)+8)
	for i := 0; i < len(value); i++ {
		c := value[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '*', c == '-', c == '.', c == '_':
			out = append(out, c)
		case c == ' ':
			out = append(out, '+')
		default:
			out = append(out, '%', hexDigits[c>>4], hexDigits[c&15])
		}
	}
	return string(out)
}

// formEncode serialises ordered name/value pairs like URLSearchParams.toString.
func formEncode(pairs [][2]string) string {
	out := make([]byte, 0, 256)
	for i, pair := range pairs {
		if i > 0 {
			out = append(out, '&')
		}
		out = append(out, formEscape(pair[0])...)
		out = append(out, '=')
		out = append(out, formEscape(pair[1])...)
	}
	return string(out)
}

// BuildAuthURL is generateAuthUrl: the standard authorize parameters in
// NeoPi's order (client_id, response_type, redirect_uri, scope, PKCE
// challenge, state) followed by the rule's access_type and prompt.
func BuildAuthURL(state, challenge, redirectURI string) string {
	scope := ""
	for i, entry := range Scopes {
		if i > 0 {
			scope += " "
		}
		scope += entry
	}
	pairs := [][2]string{
		{"client_id", ClientID},
		{"response_type", "code"},
		{"redirect_uri", redirectURI},
		{"scope", scope},
		{"code_challenge", challenge},
		{"code_challenge_method", "S256"},
	}
	if state != "" {
		pairs = append(pairs, [2]string{"state", state})
	}
	pairs = append(pairs, [2]string{"access_type", "offline"}, [2]string{"prompt", "consent"})
	return AuthorizeURL + "?" + formEncode(pairs)
}

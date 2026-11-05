// Package antigravity is the Go port of NeoPi's google-antigravity credential
// lifecycle: the `oauth-code` sign-in (PKCE authorize URL, token exchange,
// userinfo, Cloud Code Assist project discovery and free-tier onboarding), the
// refresh grant, and the usage report with its quota-counter helpers.
package antigravity

// NeoPi's `auth "google-antigravity"` rule (catalog/compat/rules/auth).
const (
	// ClientID and ClientSecret are the rule's base64-decoded OAuth client.
	ClientID     = "1071006060591-tmhssin2h21lcre235vtolojh4g403ep.apps.googleusercontent.com"
	ClientSecret = "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"

	// CallbackPort, CallbackHost and CallbackPath are the rule's loopback
	// callback. The redirect URI is `http://127.0.0.1:<port>/oauth-callback`;
	// another free port serves when the preferred one is taken.
	CallbackPort = 51121
	CallbackHost = "127.0.0.1"
	CallbackPath = "/oauth-callback"

	AuthorizeURL = "https://accounts.google.com/o/oauth2/v2/auth"
	TokenURL     = "https://oauth2.googleapis.com/token"
	UserInfoURL  = "https://www.googleapis.com/oauth2/v1/userinfo?alt=json"

	// CloudCodeEndpoint hosts loadCodeAssist and onboardUser.
	CloudCodeEndpoint = "https://daily-cloudcode-pa.googleapis.com"

	// expirySkewMs is the rule's `expires "seconds" skew-ms`.
	expirySkewMs = 300000
	// bunUserAgent is Bun's default fetch identity, which NeoPi's plain
	// token and userinfo requests carry.
	bunUserAgent = "Bun/1.3.14"
)

// Scopes is the rule's scope list, joined with a space in the authorize URL.
var Scopes = []string{
	"https://www.googleapis.com/auth/cloud-platform",
	"https://www.googleapis.com/auth/userinfo.email",
	"https://www.googleapis.com/auth/userinfo.profile",
	"https://www.googleapis.com/auth/cclog",
	"https://www.googleapis.com/auth/experimentsandconfigs",
	"https://www.googleapis.com/auth/aicode",
}

// Provider is NeoPi's provider id for the credential.
const Provider = "google-antigravity"

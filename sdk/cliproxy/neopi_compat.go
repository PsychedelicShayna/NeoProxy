package cliproxy

import (
	"net/url"
	"regexp"
	"strings"

	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

var compatEntrySuffix = regexp.MustCompile(`-\d+$`)

// neoPiCompatProvider names the NeoPi-ported provider an API-key account of
// the openai-compatibility engine belongs to ("nous-portal" or "marineris"),
// or "" for any other account. Entries are named after their catalogue id
// (with a -N suffix when several keys share it); a Nous Portal key stored
// under another name is still recognised by its fixed inference address.
func neoPiCompatProvider(a *coreauth.Auth) string {
	if a == nil {
		return ""
	}
	name := strings.ToLower(strings.TrimSpace(a.Attributes["compat_name"]))
	name = compatEntrySuffix.ReplaceAllString(strings.TrimPrefix(name, "openai-compatible-"), "")
	switch name {
	case "nous-portal", "marineris":
		return name
	}
	if parsed, err := url.Parse(strings.TrimSpace(a.Attributes["base_url"])); err == nil && strings.EqualFold(parsed.Hostname(), "inference-api.nousresearch.com") {
		return "nous-portal"
	}
	return ""
}

// neoPiCompatDiscovery is the NeoPi discovery that lists the models of an
// API-key account of a NeoPi-ported compat provider, or nil.
func neoPiCompatDiscovery(a *coreauth.Auth) *neoPiDiscovery {
	switch neoPiCompatProvider(a) {
	case "nous-portal":
		return nousDiscovery
	case "marineris":
		return marinerisDiscovery
	}
	return nil
}

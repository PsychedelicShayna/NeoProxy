package config

import (
	"reflect"
	"strings"
)

// ForAPIKey returns a request-local configuration view without v8 OAuth-only
// overrides. Legacy global settings and explicit API-key settings keep their
// existing semantics. The shared configuration is never modified.
func (cfg *Config) ForAPIKey() *Config {
	if cfg == nil || len(cfg.OAuthOnlyFields) == 0 {
		return cfg
	}
	filtered := *cfg
	value := reflect.ValueOf(&filtered).Elem()
	for _, path := range v8Paths {
		if !strings.HasPrefix(path.current, "oauth.providers.") || !cfg.OAuthOnlyFields[path.old] {
			continue
		}
		value.FieldByIndex(v8FieldIndexes[path.old]).SetZero()
	}
	filtered.OAuthOnlyFields = nil
	return &filtered
}

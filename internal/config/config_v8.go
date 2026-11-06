package config

import (
	"fmt"
	"reflect"
	"strings"
)

type configPath struct{ old, current string }

var v8FieldIndexes = make(map[string][]int)
var v8StructPaths []configPath
var v8Paths = buildV8Paths()

// Historical spellings are import-boundary aliases, not provider runtime fields.
// The canonical client path wins by presence (including explicit false/null).
// If it is absent, prefer the previous v8 OAuth path, then providers, then codex.
var v8ClientPaths = []configPath{
	{"oauth.providers.codex.optimize-multi-agent-v2", "client.codex.optimize-multi-agent-v2"},
	{"providers.codex.optimize-multi-agent-v2", "client.codex.optimize-multi-agent-v2"},
	{"codex.optimize-multi-agent-v2", "client.codex.optimize-multi-agent-v2"},
}

// Canonical upstream fields win by presence; historical OAuth fields precede globals.
var v8SharedPaths = []configPath{
	{"oauth.providers.codex.disable-codex-cloaking", "upstream.codex.disable-codex-cloaking"},
	{"oauth.providers.codex.stream-bootstrap-buffering", "upstream.codex.stream-bootstrap-buffering"},
	{"oauth.providers.codex.stream-bootstrap-timeout", "upstream.codex.stream-bootstrap-timeout"},
	{"oauth.providers.codex.orphan-delegation-compatibility", "upstream.codex.orphan-delegation-compatibility"},
	{"oauth.providers.codex.model-level-cooling", "upstream.codex.model-level-cooling"},
	{"oauth.providers.codex.response-steering", "upstream.codex.response-steering"},
	{"oauth.providers.claude.model-level-cooling", "upstream.claude.model-level-cooling"},
	{"oauth.providers.claude.claude-code.disable-cloaking-model-list", "upstream.claude.disable-cloaking-model-list"},
	{"oauth.providers.claude.disable-claude-cloak-mode", "upstream.claude.disable-claude-cloak-mode"},
	{"oauth.providers.claude.header-defaults.user-agent", "upstream.claude.header-defaults.user-agent"},
	{"oauth.providers.claude.header-defaults.package-version", "upstream.claude.header-defaults.package-version"},
	{"oauth.providers.claude.header-defaults.runtime-version", "upstream.claude.header-defaults.runtime-version"},
	{"oauth.providers.claude.header-defaults.os", "upstream.claude.header-defaults.os"},
	{"oauth.providers.claude.header-defaults.arch", "upstream.claude.header-defaults.arch"},
	{"oauth.providers.claude.header-defaults.timeout", "upstream.claude.header-defaults.timeout"},
	{"oauth.providers.claude.header-defaults.timezone", "upstream.claude.header-defaults.timezone"},
	{"oauth.providers.claude.header-defaults.stabilize-device-profile", "upstream.claude.header-defaults.stabilize-device-profile"},
	{"oauth.providers.xai.inject-x-search", "upstream.xai.inject-x-search"},
}

var v8Aliases = append(append([]configPath(nil), v8ClientPaths...), v8SharedPaths...)

var v8SharedStructPaths = []configPath{
	{"oauth.providers.claude.header-defaults", "upstream.claude.header-defaults"},
	{"oauth.providers.claude.claude-code", "upstream.claude"},
	{"oauth.providers.claude", "upstream.claude"},
	{"oauth.providers.xai", "upstream.xai"},
}

var v8KeyFamilies = []configPath{
	{"gemini-api-key", "gemini"}, {"interactions-api-key", "interactions"},
	{"vertex-api-key", "vertex"}, {"codex-api-key", "codex"},
	{"claude-api-key", "claude"}, {"xai-api-key", "xai"}, {"meta-api-key", "meta"}, {"gitlab-duo-agent-api-key", "gitlab-duo-agent"},
	{"openai-compatibility", "openai-compatibility"},
}

func buildV8Paths() []configPath {
	prefixes := []configPath{
		{"host", "server.host"}, {"port", "server.port"}, {"max-request-body-bytes", "server.max-request-body-bytes"}, {"trusted-proxies", "server.trusted-proxies"},
		{"github-token", "server.github-token"},
		{"tls", "server.tls"}, {"commercial-mode", "server.commercial-mode"}, {"discovery", "server.discovery"},
		{"remote-management", "management"}, {"api-keys", "access.api-keys"},
		{"credential-concurrency", "credentials.concurrency"}, {"credential-in-flight", "credentials.in-flight"},
		{"force-model-prefix", "routing.force-model-prefix"},
		{"request-retry", "routing.retry.request-retry"}, {"max-retry-credentials", "routing.retry.max-retry-credentials"},
		{"max-retry-interval", "routing.retry.max-retry-interval"},
		{"disable-cooling", "routing.cooldown.disable-cooling"}, {"save-cooldown-status", "routing.cooldown.save-cooldown-status"},
		{"transient-error-cooldown-seconds", "routing.cooldown.transient-error-cooldown-seconds"},
		{"proxy-url", "requests.proxy-url"}, {"tls-handshake-timeout-seconds", "requests.tls-handshake-timeout-seconds"}, {"passthrough-headers", "requests.passthrough-headers"},
		{"nonstream-keepalive-interval", "requests.nonstream-keepalive-interval"}, {"streaming", "requests.streaming"}, {"payload", "requests.payload"},
		{"auth-dir", "oauth.auth-dir"}, {"auth-auto-refresh-workers", "oauth.auth-auto-refresh-workers"},
		{"oauth-model-alias", "oauth.model-alias"}, {"oauth-excluded-models", "oauth.excluded-models"},
		{"oauth-request-scoped-errors", "oauth.request-scoped-errors"}, {"oauth-settings", "oauth.settings"}, {"ws-auth", "oauth.providers.aistudio.ws-auth"},
		{"codex.disable-codex-cloaking", "upstream.codex.disable-codex-cloaking"},
		{"codex.stream-bootstrap-buffering", "upstream.codex.stream-bootstrap-buffering"},
		{"codex.stream-bootstrap-timeout", "upstream.codex.stream-bootstrap-timeout"},
		{"codex.orphan-delegation-compatibility", "upstream.codex.orphan-delegation-compatibility"},
		{"codex.model-level-cooling", "upstream.codex.model-level-cooling"},
		{"codex.response-steering", "upstream.codex.response-steering"},
		{"codex", "oauth.providers.codex"},
		{"claude", "upstream.claude"}, {"claude-code", "upstream.claude"},
		{"disable-claude-cloak-mode", "upstream.claude.disable-claude-cloak-mode"},
		{"claude-header-defaults", "upstream.claude.header-defaults"},
		{"antigravity", "oauth.providers.antigravity"},
		{"antigravity-signature-cache-enabled", "oauth.providers.antigravity.signature-cache-enabled"},
		{"antigravity-signature-bypass-strict", "oauth.providers.antigravity.signature-bypass-strict"},
		{"quota-exceeded.antigravity-credits", "oauth.providers.antigravity.antigravity-credits"},
		{"xai", "upstream.xai"},
		{"disable-image-generation", "multimedia.disable-image-generation"}, {"gpt-image-2-base-model", "multimedia.gpt-image-2-base-model"},
		{"video-result-auth-cache-ttl", "multimedia.video-result-auth-cache-ttl"},
		{"debug", "observability.logs.debug"}, {"logging-to-file", "observability.logs.logging-to-file"},
		{"logs-max-total-size-mb", "observability.logs.logs-max-total-size-mb"}, {"request-log", "observability.logs.request-log"},
		{"error-logs-max-files", "observability.logs.error-logs-max-files"},
		{"usage-statistics-enabled", "observability.usage.usage-statistics-enabled"},
		{"redis-usage-queue-retention-seconds", "observability.usage.redis-usage-queue-retention-seconds"}, {"pprof", "observability.pprof"},
	}
	var out []configPath
	var walk func(reflect.Type, string, []int)
	walk = func(t reflect.Type, path string, indexes []int) {
		if t.Kind() == reflect.Struct {
			for _, prefix := range prefixes {
				if path == prefix.old || strings.HasPrefix(path, prefix.old+".") {
					v8StructPaths = append(v8StructPaths, configPath{path, prefix.current + strings.TrimPrefix(path, prefix.old)})
					break
				}
			}
			for i := 0; i < t.NumField(); i++ {
				field := t.Field(i)
				tag := strings.Split(field.Tag.Get("toml"), ",")[0]
				if tag == "-" || field.PkgPath != "" {
					continue
				}
				fieldIndexes := append(append([]int(nil), indexes...), i)
				if field.Anonymous {
					walk(field.Type, path, fieldIndexes)
					continue
				}
				child := tag
				if path != "" {
					child = path + "." + tag
				}
				walk(field.Type, child, fieldIndexes)
			}
			return
		}
		for _, prefix := range prefixes {
			if path == prefix.old || strings.HasPrefix(path, prefix.old+".") {
				out = append(out, configPath{path, prefix.current + strings.TrimPrefix(path, prefix.old)})
				v8FieldIndexes[path] = indexes
				return
			}
		}
	}
	walk(reflect.TypeOf(Config{}), "", nil)
	return out
}

func mapPath(root map[string]any, path string) (any, bool) {
	var value any = root
	for _, part := range strings.Split(path, ".") {
		m, ok := value.(map[string]any)
		if !ok {
			return nil, false
		}
		value, ok = m[part]
		if !ok {
			return nil, false
		}
	}
	return value, true
}
func setMapPath(root map[string]any, path string, value any) {
	parts := strings.Split(path, ".")
	for _, part := range parts[:len(parts)-1] {
		child, ok := root[part].(map[string]any)
		if !ok {
			child = map[string]any{}
			root[part] = child
		}
		root = child
	}
	root[parts[len(parts)-1]] = value
}
func deleteMapPath(root map[string]any, path string) bool {
	parts := strings.SplitN(path, ".", 2)
	if _, ok := root[parts[0]]; !ok {
		return false
	}
	if len(parts) == 2 {
		child, ok := root[parts[0]].(map[string]any)
		if !ok || !deleteMapPath(child, parts[1]) {
			return false
		}
		if len(child) > 0 {
			return true
		}
	}
	delete(root, parts[0])
	return true
}
func cloneMap(root map[string]any) map[string]any {
	out := make(map[string]any, len(root))
	for k, v := range root {
		out[k] = cloneNative(v)
	}
	return out
}
func cloneNative(v any) any {
	switch value := v.(type) {
	case map[string]any:
		return cloneMap(value)
	case []any:
		out := make([]any, len(value))
		for i, item := range value {
			out[i] = cloneNative(item)
		}
		return out
	default:
		return v
	}
}

func flattenV8Map(source map[string]any) (map[string]any, error) {
	root := cloneMap(source)
	if version, ok := root["config-version"]; ok {
		valid := false
		switch value := version.(type) {
		case int:
			valid = value == 8
		case int64:
			valid = value == 8
		}
		if !valid {
			return nil, fmt.Errorf("unsupported config-version (expected 8)")
		}
	}
	if err := NormalizeV8ConfigAliases(root); err != nil {
		return nil, err
	}
	for _, path := range v8Paths {
		parts := strings.Split(path.current, ".")
		for i := 1; i < len(parts); i++ {
			prefix := strings.Join(parts[:i], ".")
			if value, ok := mapPath(root, prefix); ok {
				// Only the shared legacy routing root accepts null as defaults.
				if value == nil && prefix == "routing" {
					break
				}
				if _, ok = value.(map[string]any); !ok {
					return nil, fmt.Errorf("%s must be a mapping", prefix)
				}
			}
		}
	}
	if _, ok := root["api-keys"].(map[string]any); ok {
		delete(root, "api-keys")
	}
	for _, path := range v8Paths {
		if value, ok := mapPath(root, path.current); ok {
			deleteMapPath(root, path.current)
			setMapPath(root, path.old, value)
		}
	}
	for _, family := range v8KeyFamilies {
		if groups, ok := mapPath(source, "api-keys."+family.current); ok {
			keys, err := expandV8MapGroups(groups, family.current)
			if err != nil {
				return nil, err
			}
			root[family.old] = keys
		}
	}
	return root, nil
}

var sharedKeyFields = map[string]bool{"priority": true, "prefix": true, "proxy-url": true, "headers": true, "models": true, "excluded-models": true, "disable-cooling": true, "request-retry": true, "request-scoped-errors": true}

func expandV8MapGroups(value any, provider string) ([]any, error) {
	groups, ok := value.([]any)
	if !ok {
		return nil, fmt.Errorf("api-keys.%s must be a list", provider)
	}
	out := []any{}
	for index, raw := range groups {
		group, ok := raw.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("api-keys.%s[%d] must be a mapping", provider, index)
		}
		keys, ok := group["keys"].([]any)
		if !ok {
			return nil, fmt.Errorf("api-keys.%s[%d].keys must be a list", provider, index)
		}
		if provider == "openai-compatibility" {
			item := cloneMap(group)
			delete(item, "keys")
			delete(item, "auth_index")
			delete(item, "auth-index")
			clean := cloneNative(keys).([]any)
			for _, v := range clean {
				if m, ok := v.(map[string]any); ok {
					delete(m, "auth_index")
					delete(m, "auth-index")
				}
			}
			item["api-key-entries"] = clean
			out = append(out, item)
			continue
		}
		for field := range group {
			if field != "name" && field != "base-url" && field != "keys" && !sharedKeyFields[field] {
				return nil, fmt.Errorf("api-keys.%s: unsupported group field %s", provider, field)
			}
		}
		for _, rawKey := range keys {
			key, ok := rawKey.(map[string]any)
			if !ok {
				return nil, fmt.Errorf("api-keys.%s key must be a mapping", provider)
			}
			if _, ok = key["base-url"]; ok {
				return nil, fmt.Errorf("api-keys.%s: base-url belongs to the group", provider)
			}
			item := map[string]any{}
			for field, v := range group {
				if field == "base-url" || sharedKeyFields[field] {
					item[field] = cloneNative(v)
				}
			}
			for field, v := range key {
				if field != "auth_index" && field != "auth-index" && v != nil {
					item[field] = cloneNative(v)
				}
			}
			out = append(out, item)
		}
	}
	return out, nil
}
func groupLegacyMapKeys(value any, provider string) []any {
	keys, _ := value.([]any)
	groups := make([]any, 0, len(keys))
	for i, raw := range keys {
		key, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		group := map[string]any{}
		if provider == "openai-compatibility" {
			group = cloneMap(key)
			entries := group["api-key-entries"]
			if entries == nil {
				entries = []any{}
			}
			group["keys"] = entries
			delete(group, "api-key-entries")
		} else {
			key = cloneMap(key)
			group["name"] = fmt.Sprintf("%s-%d", provider, i+1)
			for field, v := range key {
				if field == "base-url" || sharedKeyFields[field] {
					group[field] = v
					delete(key, field)
				}
			}
			group["keys"] = []any{key}
		}
		groups = append(groups, group)
	}
	return groups
}

// IsV8ConfigLayout identifies the canonical grouped configuration layout.
func IsV8ConfigLayout(root map[string]any) bool {
	if _, ok := root["config-version"]; ok {
		return true
	}
	for _, path := range []string{"server", "management", "access", "credentials", "requests", "oauth", "observability", "multimedia", "upstream", "providers", "models", "routing.retry", "routing.cooldown"} {
		if _, present := mapPath(root, path); present {
			return true
		}
	}
	for _, path := range append(append([]configPath{}, v8Paths...), v8Aliases...) {
		if !strings.Contains(path.current, ".") {
			continue
		}
		if _, ok := mapPath(root, path.current); ok {
			return true
		}
		if _, ok := mapPath(root, path.old); ok && strings.HasPrefix(path.old, "providers.") {
			return true
		}
	}
	_, ok := root["api-keys"].(map[string]any)
	return ok
}

// NormalizeConfigLayout drops overridden legacy fields and optionally moves legacy-only values.
func NormalizeConfigLayout(data []byte, migrate bool) ([]byte, bool, error) {
	root, err := ParseConfigDocument(data)
	if err != nil {
		return nil, false, err
	}
	if _, err = flattenV8Map(root); err != nil {
		return nil, false, err
	}
	changed := false
	if migrate {
		if err = NormalizeV8ConfigAliases(root); err != nil {
			return nil, false, err
		}
	} else if _, present := mapPath(root, "oauth.providers.codex.live-media-relay.disable-private-remote-ips"); present {
		changed = deleteMapPath(root, "codex.live-media-relay.allow-private-remote-ips")
	}
	paths := append(append([]configPath{}, v8Aliases...), v8Paths...)
	for _, path := range append(append([]configPath{}, v8StructPaths...), v8SharedStructPaths...) {
		if old, ok := mapPath(root, path.old); ok {
			if m, isMap := old.(map[string]any); old == nil || isMap && len(m) == 0 {
				paths = append(paths, path)
			}
		}
	}
	var archive []string
	for _, path := range paths {
		old, ok := mapPath(root, path.old)
		if !ok {
			continue
		}
		if path.old == "api-keys" {
			if _, collision := old.(map[string]any); collision {
				continue
			}
		}
		_, present := mapPath(root, path.current)
		if !present && !migrate {
			continue
		}
		deleteMapPath(root, path.old)
		if !present {
			if old == nil {
				old = map[string]any{}
			}
			setMapPath(root, path.current, old)
		}
		changed = true
	}
	for _, family := range v8KeyFamilies {
		old, ok := root[family.old]
		if !ok {
			continue
		}
		path := "api-keys." + family.current
		if _, present := mapPath(root, path); !present {
			if !migrate {
				continue
			}
			setMapPath(root, path, groupLegacyMapKeys(old, family.current))
		}
		delete(root, family.old)
		changed = true
	}
	if migrate {
		comments, err := archiveUnknownConfig(root, v8ConfigSchema, "")
		if err != nil {
			return nil, false, err
		}
		archive = comments
		root["config-version"] = int64(8)
		changed = true
	}
	if !changed {
		return data, false, nil
	}
	layout, err := configLayoutForRoot(data, root)
	if err != nil {
		return nil, false, err
	}
	layout.docFoot = append(layout.docFoot, archive...)
	w := nativeTOMLWriter{layout: layout}
	if err = w.table(nil, root, false); err != nil {
		return nil, false, err
	}
	w.comments(layout.docFoot)
	return w.buf.Bytes(), true, nil
}

// ValidateV8Config rejects legacy paths and unknown canonical fields.
func ValidateV8Config(data []byte) error {
	root, err := ParseConfigDocument(data)
	if err != nil {
		return err
	}
	for _, path := range append(append(append(append([]configPath{}, v8Paths...), v8Aliases...), v8SharedStructPaths...), v8StructPaths...) {
		if value, ok := mapPath(root, path.old); ok {
			if path.old == "api-keys" {
				if _, collision := value.(map[string]any); collision {
					continue
				}
			}
			return fmt.Errorf("legacy field %s is not accepted by v8; use %s", path.old, path.current)
		}
	}
	if err = validateConfigSchema(root, v8ConfigSchema, ""); err != nil {
		return err
	}
	flat, err := flattenV8Map(root)
	if err != nil {
		return err
	}
	delete(flat, "config-version")
	for _, entry := range v8Paths {
		parts := strings.Split(entry.current, ".")
		for end := len(parts) - 1; end > 0; end-- {
			path := strings.Join(parts[:end], ".")
			if value, present := mapPath(flat, path); present {
				if container, ok := value.(map[string]any); ok && len(container) == 0 {
					deleteMapPath(flat, path)
				}
			}
		}
	}
	if err = validateCredentialWeightMap(flat); err != nil {
		return err
	}
	var cfg Config
	if err = decodeNativeValue(reflect.ValueOf(&cfg).Elem(), flat, "", true); err != nil {
		return err
	}
	return cfg.Models.Validate()
}

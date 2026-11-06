package config

import (
	"bytes"
	"fmt"
	"os"
	"reflect"
	"strings"
)

// SaveConfigPreserveComments edits native TOML while retaining existing comments
// and key ordering. Explicit v8 writes migrate the effective legacy layout.
func SaveConfigPreserveComments(configFile string, cfg *Config, migrateV8 ...bool) error {
	if cfg == nil {
		return fmt.Errorf("nil config")
	}
	resolved, err := ResolveConfigPath(configFile)
	if err != nil {
		return err
	}
	data, err := os.ReadFile(resolved)
	if err != nil {
		return err
	}
	layout, err := ParseConfigDocument(data)
	if err != nil {
		return err
	}
	original, err := flattenV8Map(layout)
	if err != nil {
		return err
	}
	generatedValue, err := nativeConfigValue(reflect.ValueOf(cfg))
	if err != nil {
		return err
	}
	generated := generatedValue.(map[string]any)
	preserveClearedSlices(reflect.ValueOf(cfg), "", original, generated)
	var defaults Config
	applyLoadDefaults(&defaults)
	defaults.CredentialConcurrency = defaults.CredentialConcurrency.WithDefaults()
	defaults.NormalizePluginsConfig()
	defaults.NormalizeClaudeConfig()
	defaultsValue, err := nativeConfigValue(reflect.ValueOf(defaults))
	if err != nil {
		return err
	}
	explicit := make(map[string]bool)
	configPointerPaths(reflect.ValueOf(cfg), "", explicit)
	// Canonical empty OAuth maps deliberately shadow legacy spellings. Runtime
	// normalization may turn them into nil, so retain their source presence when
	// saving a v0 mutation instead of accidentally reviving a legacy fallback.
	for _, setting := range []configPath{
		{"oauth-excluded-models", "oauth.excluded-models"},
		{"oauth-model-alias", "oauth.model-alias"},
		{"oauth-request-scoped-errors", "oauth.request-scoped-errors"},
		{"oauth-settings", "oauth.settings"},
	} {
		if _, present := mapPath(layout, setting.current); present {
			explicit[setting.old] = true
			if _, generatedPresent := generated[setting.old]; !generatedPresent {
				generated[setting.old] = map[string]any{}
			}
		}
	}
	mergeNativeConfig(original, generated, defaultsValue.(map[string]any), "", explicit)
	migrating := len(migrateV8) > 0 && migrateV8[0] || IsV8ConfigLayout(layout)
	if !migrating {
		for _, path := range []string{"auth", "ampcode", "amp-upstream-url", "amp-upstream-api-key", "amp-restrict-management-to-localhost", "amp-model-mappings", "generative-language-api-key", "codex-header-defaults", "devin", "models.devin-catalog"} {
			deleteMapPath(original, path)
		}
	}
	for _, path := range v8Paths {
		_, present := mapPath(layout, path.current)
		if !present && !migrating {
			continue
		}
		if value, ok := mapPath(original, path.old); ok {
			if path.old == "api-keys" {
				if _, collision := value.(map[string]any); collision {
					continue
				}
			}
			deleteMapPath(original, path.old)
			setMapPath(original, path.current, value)
		}
	}
	// Compare normalized credentials rather than their raw source syntax.
	var baseline map[string]any
	for _, family := range v8KeyFamilies {
		if groups, present := mapPath(layout, "api-keys."+family.current); present || migrating {
			keys, exists := original[family.old]
			if !exists {
				continue
			}
			if present {
				if baseline == nil {
					before, err := ParseConfigBytes(data)
					if err != nil {
						return err
					}
					value, err := nativeConfigValue(reflect.ValueOf(before))
					if err != nil {
						return err
					}
					baseline = value.(map[string]any)
				}
				before, after := baseline[family.old], generated[family.old]
				if before == nil {
					before = []any{}
				}
				if after == nil {
					after = []any{}
				}
				if reflect.DeepEqual(before, after) {
					setMapPath(original, "api-keys."+family.current, cloneNative(groups))
				} else {
					setMapPath(original, "api-keys."+family.current, groupLegacyMapKeys(keys, family.current))
				}
			} else {
				setMapPath(original, "api-keys."+family.current, groupLegacyMapKeys(keys, family.current))
			}
			delete(original, family.old)
		}
	}
	if !migrating {
		for _, alias := range v8ClientPaths {
			if _, old := mapPath(layout, alias.old); old {
				if _, current := mapPath(layout, alias.current); !current {
					if value, ok := mapPath(original, alias.current); ok {
						deleteMapPath(original, alias.current)
						setMapPath(original, alias.old, value)
					}
				}
			}
		}
	}
	out, err := renderNativePreserve(original, data)
	if err != nil {
		return err
	}
	if migrating {
		out, _, err = NormalizeConfigLayout(out, true)
		if err != nil {
			return err
		}
	}
	if err = WriteConfigFile(resolved, out, 0600); err != nil {
		return err
	}
	if migrating {
		migrated, err := DecodeConfig(out, ConfigFormatTOML)
		if err != nil {
			return err
		}
		cfg.OAuthOnlyFields = migrated.OAuthOnlyFields
	}
	return nil
}

func mergeNativeConfig(dst, src, defaults map[string]any, path string, explicit map[string]bool) {
	for key, value := range src {
		child := key
		if path != "" {
			child = path + "." + key
		}
		if child == "plugins.configs" || child == "oauth-excluded-models" || child == "oauth-model-alias" || child == "oauth-request-scoped-errors" || child == "oauth-settings" {
			if nativeEmpty(value) && !explicit[child] {
				delete(dst, key)
				continue
			}
			if existing, ok := dst[key]; ok || !nativeEmpty(value) {
				if !ok || !reflect.DeepEqual(existing, value) {
					dst[key] = cloneNative(value)
				}
			}
			continue
		}
		if nested, ok := value.(map[string]any); ok {
			old, exists := dst[key].(map[string]any)
			if !exists {
				old = map[string]any{}
			}
			def, _ := defaults[key].(map[string]any)
			mergeNativeConfig(old, nested, def, child, explicit)
			if len(old) > 0 || exists {
				dst[key] = old
			}
			continue
		}
		_, present := dst[key]
		if !present && (reflect.DeepEqual(value, defaults[key]) || nativeEmpty(value) && !explicit[child]) {
			continue
		}
		dst[key] = cloneNative(value)
	}
	for _, key := range []string{"oauth-excluded-models", "oauth-model-alias", "oauth-request-scoped-errors", "oauth-settings"} {
		if path == "" {
			if _, present := src[key]; !present {
				delete(dst, key)
			}
		}
	}
	if path == "plugins" {
		if _, present := src["configs"]; !present {
			delete(dst, "configs")
		}
	}
}
func nativeEmpty(value any) bool {
	if value == nil {
		return true
	}
	v := reflect.ValueOf(value)
	switch v.Kind() {
	case reflect.Map, reflect.Slice:
		return v.Len() == 0
	default:
		return v.IsZero()
	}
}

func configPointerPaths(value reflect.Value, path string, explicit map[string]bool) {
	if value.Kind() == reflect.Pointer {
		if value.IsNil() {
			return
		}
		if path != "" {
			explicit[path] = true
		}
		value = value.Elem()
	}
	if value.Kind() != reflect.Struct {
		return
	}
	for i := 0; i < value.NumField(); i++ {
		field := value.Type().Field(i)
		if field.PkgPath != "" {
			continue
		}
		key := strings.Split(field.Tag.Get("toml"), ",")[0]
		if key == "-" {
			continue
		}
		if field.Anonymous {
			configPointerPaths(value.Field(i), path, explicit)
			continue
		}
		child := key
		if path != "" {
			child = path + "." + key
		}
		configPointerPaths(value.Field(i), child, explicit)
	}
}

// SaveConfigPreserveCommentsUpdateNestedScalar changes exactly one TOML value.
func SaveConfigPreserveCommentsUpdateNestedScalar(configFile string, path []string, value string) error {
	resolved, err := ResolveConfigPath(configFile)
	if err != nil {
		return err
	}
	data, err := os.ReadFile(resolved)
	if err != nil {
		return err
	}
	updated, err := updateNestedScalarTOML(data, path, value)
	if err != nil {
		return err
	}
	return WriteConfigFile(resolved, updated, 0600)
}
func updateNestedScalarTOML(data []byte, path []string, value string) ([]byte, error) {
	root, err := ParseConfigDocument(data)
	if err != nil {
		return nil, err
	}
	if len(path) == 0 {
		return nil, fmt.Errorf("empty config key path")
	}
	setMapPath(root, strings.Join(path, "."), value)
	return renderNativePreserve(root, data)
}

// NormalizeCommentIndentation is retained for source-compatible callers; TOML
// comments do not use indentation to identify their owning key.
func NormalizeCommentIndentation(data []byte) []byte {
	lines := bytes.Split(data, []byte("\n"))
	for i, line := range lines {
		trimmed := bytes.TrimLeft(line, " \t")
		if len(trimmed) > 0 && trimmed[0] == '#' {
			lines[i] = trimmed
		}
	}
	return bytes.Join(lines, []byte("\n"))
}

// Nil runtime slices mean cleared settings on persistence, but omitted settings
// in snapshots. Retain that distinction without serializing null into TOML.
func preserveClearedSlices(value reflect.Value, path string, original, generated map[string]any) {
	if value.Kind() == reflect.Pointer {
		if value.IsNil() {
			return
		}
		value = value.Elem()
	}
	if value.Kind() != reflect.Struct {
		return
	}
	for i := 0; i < value.NumField(); i++ {
		field := value.Type().Field(i)
		if field.PkgPath != "" {
			continue
		}
		key := strings.Split(field.Tag.Get("toml"), ",")[0]
		if key == "-" {
			continue
		}
		if field.Anonymous {
			preserveClearedSlices(value.Field(i), path, original, generated)
			continue
		}
		child := key
		if path != "" {
			child = path + "." + key
		}
		member := value.Field(i)
		if member.Kind() == reflect.Slice && member.IsNil() {
			if _, present := mapPath(original, child); present {
				setMapPath(generated, child, []any{})
			}
		} else {
			preserveClearedSlices(member, child, original, generated)
		}
	}
}

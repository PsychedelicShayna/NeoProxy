package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestSharedUpstreamConfigSurvivesMigrationAndSnapshots(t *testing.T) {
	legacy := []byte(`auth-dir: client-auth
auth-auto-refresh-workers: 3
codex: {disable-codex-cloaking: true, stream-bootstrap-buffering: true, stream-bootstrap-timeout: 10s, orphan-delegation-compatibility: true, model-level-cooling: true, response-steering: true}
claude: {model-level-cooling: true}
claude-code: {disable-cloaking-model-list: true}
disable-claude-cloak-mode: true
claude-header-defaults: {user-agent: client-agent, package-version: 1.2.3, runtime-version: v22.1.0, os: Linux, arch: arm64, timeout: '123', timezone: Asia/Shanghai, stabilize-device-profile: true}
xai: {inject-x-search: true}
oauth: {auth-dir: client-auth, auth-auto-refresh-workers: 3}
api-keys: {codex: [{base-url: https://example.invalid, keys: [{api-key: test-key, disable-codex-cloaking: false}]}]}
`)
	canonical := []byte(`[upstream.codex]
disable-codex-cloaking = true
stream-bootstrap-buffering = true
stream-bootstrap-timeout = "10s"
orphan-delegation-compatibility = true
model-level-cooling = true
response-steering = true
[upstream.claude]
model-level-cooling = true
disable-cloaking-model-list = true
disable-claude-cloak-mode = true
[upstream.claude.header-defaults]
user-agent = "client-agent"
package-version = "1.2.3"
runtime-version = "v22.1.0"
os = "Linux"
arch = "arm64"
timeout = "123"
timezone = "Asia/Shanghai"
stabilize-device-profile = true
[upstream.xai]
inject-x-search = true
[oauth]
auth-dir = "client-auth"
auth-auto-refresh-workers = 3
[[api-keys.codex]]
base-url = "https://example.invalid"
keys = [{api-key = "test-key", disable-codex-cloaking = false}]
`)
	historical := []byte(`[oauth]
auth-dir = "client-auth"
auth-auto-refresh-workers = 3
[oauth.providers.codex]
disable-codex-cloaking = true
stream-bootstrap-buffering = true
stream-bootstrap-timeout = "10s"
orphan-delegation-compatibility = true
model-level-cooling = true
response-steering = true
[oauth.providers.claude]
model-level-cooling = true
disable-claude-cloak-mode = true
[oauth.providers.claude.claude-code]
disable-cloaking-model-list = true
[oauth.providers.claude.header-defaults]
user-agent = "client-agent"
package-version = "1.2.3"
runtime-version = "v22.1.0"
os = "Linux"
arch = "arm64"
timeout = "123"
timezone = "Asia/Shanghai"
stabilize-device-profile = true
[oauth.providers.xai]
inject-x-search = true
[[api-keys.codex]]
base-url = "https://example.invalid"
keys = [{api-key = "test-key", disable-codex-cloaking = false}]
`)
	legacyTOML, errImport := YAMLToTOML(legacy)
	if errImport != nil {
		t.Fatal(errImport)
	}
	expected := []any{"client-auth", 3, true, true, "10s", true, true, true, true, true, true, "client-agent", "1.2.3", "v22.1.0", "Linux", "arm64", "123", "Asia/Shanghai", true, true}
	for _, tc := range []struct {
		name string
		raw  []byte
	}{
		{"legacy import", legacyTOML}, {"upstream", canonical}, {"historical OAuth", historical},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg, errParse := ParseConfigBytes(tc.raw)
			if errParse != nil {
				t.Fatal(errParse)
			}
			migrated, _, errMigrate := NormalizeConfigLayout(tc.raw, true)
			if errMigrate != nil {
				t.Fatal(errMigrate)
			}
			if errValidate := ValidateV8Config(migrated); errValidate != nil {
				t.Fatal(errValidate)
			}
			doc, errDecode := ParseConfigDocument(migrated)
			if errDecode != nil {
				t.Fatal(errDecode)
			}
			_, hasAuthDir := mapPath(doc, "oauth.auth-dir")
			_, hasTimezone := mapPath(doc, "upstream.claude.header-defaults.timezone")
			if !hasAuthDir || !hasTimezone {
				t.Fatal("migration did not separate shared upstream fields from OAuth settings")
			}
			for _, alias := range v8Aliases {
				if _, present := mapPath(doc, alias.old); present {
					t.Fatalf("historical alias %s survived migration", alias.old)
				}
			}
			snapshot, errMarshal := MarshalTOML(cfg)
			if errMarshal != nil {
				t.Fatal(errMarshal)
			}
			path := filepath.Join(t.TempDir(), "config.toml")
			if errWrite := os.WriteFile(path, tc.raw, 0600); errWrite != nil {
				t.Fatal(errWrite)
			}
			if errSave := SaveConfigPreserveComments(path, cfg, true); errSave != nil {
				t.Fatal(errSave)
			}
			saved, errRead := os.ReadFile(path)
			if errRead != nil {
				t.Fatal(errRead)
			}
			for name, raw := range map[string][]byte{"snapshot": snapshot, "migrated": migrated, "saved": saved} {
				restored, errParse := ParseConfigBytes(raw)
				if errParse != nil {
					t.Fatal(errParse)
				}
				for _, view := range []*Config{restored, restored.CloneForRuntime()} {
					for _, scoped := range []*Config{view, view.ForAPIKey()} {
						actual := []any{
							scoped.AuthDir, scoped.AuthAutoRefreshWorkers,
							scoped.Codex.DisableCodexCloaking, scoped.Codex.StreamBootstrapBuffering, scoped.Codex.StreamBootstrapTimeout,
							scoped.Codex.OrphanDelegationCompatibility, scoped.Codex.ModelLevelCooling, scoped.Codex.ResponseSteering,
							scoped.Claude.ModelLevelCooling, scoped.ClaudeCode.DisableCloakingModelList, scoped.DisableClaudeCloakMode,
							scoped.ClaudeHeaderDefaults.UserAgent, scoped.ClaudeHeaderDefaults.PackageVersion, scoped.ClaudeHeaderDefaults.RuntimeVersion,
							scoped.ClaudeHeaderDefaults.OS, scoped.ClaudeHeaderDefaults.Arch, scoped.ClaudeHeaderDefaults.Timeout, scoped.ClaudeHeaderDefaults.Timezone,
							scoped.ClaudeHeaderDefaults.StabilizeDeviceProfile != nil && *scoped.ClaudeHeaderDefaults.StabilizeDeviceProfile,
							scoped.XAI.InjectXSearch,
						}
						if !reflect.DeepEqual(actual, expected) {
							t.Fatalf("%s changed shared configuration: got %#v, want %#v", name, actual, expected)
						}
					}
					if len(view.CodexKey) != 1 || view.CodexKey[0].DisableCodexCloaking == nil || *view.CodexKey[0].DisableCodexCloaking {
						t.Fatal("explicit key override lost its scope")
					}
				}
			}
		})
	}
}

func TestSharedUpstreamPresenceWinsOverHistoricalAliases(t *testing.T) {
	for _, value := range []string{"false", "null"} {
		t.Run(value, func(t *testing.T) {
			raw := []byte(`[codex]
response-steering = true
[oauth.providers.codex]
response-steering = true
[oauth.providers.claude.header-defaults]
user-agent = "historical-agent"
stabilize-device-profile = true
[upstream.codex]
response-steering = false
[upstream.claude.header-defaults]
user-agent = ""
stabilize-device-profile = false
`)
			imported := raw
			if value == "null" {
				legacy := []byte("codex: {response-steering: true}\noauth: {providers: {codex: {response-steering: true}, claude: {header-defaults: {user-agent: historical-agent, stabilize-device-profile: true}}}}\nupstream: {codex: {response-steering: null}, claude: {header-defaults: {user-agent: '', stabilize-device-profile: null}}}\n")
				var errImport error
				imported, errImport = YAMLToTOML(legacy)
				if errImport != nil {
					t.Fatal(errImport)
				}
			}
			for _, migrate := range []bool{false, true} {
				data, _, errNormalize := NormalizeConfigLayout(imported, migrate)
				if errNormalize != nil {
					t.Fatal(errNormalize)
				}
				cfg, errParse := ParseConfigBytes(data)
				if errParse != nil {
					t.Fatal(errParse)
				}
				if cfg.Codex.ResponseSteering || cfg.ClaudeHeaderDefaults.UserAgent != "" || cfg.AuthAutoRefreshWorkers != 0 || (cfg.ClaudeHeaderDefaults.StabilizeDeviceProfile != nil && *cfg.ClaudeHeaderDefaults.StabilizeDeviceProfile) {
					t.Fatal("historical alias overrode an explicit shared zero value")
				}
				if migrate {
					if errValidate := ValidateV8Config(data); errValidate != nil {
						t.Fatal(errValidate)
					}
				}
			}
		})
	}
}

func TestSharedUpstreamEmptyHistoricalContainers(t *testing.T) {
	for _, tc := range []struct{ historical, current string }{
		{"oauth.providers.claude.header-defaults", "upstream.claude.header-defaults"},
		{"oauth.providers.claude.claude-code", "upstream.claude"},
		{"oauth.providers.claude", "upstream.claude"},
		{"oauth.providers.xai", "upstream.xai"},
	} {
		for _, value := range []string{"{}", "null"} {
			t.Run(tc.historical+"/"+value, func(t *testing.T) {
				root := make(map[string]any)
				setMapPath(root, tc.historical, map[string]any{})
				raw, errMarshal := RenderConfigDocument(root)
				if errMarshal != nil {
					t.Fatal(errMarshal)
				}
				if value == "null" {
					// Null exists only in legacy input; exercise it through the importer.
					legacy := "null"
					parts := strings.Split(tc.historical, ".")
					for i := len(parts) - 1; i >= 0; i-- {
						legacy = "{" + parts[i] + ": " + legacy + "}"
					}
					var errImport error
					raw, errImport = YAMLToTOML([]byte(legacy))
					if errImport != nil {
						t.Fatal(errImport)
					}
				}
				migrated, _, errMigrate := NormalizeConfigLayout(raw, true)
				if errMigrate != nil {
					t.Fatal(errMigrate)
				}
				if errValidate := ValidateV8Config(migrated); errValidate != nil {
					t.Fatal(errValidate)
				}
				doc, errDecode := ParseConfigDocument(migrated)
				if errDecode != nil {
					t.Fatal(errDecode)
				}
				_, hasHistorical := mapPath(doc, tc.historical)
				_, hasCurrent := mapPath(doc, tc.current)
				if hasHistorical || !hasCurrent {
					t.Fatal("empty historical provider container was not migrated")
				}
			})
		}
	}
}

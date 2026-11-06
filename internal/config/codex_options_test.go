package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Codex header defaults were retired once Codex OAuth started sending the
// client's own headers. Existing files must still load, and a save must not
// write the key back as an active setting.
func TestRetiredCodexHeaderDefaultsAreIgnored(t *testing.T) {
	for _, tc := range []struct {
		name, raw, retired string
		v8                 bool
	}{
		{"legacy", "\"port\" = 8317\n\"codex-header-defaults\" = {\"user-agent\" = \"old-agent\", \"beta-features\" = \"old-beta\"}\n\"codex\" = {\"disable-codex-cloaking\" = true}\n", "codex-header-defaults", false},
		{"v8", "\"config-version\" = 8\n\"server\" = {\"port\" = 8317}\n\"upstream\" = {\"codex\" = {\"disable-codex-cloaking\" = true}}\n\"oauth\" = {\"providers\" = {\"codex\" = {\"header-defaults\" = {\"user-agent\" = \"old-agent\", \"beta-features\" = \"old-beta\"}}}}\n", "oauth.providers.codex.header-defaults", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var warned []string
			SetV8MigrationWarnFunc(func(section, _ string) { warned = append(warned, section) })
			t.Cleanup(func() { SetV8MigrationWarnFunc(nil) })
			path := filepath.Join(t.TempDir(), "config.toml")
			if err := os.WriteFile(path, []byte(tc.raw), 0o600); err != nil {
				t.Fatal(err)
			}
			cfg, err := LoadConfig(path)
			if err != nil || cfg.Port != 8317 || !cfg.Codex.DisableCodexCloaking {
				t.Fatalf("load with retired key: cfg=%+v error=%v", cfg, err)
			}
			if err = SaveConfigPreserveComments(path, cfg); err != nil {
				t.Fatal(err)
			}
			saved, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			doc, err := ParseConfigDocument(saved)
			if err != nil {
				t.Fatal(err)
			}
			if _, exists := mapPath(doc, tc.retired); exists {
				t.Fatalf("save wrote retired %s back:\n%s", tc.retired, saved)
			}
			if tc.v8 {
				if !strings.Contains(string(saved), "# "+tc.retired+" =") || len(warned) != 1 || warned[0] != tc.retired {
					t.Fatalf("v8 save did not comment out retired key with a warning: warned=%v\n%s", warned, saved)
				}
				if err = ValidateV8Config(saved); err != nil {
					t.Fatalf("saved v8 config is invalid: %v", err)
				}
			} else if strings.Contains(string(saved), "old-agent") {
				t.Fatalf("legacy save kept retired values:\n%s", saved)
			}
			if reloaded, errReload := LoadConfig(path); errReload != nil || !reloaded.Codex.DisableCodexCloaking {
				t.Fatalf("reload saved config: cfg=%+v error=%v", reloaded, errReload)
			}
		})
	}
}

func TestLoadConfigOptional_CodexOptions(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.toml")
	configTOML := []byte(`"codex" = {"disable-codex-cloaking" = true, "optimize-multi-agent-v2" = true}
`)
	if err := os.WriteFile(configPath, configTOML, 0o600); err != nil {
		t.Fatalf("failed to write config: %v", err)
	}

	cfg, err := LoadConfigOptional(configPath, false)
	if err != nil {
		t.Fatalf("LoadConfigOptional() error = %v", err)
	}

	if !cfg.Codex.DisableCodexCloaking {
		t.Fatal("DisableCodexCloaking = false, want true")
	}
	if !cfg.Client.Codex.OptimizeMultiAgentV2 {
		t.Fatalf("OptimizeMultiAgentV2 = false, want true")
	}
}

func TestLoadConfigOptional_CodexModelLevelCooling(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.toml")
	configTOML := []byte(`"codex" = {"model-level-cooling" = true}
`)
	if err := os.WriteFile(configPath, configTOML, 0o600); err != nil {
		t.Fatalf("failed to write config: %v", err)
	}

	cfg, err := LoadConfigOptional(configPath, false)
	if err != nil {
		t.Fatalf("LoadConfigOptional() error = %v", err)
	}

	if !cfg.Codex.ModelLevelCooling {
		t.Fatalf("ModelLevelCooling = false, want true")
	}

	defaultCfg, errDefault := ParseConfigBytes([]byte(`
`))
	if errDefault != nil {
		t.Fatalf("ParseConfigBytes() error = %v", errDefault)
	}
	if defaultCfg.Codex.ModelLevelCooling {
		t.Fatalf("default ModelLevelCooling = true, want false")
	}
}

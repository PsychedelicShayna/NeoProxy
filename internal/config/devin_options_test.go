package config

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// models.devin-catalog and the devin block's sensitive-words were retired with
// the NeoPi Devin port. Existing files must still load, and a save must not
// write either back as an active setting: a legacy file drops them, and a file
// saved in the v8 layout (a models block migrates it) keeps them as warned
// comments.
func TestRetiredDevinKeysAreIgnored(t *testing.T) {
	for _, tc := range []struct {
		name, raw string
		retired   []string
		v8        bool
	}{
		{"legacy", "\"port\" = 8317\n\"devin\" = {\"sensitive-words\" = [\"secret-word\"]}\n",
			[]string{"devin"}, false},
		{"legacy with catalogs", "\"port\" = 8317\n\"models\" = {\"codex-catalog\" = \"https://example.com/codex.json\", \"devin-catalog\" = \"https://example.com/devin.json\"}\n\"devin\" = {\"sensitive-words\" = [\"secret-word\"]}\n",
			[]string{"devin", "models.devin-catalog"}, true},
		{"v8", "\"config-version\" = 8\n\"server\" = {\"port\" = 8317}\n\"models\" = {\"codex-catalog\" = \"https://example.com/codex.json\", \"devin-catalog\" = \"https://example.com/devin.json\"}\n\"oauth\" = {\"providers\" = {\"devin\" = {\"sensitive-words\" = [\"secret-word\"]}}}\n",
			[]string{"models.devin-catalog", "oauth.providers.devin"}, true},
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
			if err != nil || cfg.Port != 8317 {
				t.Fatalf("load with retired keys: cfg=%+v error=%v", cfg, err)
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
			for _, retired := range tc.retired {
				if _, exists := mapPath(doc, retired); exists {
					t.Fatalf("save wrote retired %s back:\n%s", retired, saved)
				}
			}
			if tc.v8 {
				slices.Sort(warned)
				if !slices.Equal(warned, tc.retired) {
					t.Fatalf("v8 save warned %v, want %v:\n%s", warned, tc.retired, saved)
				}
				if err = ValidateV8Config(saved); err != nil {
					t.Fatalf("saved v8 config is invalid: %v", err)
				}
			} else if strings.Contains(string(saved), "secret-word") || strings.Contains(string(saved), "devin.json") {
				t.Fatalf("legacy save kept retired values:\n%s", saved)
			}
			if reloaded, errReload := LoadConfig(path); errReload != nil || reloaded.Port != 8317 {
				t.Fatalf("reload saved config: cfg=%+v error=%v", reloaded, errReload)
			}
		})
	}
}

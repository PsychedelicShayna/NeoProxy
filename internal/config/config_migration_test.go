package config

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolveConfigPathImportsYAMLWithoutChangingSource(t *testing.T) {
	dir := t.TempDir()
	legacy := filepath.Join(dir, "config.yaml")
	canonical := filepath.Join(dir, "config.toml")
	source := []byte("# Existing deployment\nport: 8317\ndebug: true # Keep this note\n")
	if err := os.WriteFile(legacy, source, 0640); err != nil {
		t.Fatal(err)
	}
	resolved, err := ResolveConfigPath(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if resolved != canonical {
		t.Fatalf("resolved = %q, want %q", resolved, canonical)
	}
	imported, err := os.ReadFile(canonical)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(imported), "# Existing deployment") || !strings.Contains(string(imported), "# Keep this note") {
		t.Fatal("legacy comments were not retained")
	}
	cfg, err := DecodeConfig(imported, ConfigFormatTOML)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Port != 8317 || !cfg.Debug {
		t.Fatal("import changed effective configuration")
	}
	info, err := os.Stat(canonical)
	if err != nil {
		t.Fatal(err)
	}
	legacyInfo, err := os.Stat(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != legacyInfo.Mode().Perm() {
		t.Fatalf("imported mode = %v, source mode = %v", info.Mode().Perm(), legacyInfo.Mode().Perm())
	}
	unchanged, err := os.ReadFile(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(source, unchanged) {
		t.Fatal("legacy source was modified")
	}
	resolvedAgain, err := ResolveConfigPath(DefaultConfigFile(dir))
	if err != nil {
		t.Fatal(err)
	}
	if resolvedAgain != canonical {
		t.Fatal("canonical resolver changed path")
	}
	again, err := os.ReadFile(canonical)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(imported, again) {
		t.Fatal("repeated resolution rewrote canonical config")
	}
}

func TestResolveConfigPathTOMLWinsOverYAMLSibling(t *testing.T) {
	for _, requestedLegacy := range []bool{false, true} {
		t.Run(map[bool]string{false: "native-path", true: "legacy-path"}[requestedLegacy], func(t *testing.T) {
			dir := t.TempDir()
			legacy := filepath.Join(dir, "config.yaml")
			canonical := DefaultConfigFile(dir)
			source := []byte("not: [valid legacy YAML\n")
			native := []byte("# Canonical configuration\nport = 9443\n")
			if err := os.WriteFile(legacy, source, 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(canonical, native, 0640); err != nil {
				t.Fatal(err)
			}
			request := canonical
			if requestedLegacy {
				request = legacy
			}
			resolved, err := ResolveConfigPath(request)
			if err != nil {
				t.Fatal(err)
			}
			if resolved != canonical {
				t.Fatalf("resolved = %q, want canonical", resolved)
			}
			cfg, err := LoadConfig(request)
			if err != nil {
				t.Fatal(err)
			}
			if cfg.Port != 9443 {
				t.Fatalf("loaded port = %d, want TOML value", cfg.Port)
			}
			legacyAfter, err := os.ReadFile(legacy)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(legacyAfter, source) {
				t.Fatal("legacy source changed")
			}
			nativeAfter, err := os.ReadFile(canonical)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(nativeAfter, native) {
				t.Fatal("canonical config unnecessarily rewritten")
			}
		})
	}
}

func TestDefaultConfigPathImportsLegacySibling(t *testing.T) {
	dir := t.TempDir()
	legacy := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(legacy, []byte("port: 8123\n"), 0600); err != nil {
		t.Fatal(err)
	}
	canonical := DefaultConfigFile(dir)
	if filepath.Base(canonical) != "config.toml" {
		t.Fatal("default is not TOML")
	}
	cfg, err := LoadConfig(canonical)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Port != 8123 {
		t.Fatal("default path did not import legacy config")
	}
	resolved, err := ResolveConfigPath(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if resolved != canonical {
		t.Fatal("legacy caller did not resolve imported path")
	}
}

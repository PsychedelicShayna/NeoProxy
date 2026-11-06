package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func readExample(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", name))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func decodeTOMLTestConfig(data []byte, cfg *Config) error {
	decoded, err := DecodeConfig(data, ConfigFormatTOML)
	if err == nil {
		*cfg = *decoded
	}
	return err
}

func sameConfig(a, b Config) bool {
	return equivalentConfigValues(reflect.ValueOf(&a).Elem(), reflect.ValueOf(&b).Elem())
}
func TestTOMLRoundTripExample(t *testing.T) {
	source := readExample(t, "config.example.toml")
	cfg, err := DecodeConfig(source, ConfigFormatTOML)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := EncodeConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	restored, err := DecodeConfig(encoded, ConfigFormatTOML)
	if err != nil {
		t.Fatal(err)
	}
	if !sameConfig(*cfg, *restored) {
		t.Fatal("TOML round trip changed the decoded config")
	}
	again, err := EncodeConfig(restored)
	if err != nil {
		t.Fatal(err)
	}
	if string(encoded) != string(again) {
		t.Fatal("repeated TOML writes are not stable")
	}
}

func TestYAMLToTOMLNulls(t *testing.T) {
	// A null whose absence decodes the same is left out.
	out, err := YAMLToTOML([]byte("port: 8317\nproxy-url: null\n"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(out), "proxy-url") || strings.Contains(string(out), "null") {
		t.Fatalf("null kept:\n%s", out)
	}
	// Opaque plugin settings receive null as a value, so it cannot be dropped.
	if _, err = YAMLToTOML([]byte("plugins:\n  configs:\n    demo:\n      value: null\n")); err == nil || !strings.Contains(err.Error(), "plugins.configs.demo.value") {
		t.Fatalf("plugin null must be rejected naming the key, got %v", err)
	}
	// A null scalar leaves the default in place either way, so it is dropped.
	if _, err = YAMLToTOML([]byte("ws-auth: null\n")); err != nil {
		t.Fatalf("ws-auth: null decodes like absence, got %v", err)
	}
	// A null that clears a list with a default cannot be expressed.
	if _, err = YAMLToTOML([]byte("discovery:\n  subtypes: null\n")); err == nil || !strings.Contains(err.Error(), "discovery.subtypes") {
		t.Fatalf("discovery.subtypes: null must be rejected naming the key, got %v", err)
	}
	// A null array element would shift its siblings.
	if _, err = YAMLToTOML([]byte("api-keys: [a, null]\n")); err == nil || !strings.Contains(err.Error(), "null array element") {
		t.Fatalf("null array element must be rejected, got %v", err)
	}
}

func TestYAMLToTOMLAnchorsAndQuoting(t *testing.T) {
	out, err := YAMLToTOML([]byte("base: &b {x: 1}\nderived:\n  <<: *b\n  y: \"true\"\nweird key: 2\n"))
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	got, err = ParseConfigDocument(out)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]any{"base": map[string]any{"x": int64(1)}, "derived": map[string]any{"x": int64(1), "y": "true"}, "weird key": int64(2)}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v\n%s", got, out)
	}
}

func TestDetectConfigFormat(t *testing.T) {
	cases := []struct {
		path string
		data string
		want ConfigFormat
	}{
		{"config.toml", "port: 1", ConfigFormatTOML},
		{"config.yml", "port = 1", ConfigFormatYAML},
		{"config", "# comment\nport = 1\n", ConfigFormatTOML},
		{"config", "[server]\nport = 1\n", ConfigFormatTOML},
		{"config", "url: http://a=b\n", ConfigFormatYAML},
		{"config", "", ConfigFormatTOML},
	}
	for _, c := range cases {
		if got := DetectConfigFormat(c.path, []byte(c.data)); got != c.want {
			t.Errorf("DetectConfigFormat(%q, %q) = %s, want %s", c.path, c.data, got, c.want)
		}
	}
}

func TestDefaultConfigFilePrefersTOML(t *testing.T) {
	dir := t.TempDir()
	if got := DefaultConfigFile(dir); got != filepath.Join(dir, "config.toml") {
		t.Fatalf("no files: %s", got)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.toml"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	if got := DefaultConfigFile(dir); got != filepath.Join(dir, "config.toml") {
		t.Fatalf("both files: %s", got)
	}
}

// Saves of a TOML-loaded config write TOML and keep its comments.
func TestSaveConfigPreserveCommentsKeepsTOML(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	raw := "# Top comment.\nport = 8317 # the port\n\n# Debug logging.\ndebug = false\n"
	if err := os.WriteFile(path, []byte(raw), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Debug = true
	if err = SaveConfigPreserveComments(path, cfg); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	for _, want := range []string{"# Top comment.", "port = 8317 # the port", "# Debug logging.", "debug = true"} {
		if !strings.Contains(text, want) {
			t.Fatalf("saved TOML lacks %q:\n%s", want, text)
		}
	}
	reloaded, err := LoadConfig(path)
	if err != nil || !reloaded.Debug || reloaded.Port != 8317 {
		t.Fatalf("reload: %+v %v", reloaded, err)
	}
}

func TestLoadConfigTOMLSyntaxError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte("port = \n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadConfig(path); err == nil || !strings.Contains(err.Error(), "line 1") {
		t.Fatalf("want a positioned TOML error, got %v", err)
	}
	if cfg, err := LoadConfigOptional(path, true); err != nil || cfg == nil {
		t.Fatalf("optional load of invalid TOML: %v", err)
	}
}

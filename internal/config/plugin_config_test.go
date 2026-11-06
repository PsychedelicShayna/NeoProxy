package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParseConfigBytes_PluginsDefaults(t *testing.T) {
	cfg, errParse := ParseConfigBytes([]byte(`
[plugins]
`))
	if errParse != nil {
		t.Fatalf("ParseConfigBytes() error = %v", errParse)
	}

	if cfg.Plugins.Enabled {
		t.Fatal("Plugins.Enabled = true, want false")
	}
	if cfg.Plugins.Dir != "plugins" {
		t.Fatalf("Plugins.Dir = %q, want plugins", cfg.Plugins.Dir)
	}
	if cfg.Plugins.Configs == nil {
		t.Fatal("Plugins.Configs = nil, want empty map")
	}
	if len(cfg.Plugins.Configs) != 0 {
		t.Fatalf("len(Plugins.Configs) = %d, want 0", len(cfg.Plugins.Configs))
	}
}

func TestParseConfigBytes_PluginsDirExpandsLeadingTilde(t *testing.T) {
	homeDir := t.TempDir()
	t.Setenv("HOME", homeDir)
	t.Setenv("USERPROFILE", homeDir)

	cfg, errParse := ParseConfigBytes([]byte(`
[plugins]
dir = "~/.cli-proxy-api/plugins"
`))
	if errParse != nil {
		t.Fatalf("ParseConfigBytes() error = %v", errParse)
	}

	want := filepath.Join(homeDir, ".cli-proxy-api", "plugins")
	if cfg.Plugins.Dir != want {
		t.Fatalf("Plugins.Dir = %q, want %q", cfg.Plugins.Dir, want)
	}
}

func TestLoadConfig_PluginsDirExpandsLeadingTilde(t *testing.T) {
	homeDir := t.TempDir()
	t.Setenv("HOME", homeDir)
	t.Setenv("USERPROFILE", homeDir)
	configPath := filepath.Join(t.TempDir(), "config.toml")
	if errWrite := os.WriteFile(configPath, []byte("[plugins]\ndir = \"~/.cli-proxy-api/plugins\"\n"), 0o600); errWrite != nil {
		t.Fatalf("os.WriteFile() error = %v", errWrite)
	}

	cfg, errLoad := LoadConfig(configPath)
	if errLoad != nil {
		t.Fatalf("LoadConfig() error = %v", errLoad)
	}

	want := filepath.Join(homeDir, ".cli-proxy-api", "plugins")
	if cfg.Plugins.Dir != want {
		t.Fatalf("Plugins.Dir = %q, want %q", cfg.Plugins.Dir, want)
	}
}

func TestParseConfigBytes_PluginStoreSources(t *testing.T) {
	cfg, errParse := ParseConfigBytes([]byte(`
[plugins]
store-sources = [" https://community.example/registry.json ", ""]
`))
	if errParse != nil {
		t.Fatalf("ParseConfigBytes() error = %v", errParse)
	}

	if len(cfg.Plugins.StoreSources) != 1 {
		t.Fatalf("Plugins.StoreSources len = %d, want 1", len(cfg.Plugins.StoreSources))
	}
	source := cfg.Plugins.StoreSources[0]
	if source != "https://community.example/registry.json" {
		t.Fatalf("Plugins.StoreSources[0] = %#v", source)
	}
}

func TestParseConfigBytes_PluginStoreAuth(t *testing.T) {
	cfg, errParse := ParseConfigBytes([]byte(`
[[plugins.store-auth]]
match = " https://plugins.example.com/ "
apply-to = ["registry", "artifact", "registry"]
type = "bearer"
token-env = " CLIPROXY_PLUGIN_STORE_TOKEN "
[[plugins.store-auth]]
match = ""
type = "bearer"
`))
	if errParse != nil {
		t.Fatalf("ParseConfigBytes() error = %v", errParse)
	}

	if len(cfg.Plugins.StoreAuth) != 1 {
		t.Fatalf("Plugins.StoreAuth len = %d, want 1", len(cfg.Plugins.StoreAuth))
	}
	auth := cfg.Plugins.StoreAuth[0]
	if auth.Match != "https://plugins.example.com/" || auth.Type != "bearer" || auth.TokenEnv != "CLIPROXY_PLUGIN_STORE_TOKEN" {
		t.Fatalf("Plugins.StoreAuth[0] = %#v", auth)
	}
	if len(auth.ApplyTo) != 2 || auth.ApplyTo[0] != "registry" || auth.ApplyTo[1] != "artifact" {
		t.Fatalf("Plugins.StoreAuth[0].ApplyTo = %#v", auth.ApplyTo)
	}
}

func TestParseConfigBytes_PluginAuthRevision(t *testing.T) {
	cfg, errParse := ParseConfigBytes([]byte("[plugins]\nauth-revision = 42\n"))
	if errParse != nil {
		t.Fatalf("ParseConfigBytes() error = %v", errParse)
	}
	if cfg.Plugins.AuthRevision != 42 {
		t.Fatalf("Plugins.AuthRevision = %d, want 42", cfg.Plugins.AuthRevision)
	}
}

func TestParseConfigBytes_PluginInstanceEmptyRawTOML(t *testing.T) {
	cfg, errParse := ParseConfigBytes([]byte(`
[plugins.configs.sample]
`))
	if errParse != nil {
		t.Fatalf("ParseConfigBytes() error = %v", errParse)
	}

	plugin, ok := cfg.Plugins.Configs["sample"]
	if !ok {
		t.Fatal("Plugins.Configs[\"sample\"] missing")
	}
	if plugin.Enabled == nil {
		t.Fatal("Plugin.Enabled = nil, want false pointer")
	}
	if *plugin.Enabled {
		t.Fatal("Plugin.Enabled = true, want false")
	}
	if plugin.Priority != 0 {
		t.Fatalf("Plugin.Priority = %d, want 0", plugin.Priority)
	}

	for name, value := range map[string]any{"raw": plugin.Raw, "plugin": plugin} {
		data, errMarshal := MarshalTOML(value)
		if errMarshal != nil {
			t.Fatalf("MarshalTOML(%s) error = %v", name, errMarshal)
		}
		doc, errParse := ParseConfigDocument(data)
		if errParse != nil {
			t.Fatal(errParse)
		}
		for _, key := range []string{"enabled", "priority"} {
			if _, present := doc[key]; present {
				t.Fatalf("%s TOML contains %s default:\n%s", name, key, data)
			}
		}
	}
}

func TestSaveConfigPreserveComments_PrunesDefaultPluginsDir(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.toml")
	if errWrite := os.WriteFile(configPath, []byte("debug = true\n"), 0o600); errWrite != nil {
		t.Fatalf("os.WriteFile() error = %v", errWrite)
	}

	cfg := &Config{
		Debug: true,
		Plugins: PluginsConfig{
			Dir:     "plugins",
			Configs: map[string]PluginInstanceConfig{},
		},
	}
	if errSave := SaveConfigPreserveComments(configPath, cfg); errSave != nil {
		t.Fatalf("SaveConfigPreserveComments() error = %v", errSave)
	}

	data, errRead := os.ReadFile(configPath)
	if errRead != nil {
		t.Fatalf("os.ReadFile() error = %v", errRead)
	}
	doc, errParse := ParseConfigDocument(data)
	if errParse != nil {
		t.Fatal(errParse)
	}
	if _, present := doc["plugins"]; present {
		t.Fatalf("saved config contains plugins default section:\n%s", data)
	}
}

func TestParseConfigBytes_PluginInstanceRawTOML(t *testing.T) {
	cfg, errParse := ParseConfigBytes([]byte(`
[plugins]
enabled = true
dir = "custom-plugins"
[plugins.configs.sample]
enabled = false
priority = 7
config1 = "value1"
[plugins.configs.sample.config2]
nested = "value2"
`))
	if errParse != nil {
		t.Fatalf("ParseConfigBytes() error = %v", errParse)
	}

	plugin, ok := cfg.Plugins.Configs["sample"]
	if !ok {
		t.Fatal("Plugins.Configs[\"sample\"] missing")
	}
	if plugin.Enabled == nil {
		t.Fatal("Plugin.Enabled = nil, want false pointer")
	}
	if *plugin.Enabled {
		t.Fatal("Plugin.Enabled = true, want false")
	}
	if plugin.Priority != 7 {
		t.Fatalf("Plugin.Priority = %d, want 7", plugin.Priority)
	}

	raw, errMarshal := MarshalTOML(plugin.Raw)
	if errMarshal != nil {
		t.Fatalf("MarshalTOML(Raw) error = %v", errMarshal)
	}
	doc, errParse := ParseConfigDocument(raw)
	if errParse != nil {
		t.Fatal(errParse)
	}
	if doc["enabled"] != false || doc["priority"] != int64(7) || doc["config1"] != "value1" {
		t.Fatalf("Raw TOML lost plugin values: %#v", doc)
	}
	nested, ok := doc["config2"].(map[string]any)
	if !ok || nested["nested"] != "value2" {
		t.Fatalf("Raw TOML lost nested config: %#v", doc)
	}
}

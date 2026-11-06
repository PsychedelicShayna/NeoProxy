package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func parseTestPluginConfig(t *testing.T, text string) PluginInstanceConfig {
	t.Helper()
	raw, errParse := ParseConfigDocument([]byte(text))
	if errParse != nil {
		t.Fatal(errParse)
	}
	item, errConvert := PluginInstanceConfigFromMap(raw)
	if errConvert != nil {
		t.Fatal(errConvert)
	}
	return item
}

func saveTestPluginConfig(t *testing.T, initial string, cfg *Config) (string, map[string]any) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.toml")
	if errWrite := os.WriteFile(path, []byte(initial), 0o600); errWrite != nil {
		t.Fatal(errWrite)
	}
	if errSave := SaveConfigPreserveComments(path, cfg); errSave != nil {
		t.Fatal(errSave)
	}
	data, errRead := os.ReadFile(path)
	if errRead != nil {
		t.Fatal(errRead)
	}
	root, errParse := ParseConfigDocument(data)
	if errParse != nil {
		t.Fatal(errParse)
	}
	return string(data), root
}

func assertTestPluginValues(t *testing.T, root map[string]any, name string, want map[string]any) {
	t.Helper()
	value, present := mapPath(root, "plugins.configs."+name)
	if !present || !reflect.DeepEqual(value, want) {
		t.Fatalf("saved plugin %s = %#v, want %#v", name, value, want)
	}
}

func TestSaveConfigPreserveComments_PluginConfigPreservesZeroValuesAndBooleans(t *testing.T) {
	cfg := &Config{Plugins: PluginsConfig{Enabled: true, Dir: "plugins", Configs: map[string]PluginInstanceConfig{
		"sample": parseTestPluginConfig(t, `name = "updated"
enabled = false
timeout = 0
empty_str = ""
empty_list = []
`),
	}}}
	_, root := saveTestPluginConfig(t, "[plugins]\nenabled = true\n[plugins.configs.sample]\nname = 'initial'\n", cfg)
	assertTestPluginValues(t, root, "sample", map[string]any{"name": "updated", "enabled": false, "timeout": int64(0), "empty_str": "", "empty_list": []any{}})
}

func TestSaveConfigPreserveComments_PluginConfigDeletesRemovedKeys(t *testing.T) {
	cfg := &Config{Plugins: PluginsConfig{Enabled: true, Dir: "plugins", Configs: map[string]PluginInstanceConfig{
		"sample": parseTestPluginConfig(t, "name = 'updated'\n[nested]\nnew_child = 'keep_me'\n"),
	}}}
	_, root := saveTestPluginConfig(t, `[plugins]
enabled = true
[plugins.configs.sample]
name = "initial"
stale_key = "should_be_removed"
[plugins.configs.sample.nested]
old_child = "remove_me"
`, cfg)
	assertTestPluginValues(t, root, "sample", map[string]any{"name": "updated", "nested": map[string]any{"new_child": "keep_me"}})
}

func TestSaveConfigPreserveComments_PluginConfigPreservesSequenceReordering(t *testing.T) {
	cfg := &Config{Plugins: PluginsConfig{Enabled: true, Dir: "plugins", Configs: map[string]PluginInstanceConfig{
		"sample": parseTestPluginConfig(t, `[[vision_models]]
name = "model-b"
detail = "new-detail-b"
extra_b = "only-in-b"
[[vision_models]]
name = "model-a"
detail = "new-detail-a"
`),
	}}}
	_, root := saveTestPluginConfig(t, `[plugins]
enabled = true
[[plugins.configs.sample.vision_models]]
name = "model-a"
detail = "detail-a"
old_prop = "stale-a"
[[plugins.configs.sample.vision_models]]
name = "model-b"
detail = "detail-b"
old_prop = "stale-b"
`, cfg)
	assertTestPluginValues(t, root, "sample", map[string]any{"vision_models": []any{
		map[string]any{"name": "model-b", "detail": "new-detail-b", "extra_b": "only-in-b"},
		map[string]any{"name": "model-a", "detail": "new-detail-a"},
	}})
}

func TestSaveConfigPreserveComments_PluginConfigCreatesNewSubtree(t *testing.T) {
	cfg := &Config{Port: 8080, Plugins: PluginsConfig{Enabled: true, Dir: "plugins", Configs: map[string]PluginInstanceConfig{
		"new-plugin": parseTestPluginConfig(t, "enabled = false\nmode = 'minimal'\ncount = 0\n"),
	}}}
	_, root := saveTestPluginConfig(t, "port = 8080\ndebug = false\n", cfg)
	assertTestPluginValues(t, root, "new-plugin", map[string]any{"enabled": false, "mode": "minimal", "count": int64(0)})
}

func TestSaveConfigPreserveComments_PluginConfigCreatesNewSubtreeWithOnlyZeroValues(t *testing.T) {
	cfg := &Config{Port: 8080, Plugins: PluginsConfig{Dir: "plugins", Configs: map[string]PluginInstanceConfig{
		"zero-plugin": parseTestPluginConfig(t, "enabled = false\ntimeout = 0\nempty_str = ''\nempty_list = []\n"),
	}}}
	_, root := saveTestPluginConfig(t, "port = 8080\n", cfg)
	assertTestPluginValues(t, root, "zero-plugin", map[string]any{"enabled": false, "timeout": int64(0), "empty_str": "", "empty_list": []any{}})
}

func TestSaveConfigPreserveComments_PluginConfigDeletesPluginAndClearsConfigs(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	initial := "[plugins]\nenabled = true\n[plugins.configs.plugin-a]\nname = 'a'\n[plugins.configs.plugin-b]\nname = 'b'\n"
	if errWrite := os.WriteFile(path, []byte(initial), 0o600); errWrite != nil {
		t.Fatal(errWrite)
	}
	cfg := &Config{Plugins: PluginsConfig{Enabled: true, Dir: "plugins", Configs: map[string]PluginInstanceConfig{
		"plugin-b": parseTestPluginConfig(t, "name = 'b-updated'\n"),
	}}}
	for step := 1; step <= 2; step++ {
		if step == 2 {
			cfg.Plugins.Configs = map[string]PluginInstanceConfig{}
		}
		if errSave := SaveConfigPreserveComments(path, cfg); errSave != nil {
			t.Fatalf("step %d: %v", step, errSave)
		}
		data, errRead := os.ReadFile(path)
		if errRead != nil {
			t.Fatal(errRead)
		}
		root, errParse := ParseConfigDocument(data)
		if errParse != nil {
			t.Fatal(errParse)
		}
		if step == 1 {
			if _, present := mapPath(root, "plugins.configs.plugin-a"); present {
				t.Fatal("removed plugin-a survived")
			}
			assertTestPluginValues(t, root, "plugin-b", map[string]any{"name": "b-updated"})
		} else if _, present := mapPath(root, "plugins.configs"); present {
			t.Fatalf("cleared configs survived: %s", data)
		}
	}
}

func TestSaveConfigPreserveComments_PluginConfigPreservesCommentsOutsidePlugins(t *testing.T) {
	cfg := &Config{Port: 8080, Plugins: PluginsConfig{Enabled: true, Dir: "plugins", Configs: map[string]PluginInstanceConfig{
		"sample": parseTestPluginConfig(t, "name = 'updated'\nenabled = false\n"),
	}}}
	text, root := saveTestPluginConfig(t, `# Server listening port
port = 8080
# Plugins configuration section
[plugins]
enabled = true
[plugins.configs.sample]
name = "old"
`, cfg)
	for _, comment := range []string{"# Server listening port", "# Plugins configuration section"} {
		if !strings.Contains(text, comment) {
			t.Errorf("saved config lost comment %q: %s", comment, text)
		}
	}
	assertTestPluginValues(t, root, "sample", map[string]any{"name": "updated", "enabled": false})
}

func TestSaveConfigPreserveComments_PluginConfigAddsConfigsWhenPluginsExistsWithoutConfigs(t *testing.T) {
	cfg := &Config{Plugins: PluginsConfig{Enabled: true, Dir: "custom-dir", Configs: map[string]PluginInstanceConfig{
		"new-item": parseTestPluginConfig(t, "enabled = false\n"),
	}}}
	_, root := saveTestPluginConfig(t, "[plugins]\nenabled = true\ndir = 'custom-dir'\n", cfg)
	if dir, _ := mapPath(root, "plugins.dir"); dir != "custom-dir" {
		t.Fatalf("saved plugins.dir = %#v, want custom-dir", dir)
	}
	assertTestPluginValues(t, root, "new-item", map[string]any{"enabled": false})
}

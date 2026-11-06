package pluginhost

import (
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

func TestRuntimeConfigTOMLAddsHostDefaultsToRawPluginConfig(t *testing.T) {
	item := config.PluginInstanceConfig{
		Priority: 3,
		Raw:      map[string]any{"config1": true, "config2": "value"},
	}

	raw, errEncode := runtimeConfigTOML(item, true)
	if errEncode != nil {
		t.Fatal(errEncode)
	}
	got := string(raw)
	for _, want := range []string{
		"config1 = true",
		"config2 = 'value'",
		"enabled = true",
		"priority = 3",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("runtimeConfigTOML() missing %q in:\n%s", want, got)
		}
	}
}

func TestRuntimeConfigTOMLDefaultsEnabledFalse(t *testing.T) {
	item := config.PluginInstanceConfig{
		Priority: 3,
	}

	raw, errEncode := runtimeConfigTOML(item, false)
	if errEncode != nil {
		t.Fatal(errEncode)
	}
	got := string(raw)
	for _, want := range []string{
		"enabled = false",
		"priority = 3",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("runtimeConfigTOML() missing %q in:\n%s", want, got)
		}
	}
}

func TestRuntimeConfigFromConfigExtractsStoreVersion(t *testing.T) {
	enabled := true
	cfg := &config.Config{
		Plugins: config.PluginsConfig{
			Enabled: true,
			Configs: map[string]config.PluginInstanceConfig{
				"alpha": {
					Enabled: &enabled,
					Raw:     map[string]any{"store": map[string]any{"version": "1.0.3", "release-tag": "v1.0.3"}},
				},
			},
		},
	}

	got, errRuntimeConfig := runtimeConfigFromConfig(cfg)
	if errRuntimeConfig != nil {
		t.Fatalf("runtimeConfigFromConfig() error = %v", errRuntimeConfig)
	}
	if got.Items["alpha"].Version != "1.0.3" {
		t.Fatalf("runtimeConfigFromConfig() version = %q, want 1.0.3", got.Items["alpha"].Version)
	}
}

func TestRuntimeConfigFromConfigDerivesStoreVersionFromReleaseTag(t *testing.T) {
	enabled := true
	cfg := &config.Config{
		Plugins: config.PluginsConfig{
			Enabled: true,
			Configs: map[string]config.PluginInstanceConfig{
				"alpha": {
					Enabled: &enabled,
					Raw:     map[string]any{"store": map[string]any{"release-tag": "v1.0.3"}},
				},
			},
		},
	}

	got, errRuntimeConfig := runtimeConfigFromConfig(cfg)
	if errRuntimeConfig != nil {
		t.Fatalf("runtimeConfigFromConfig() error = %v", errRuntimeConfig)
	}
	if got.Items["alpha"].Version != "1.0.3" {
		t.Fatalf("runtimeConfigFromConfig() version = %q, want 1.0.3", got.Items["alpha"].Version)
	}
}

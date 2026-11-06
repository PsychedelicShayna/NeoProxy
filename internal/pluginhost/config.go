package pluginhost

import (
	"fmt"
	"sort"
	"strings"

	"github.com/pelletier/go-toml/v2"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

var defaultRuntimeConfigTOML = []byte("enabled = false\npriority = 0\n")

type runtimeConfig struct {
	Enabled bool
	Dir     string
	Items   map[string]runtimeItemConfig
}

type runtimeItemConfig struct {
	ID         string
	Enabled    bool
	Priority   int
	Version    string
	ConfigTOML []byte
}

func runtimeConfigFromConfig(cfg *config.Config) (runtimeConfig, error) {
	out := runtimeConfig{
		Dir:   "plugins",
		Items: make(map[string]runtimeItemConfig),
	}
	if cfg == nil {
		return out, nil
	}

	out.Enabled = cfg.Plugins.Enabled
	if !out.Enabled {
		return out, nil
	}
	pluginsDir, errResolvePluginsDir := config.ResolvePluginsDir(cfg.Plugins.Dir)
	if errResolvePluginsDir != nil {
		return runtimeConfig{}, errResolvePluginsDir
	}
	out.Dir = pluginsDir

	ids := make([]string, 0, len(cfg.Plugins.Configs))
	for id := range cfg.Plugins.Configs {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	for _, id := range ids {
		item := cfg.Plugins.Configs[id]
		enabled := false
		if item.Enabled != nil {
			enabled = *item.Enabled
		}
		raw, errEncode := runtimeConfigTOML(item, enabled)
		if errEncode != nil {
			return runtimeConfig{}, fmt.Errorf("pluginhost: encode config for %s: %w", id, errEncode)
		}

		out.Items[id] = runtimeItemConfig{
			ID:         id,
			Enabled:    enabled,
			Priority:   item.Priority,
			Version:    pluginConfigDesiredVersion(item),
			ConfigTOML: raw,
		}
	}
	return out, nil
}

func defaultRuntimeItemConfig(id string) runtimeItemConfig {
	return runtimeItemConfig{
		ID:         id,
		Enabled:    false,
		Priority:   0,
		ConfigTOML: append([]byte(nil), defaultRuntimeConfigTOML...),
	}
}

func runtimeConfigTOML(item config.PluginInstanceConfig, enabled bool) ([]byte, error) {
	values := make(map[string]any, len(item.Raw)+2)
	for key, value := range item.Raw {
		values[key] = value
	}
	if _, ok := values["enabled"]; !ok {
		values["enabled"] = enabled
	}
	if _, ok := values["priority"]; !ok {
		values["priority"] = item.Priority
	}
	return toml.Marshal(values)
}

func desiredPluginVersions(items map[string]runtimeItemConfig) map[string]string {
	if len(items) == 0 {
		return nil
	}
	out := make(map[string]string, len(items))
	for id, item := range items {
		id = strings.TrimSpace(id)
		version := strings.TrimSpace(item.Version)
		if id == "" || version == "" {
			continue
		}
		out[id] = version
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func pluginConfigDesiredVersion(item config.PluginInstanceConfig) string {
	store, ok := item.Raw["store"].(map[string]any)
	if !ok {
		return ""
	}
	version, _ := store["version"].(string)
	if version = normalizePluginDesiredVersion(version); version != "" {
		return version
	}
	releaseTag, _ := store["release-tag"].(string)
	return normalizePluginDesiredVersion(releaseTag)
}

func normalizePluginDesiredVersion(version string) string {
	version = strings.TrimSpace(version)
	if len(version) > 1 && (version[0] == 'v' || version[0] == 'V') {
		version = version[1:]
	}
	if !validPluginVersion(version) {
		return ""
	}
	return version
}

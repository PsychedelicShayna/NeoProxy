package management

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"sort"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/htmlsanitize"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/pluginhost"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

type pluginListResponse struct {
	PluginsEnabled bool              `json:"plugins_enabled"`
	PluginsDir     string            `json:"plugins_dir"`
	Plugins        []pluginListEntry `json:"plugins"`
}

type pluginListEntry struct {
	ID               string                  `json:"id"`
	Path             string                  `json:"path"`
	Configured       bool                    `json:"configured"`
	Registered       bool                    `json:"registered"`
	Enabled          bool                    `json:"enabled"`
	EffectiveEnabled bool                    `json:"effective_enabled"`
	SupportsOAuth    bool                    `json:"supports_oauth"`
	OAuthProvider    string                  `json:"oauth_provider"`
	SupportsQuota    bool                    `json:"supports_quota"`
	QuotaProvider    string                  `json:"quota_provider,omitempty"`
	Logo             string                  `json:"logo"`
	ConfigFields     []pluginConfigFieldInfo `json:"config_fields"`
	Menus            []pluginMenuInfo        `json:"menus"`
	Metadata         *pluginMetadataInfo     `json:"metadata"`
}

type pluginMetadataInfo struct {
	Name             string                  `json:"name"`
	Version          string                  `json:"version"`
	Author           string                  `json:"author"`
	GitHubRepository string                  `json:"github_repository"`
	Logo             string                  `json:"logo"`
	ConfigFields     []pluginConfigFieldInfo `json:"config_fields"`
}

type pluginConfigFieldInfo struct {
	Default     any      `json:"default"`
	Name        string   `json:"name"`
	Type        string   `json:"type"`
	EnumValues  []string `json:"enum_values"`
	Description string   `json:"description"`
}

type pluginMenuInfo struct {
	Path        string `json:"path"`
	Menu        string `json:"menu"`
	Description string `json:"description"`
}

// ListPlugins returns discovered, configured, and registered plugin entries.
func (h *Handler) ListPlugins(c *gin.Context) {
	if h == nil || h.cfg == nil {
		c.JSON(http.StatusOK, pluginListResponse{
			PluginsDir: "plugins",
			Plugins:    []pluginListEntry{},
		})
		return
	}

	h.mu.Lock()
	pluginsEnabled := h.cfg.Plugins.Enabled
	pluginsDir := normalizedPluginsDir(h.cfg.Plugins.Dir)
	configs := make(map[string]config.PluginInstanceConfig, len(h.cfg.Plugins.Configs))
	for id, item := range h.cfg.Plugins.Configs {
		configs[id] = item
	}
	host := h.pluginHost
	h.mu.Unlock()

	resolvedPluginsDir, errResolvePluginsDir := config.ResolvePluginsDir(pluginsDir)
	if errResolvePluginsDir != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "plugin_directory_invalid", "message": errResolvePluginsDir.Error()})
		return
	}
	pluginsDir = resolvedPluginsDir
	entries := make(map[string]pluginListEntry)
	files, errDiscover := pluginhost.DiscoverPluginFiles(pluginsDir, pluginStoreDesiredVersions(configs))
	if errDiscover != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "plugin_discovery_failed", "message": errDiscover.Error()})
		return
	}
	for _, file := range files {
		entries[file.ID] = pluginListEntry{
			ID:           htmlsanitize.String(file.ID),
			Path:         htmlsanitize.String(file.Path),
			Enabled:      false,
			ConfigFields: []pluginConfigFieldInfo{},
			Menus:        []pluginMenuInfo{},
		}
	}
	for id, item := range configs {
		entry := entries[id]
		entry.ID = htmlsanitize.String(id)
		entry.Configured = true
		entry.Enabled = pluginInstanceEnabled(item)
		if entry.ConfigFields == nil {
			entry.ConfigFields = []pluginConfigFieldInfo{}
		}
		if entry.Menus == nil {
			entry.Menus = []pluginMenuInfo{}
		}
		entries[id] = entry
	}
	if host != nil {
		for _, info := range host.RegisteredPlugins() {
			entry := entries[info.ID]
			entry.ID = htmlsanitize.String(info.ID)
			entry.Registered = true
			entry.SupportsOAuth = info.SupportsOAuth
			entry.OAuthProvider = htmlsanitize.String(info.OAuthProvider)
			entry.SupportsQuota = info.SupportsQuota
			entry.QuotaProvider = htmlsanitize.String(info.QuotaProvider)
			entry.Logo = htmlsanitize.String(info.Metadata.Logo)
			entry.ConfigFields = pluginConfigFields(info.Metadata.ConfigFields)
			entry.Menus = pluginMenus(info.Menus)
			entry.Metadata = pluginMetadata(info.Metadata)
			entries[info.ID] = entry
		}
	}

	ids := make([]string, 0, len(entries))
	for id := range entries {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	out := make([]pluginListEntry, 0, len(ids))
	for _, id := range ids {
		entry := entries[id]
		entry.EffectiveEnabled = pluginsEnabled && entry.Enabled && entry.Registered
		if entry.ConfigFields == nil {
			entry.ConfigFields = []pluginConfigFieldInfo{}
		}
		if entry.Menus == nil {
			entry.Menus = []pluginMenuInfo{}
		}
		out = append(out, entry)
	}

	c.JSON(http.StatusOK, pluginListResponse{
		PluginsEnabled: pluginsEnabled,
		PluginsDir:     htmlsanitize.String(pluginsDir),
		Plugins:        out,
	})
}

// GetPluginConfig returns the preserved plugins.configs.<id> object as JSON.
func (h *Handler) GetPluginConfig(c *gin.Context) {
	id, okID := pluginIDFromRequest(c)
	if !okID {
		return
	}
	if h == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "plugin_not_found", "message": "plugin not found"})
		return
	}

	h.mu.Lock()
	if h.cfg == nil {
		h.mu.Unlock()
		c.JSON(http.StatusNotFound, gin.H{"error": "plugin_not_found", "message": "plugin not found"})
		return
	}
	item, configured := h.cfg.Plugins.Configs[id]
	pluginsDir := normalizedPluginsDir(h.cfg.Plugins.Dir)
	host := h.pluginHost
	h.mu.Unlock()

	if configured {
		body, errBody := pluginConfigJSONObject(item)
		if errBody != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "plugin_config_encode_failed", "message": errBody.Error()})
			return
		}
		c.JSON(http.StatusOK, body)
		return
	}

	if pluginRegistered(host, id) {
		c.JSON(http.StatusOK, gin.H{})
		return
	}
	resolvedPluginsDir, errResolvePluginsDir := config.ResolvePluginsDir(pluginsDir)
	if errResolvePluginsDir != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "plugin_directory_invalid", "message": errResolvePluginsDir.Error()})
		return
	}
	discovered, errDiscover := pluginDiscovered(resolvedPluginsDir, id)
	if errDiscover != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "plugin_discovery_failed", "message": errDiscover.Error()})
		return
	}
	if discovered {
		c.JSON(http.StatusOK, gin.H{})
		return
	}

	c.JSON(http.StatusNotFound, gin.H{"error": "plugin_not_found", "message": "plugin not found"})
}

// PatchPluginEnabled updates plugins.configs.<id>.enabled without touching plugins.enabled.
func (h *Handler) PatchPluginEnabled(c *gin.Context) {
	id, okID := pluginIDFromRequest(c)
	if !okID {
		return
	}
	var body struct {
		Enabled *bool `json:"enabled"`
	}
	if errBindJSON := c.ShouldBindJSON(&body); errBindJSON != nil || body.Enabled == nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_body", "message": "enabled is required"})
		return
	}

	h.mu.Lock()
	ensurePluginConfigMap(h.cfg)
	item := h.cfg.Plugins.Configs[id]
	raw := pluginConfigMap(item)
	raw["enabled"] = *body.Enabled
	updated, errConfig := config.PluginInstanceConfigFromMap(raw)
	if errConfig != nil {
		h.mu.Unlock()
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_config", "message": errConfig.Error()})
		return
	}
	h.cfg.Plugins.Configs[id] = updated
	cfgSnapshot, okSnapshot := h.saveConfigAndSnapshotLocked(c)
	h.mu.Unlock()
	if !okSnapshot {
		return
	}

	h.reloadConfigAfterManagementSaveAsync(c.Request.Context(), cfgSnapshot)
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

// PutPluginConfig replaces plugins.configs.<id> with the request object.
func (h *Handler) PutPluginConfig(c *gin.Context) {
	id, okID := pluginIDFromRequest(c)
	if !okID {
		return
	}
	body, okBody := readPluginConfigObject(c)
	if !okBody {
		return
	}
	rawValue, errNode := normalizePluginJSONValue(body)
	if errNode != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_body", "message": errNode.Error()})
		return
	}
	updated, errConfig := config.PluginInstanceConfigFromMap(rawValue.(map[string]any))
	if errConfig != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_config", "message": errConfig.Error()})
		return
	}

	h.mu.Lock()
	defer h.mu.Unlock()
	ensurePluginConfigMap(h.cfg)
	h.cfg.Plugins.Configs[id] = updated
	h.persistLocked(c)
}

// PatchPluginConfig shallow-merges plugins.configs.<id> with the request object.
func (h *Handler) PatchPluginConfig(c *gin.Context) {
	id, okID := pluginIDFromRequest(c)
	if !okID {
		return
	}
	body, okBody := readPluginConfigObject(c)
	if !okBody {
		return
	}

	h.mu.Lock()
	defer h.mu.Unlock()
	ensurePluginConfigMap(h.cfg)
	raw := pluginConfigMap(h.cfg.Plugins.Configs[id])
	for key, value := range body {
		if value == nil {
			delete(raw, key)
			continue
		}
		normalized, errValue := normalizePluginJSONValue(value)
		if errValue != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_body", "message": errValue.Error()})
			return
		}
		raw[key] = normalized
	}
	updated, errConfig := config.PluginInstanceConfigFromMap(raw)
	if errConfig != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_config", "message": errConfig.Error()})
		return
	}
	h.cfg.Plugins.Configs[id] = updated
	h.persistLocked(c)
}

// DeletePlugin removes the selected local plugin file and its saved config.
func (h *Handler) DeletePlugin(c *gin.Context) {
	id, okID := pluginIDFromRequest(c)
	if !okID {
		return
	}
	if h == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "plugin_not_found", "message": "plugin not found"})
		return
	}

	h.mu.Lock()
	if h.cfg == nil {
		h.mu.Unlock()
		c.JSON(http.StatusNotFound, gin.H{"error": "plugin_not_found", "message": "plugin not found"})
		return
	}
	pluginsDir := normalizedPluginsDir(h.cfg.Plugins.Dir)
	item, configured := h.cfg.Plugins.Configs[id]
	host := h.pluginHost
	h.mu.Unlock()

	resolvedPluginsDir, errResolvePluginsDir := config.ResolvePluginsDir(pluginsDir)
	if errResolvePluginsDir != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "plugin_directory_invalid", "message": errResolvePluginsDir.Error()})
		return
	}
	pluginsDir = resolvedPluginsDir
	var desiredVersions map[string]string
	if configured {
		desiredVersions = pluginStoreDesiredVersions(map[string]config.PluginInstanceConfig{id: item})
	}
	path, errPath := pluginFilePath(pluginsDir, id, desiredVersions)
	if errPath != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "plugin_discovery_failed", "message": errPath.Error()})
		return
	}
	if path == "" && !configured {
		c.JSON(http.StatusNotFound, gin.H{"error": "plugin_not_found", "message": "plugin not found"})
		return
	}

	if pluginBusy(host, id) && (host == nil || !host.UnloadPlugin(id)) && pluginBusy(host, id) {
		c.JSON(http.StatusConflict, gin.H{
			"error":            "plugin_delete_requires_restart",
			"message":          "loaded plugin cannot be deleted while the server is running",
			"restart_required": true,
		})
		return
	}

	fileDeleted := false
	if path != "" {
		if errRemove := os.Remove(path); errRemove != nil {
			if !errors.Is(errRemove, os.ErrNotExist) {
				c.JSON(http.StatusInternalServerError, gin.H{"error": "plugin_delete_failed", "message": errRemove.Error()})
				return
			}
		} else {
			fileDeleted = true
		}
	}

	h.mu.Lock()
	delete(h.cfg.Plugins.Configs, id)
	if configured {
		if errSave := config.SaveConfigPreserveComments(h.configFilePath, h.cfg, c.GetBool(ConfigV8ContextKey)); errSave != nil {
			h.mu.Unlock()
			c.JSON(http.StatusInternalServerError, gin.H{
				"error":        "config_save_failed",
				"message":      fmt.Sprintf("plugin deleted but saving config failed: %s", errSave.Error()),
				"file_deleted": fileDeleted,
				"path":         path,
			})
			return
		}
	}
	cfgSnapshot := h.reloadSnapshotConfigLocked()
	h.mu.Unlock()

	h.reloadConfigAfterManagementSaveAsync(c.Request.Context(), cfgSnapshot)
	c.JSON(http.StatusOK, gin.H{
		"status":             "deleted",
		"id":                 htmlsanitize.String(id),
		"path":               htmlsanitize.String(path),
		"file_deleted":       fileDeleted,
		"configured_removed": configured,
		"restart_required":   false,
	})
}

func normalizedPluginsDir(dir string) string {
	dir = strings.TrimSpace(dir)
	if dir == "" {
		return "plugins"
	}
	return dir
}

func pluginInstanceEnabled(item config.PluginInstanceConfig) bool {
	if item.Enabled == nil {
		return false
	}
	return *item.Enabled
}

func pluginRegistered(host *pluginhost.Host, id string) bool {
	if host == nil {
		return false
	}
	for _, info := range host.RegisteredPlugins() {
		if info.ID == id {
			return true
		}
	}
	return false
}

func pluginDiscovered(pluginsDir string, id string) (bool, error) {
	files, errDiscover := pluginhost.DiscoverPluginFiles(pluginsDir)
	if errDiscover != nil {
		return false, errDiscover
	}
	for _, file := range files {
		if file.ID == id {
			return true, nil
		}
	}
	return false, nil
}

func pluginFilePath(pluginsDir string, id string, desiredVersions ...map[string]string) (string, error) {
	files, errDiscover := pluginhost.DiscoverPluginFiles(pluginsDir, desiredVersions...)
	if errDiscover != nil {
		return "", errDiscover
	}
	for _, file := range files {
		if file.ID == id {
			return file.Path, nil
		}
	}
	return "", nil
}

func pluginConfigFields(fields []pluginapi.ConfigField) []pluginConfigFieldInfo {
	out := make([]pluginConfigFieldInfo, 0, len(fields))
	for _, field := range fields {
		out = append(out, pluginConfigFieldInfo{
			Name:        htmlsanitize.String(field.Name),
			Type:        htmlsanitize.String(string(field.Type)),
			Default:     htmlsanitize.JSONValue(field.Default),
			EnumValues:  htmlsanitize.Strings(field.EnumValues),
			Description: htmlsanitize.String(field.Description),
		})
	}
	return out
}

func pluginMenus(menus []pluginhost.RegisteredPluginMenu) []pluginMenuInfo {
	out := make([]pluginMenuInfo, 0, len(menus))
	for _, menu := range menus {
		out = append(out, pluginMenuInfo{
			Path:        htmlsanitize.String(menu.Path),
			Menu:        htmlsanitize.String(menu.Menu),
			Description: htmlsanitize.String(menu.Description),
		})
	}
	return out
}

func pluginMetadata(meta pluginapi.Metadata) *pluginMetadataInfo {
	return &pluginMetadataInfo{
		Name:             htmlsanitize.String(meta.Name),
		Version:          htmlsanitize.String(meta.Version),
		Author:           htmlsanitize.String(meta.Author),
		GitHubRepository: htmlsanitize.String(meta.GitHubRepository),
		Logo:             htmlsanitize.String(meta.Logo),
		ConfigFields:     pluginConfigFields(meta.ConfigFields),
	}
}

func pluginIDFromRequest(c *gin.Context) (string, bool) {
	id := strings.TrimSpace(c.Param("id"))
	if !pluginhost.ValidatePluginID(id) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_plugin_id", "message": "invalid plugin id"})
		return "", false
	}
	return id, true
}

func readPluginConfigObject(c *gin.Context) (map[string]any, bool) {
	decoder := json.NewDecoder(c.Request.Body)
	decoder.UseNumber()
	var body map[string]any
	if errDecode := decoder.Decode(&body); errDecode != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_body", "message": errDecode.Error()})
		return nil, false
	}
	if body == nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_body", "message": "body must be a JSON object"})
		return nil, false
	}
	return body, true
}

func ensurePluginConfigMap(cfg *config.Config) {
	if cfg == nil {
		return
	}
	cfg.NormalizePluginsConfig()
}

func pluginConfigMap(item config.PluginInstanceConfig) map[string]any {
	raw := cloneConfigV8Value(item.Raw)
	object, _ := raw.(map[string]any)
	if object == nil {
		object = map[string]any{}
	}
	if item.Enabled != nil {
		object["enabled"] = *item.Enabled
	}
	if item.Priority != 0 {
		object["priority"] = item.Priority
	}
	return object
}

func pluginConfigJSONObject(item config.PluginInstanceConfig) (map[string]any, error) {
	return pluginConfigMap(item), nil
}

// JSON numbers are converted before persistence so the native TOML encoder
// retains integer values instead of treating json.Number as a string.
func normalizePluginJSONValue(value any) (any, error) {
	switch value := value.(type) {
	case json.Number:
		if integer, err := value.Int64(); err == nil {
			return integer, nil
		}
		number, err := value.Float64()
		if err != nil {
			return nil, fmt.Errorf("invalid number %q: %w", value, err)
		}
		return number, nil
	case map[string]any:
		out := make(map[string]any, len(value))
		for key, child := range value {
			normalized, err := normalizePluginJSONValue(child)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", key, err)
			}
			out[key] = normalized
		}
		return out, nil
	case []any:
		out := make([]any, len(value))
		for index, child := range value {
			normalized, err := normalizePluginJSONValue(child)
			if err != nil {
				return nil, err
			}
			out[index] = normalized
		}
		return out, nil
	default:
		return value, nil
	}
}

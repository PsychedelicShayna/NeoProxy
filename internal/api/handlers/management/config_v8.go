package management

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"reflect"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/clienterror"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

// ConfigV8ContextKey marks operations registered by the v8 API that save configuration.
const ConfigV8ContextKey = "management.config-v8"

// ConfigV8 serves the persisted configuration using v8 names. GET is a view;
// only a successful configuration mutation migrates the stored document.
//
// /config.toml exchanges the whole document as TOML. All writes use the
// canonical native TOML configuration, reported in X-Config-Format.
//
// Reads return an ETag holding the SHA-256 of the on-disk config bytes.
// Writes honour an optional If-Match against that token, so a client can
// detect that another writer saved after its read (412) instead of silently
// overwriting the change. Without If-Match, writes stay unconditional.
func (h *Handler) ConfigV8(c *gin.Context) {
	h.mu.Lock()
	defer h.mu.Unlock()
	resolvedPath, err := config.ResolveConfigPath(h.configFilePath)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "read_failed"})
		return
	}
	h.configFilePath = resolvedPath
	source, raw, fileFormat, err := config.ReadConfigFile(h.configFilePath)
	if raw == nil && err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "read_failed"})
		return
	}
	c.Header("X-Config-Format", string(fileFormat))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "invalid_config", "message": err.Error()})
		return
	}
	diskHash := config.ComputeSourceHash(raw)
	if c.Request.Method != http.MethodGet && !ifMatchSatisfied(c.Request.Header.Values("If-Match"), diskHash) {
		c.Header("ETag", configV8ETag(diskHash))
		c.JSON(http.StatusPreconditionFailed, gin.H{"error": "precondition_failed"})
		return
	}
	data, _, err := config.NormalizeConfigLayout(source, true)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "invalid_config", "message": err.Error()})
		return
	}
	root, err := config.ParseConfigDocument(data)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "invalid_config"})
		return
	}
	path := strings.Trim(c.Param("path"), "/")
	parts := []string{}
	if path != "" {
		parts = strings.Split(path, "/")
	}
	documentRequest := strings.HasSuffix(c.FullPath(), "/config.toml")
	if c.Request.Method == http.MethodGet {
		config.ProjectV8ConfigAliases(root, strings.Join(parts, "."))
		if !documentRequest {
			if servers, ok := configV8Node(root, v8ICEServersPath).([]any); ok {
				for _, server := range servers {
					deleteConfigV8Path(server, []string{"username"})
					deleteConfigV8Path(server, []string{"credential"})
				}
			}
			h.injectV8APIKeyAuthIndexesLocked(root, data)
		}
		value := configV8Node(root, parts)
		if value == nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "not_found"})
			return
		}
		c.Header("Cache-Control", "no-store")
		c.Header("ETag", configV8ETag(diskHash))
		if documentRequest {
			document, errConvert := config.RenderConfigDocumentPreserve(root, data)
			if errConvert != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": "convert_failed", "message": errConvert.Error()})
				return
			}
			c.Data(http.StatusOK, config.ConfigFormatTOML.ContentType(), document)
			return
		}
		c.JSON(http.StatusOK, value)
		return
	}
	commentSource := data
	before := cloneConfigV8Value(root).(map[string]any)
	config.ProjectV8ConfigAliases(root, strings.Join(parts, "."))
	if c.Request.Method == http.MethodDelete {
		if len(parts) == 0 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "cannot_delete_config"})
			return
		}
		if !deleteConfigV8Path(root, parts) {
			c.JSON(http.StatusNotFound, gin.H{"error": "not_found"})
			return
		}
	} else {
		body, errRead := io.ReadAll(c.Request.Body)
		if errRead != nil {
			c.JSON(clienterror.HTTPStatusFromErrorOr(errRead, http.StatusBadRequest), gin.H{"error": "invalid_body"})
			return
		}
		if !documentRequest && !json.Valid(body) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_json"})
			return
		}
		var update any
		if documentRequest {
			update, err = config.ParseConfigDocument(body)
			if err == nil {
				commentSource = body
			}
		} else {
			decoder := json.NewDecoder(bytes.NewReader(body))
			decoder.UseNumber()
			err = decoder.Decode(&update)
			if err == nil {
				update, err = normalizePluginJSONValue(update)
			}
		}
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_body", "message": err.Error()})
			return
		}
		stripAPIKeysAuthIndexesFromUpdate(parts, update)
		if len(parts) == 0 {
			object, ok := update.(map[string]any)
			if !ok {
				c.JSON(http.StatusBadRequest, gin.H{"error": "config_must_be_object"})
				return
			}
			if err = config.NormalizeV8ConfigAliases(object); err != nil {
				c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_config", "message": err.Error()})
				return
			}
			if c.Request.Method == http.MethodPatch {
				root = mergeConfigV8Patch(root, object).(map[string]any)
			} else {
				root = object
			}
		} else {
			// Paths identify table keys, never array indexes. Lists are replaced whole.
			dst := root
			for index, part := range parts {
				if part == "" {
					c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_path"})
					return
				}
				if index == len(parts)-1 {
					if c.Request.Method == http.MethodPatch {
						dst[part] = mergeConfigV8Patch(dst[part], update)
					} else {
						dst[part] = update
					}
					break
				}
				next, exists := dst[part]
				if !exists {
					next = map[string]any{}
					dst[part] = next
				}
				object, ok := next.(map[string]any)
				if !ok {
					c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_path"})
					return
				}
				dst = object
			}
		}
	}
	if err = config.NormalizeV8ConfigAliases(root); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_config", "message": err.Error()})
		return
	}
	if !documentRequest && c.Request.Method != http.MethodDelete {
		preserveV8TURNSecrets(root, before)
	}
	// Revisions are owned by Home and must not become ordinary editable settings.
	for _, field := range []string{"credentials/concurrency/lifecycle-config-revision", "credentials/concurrency/observation-barrier-revision", "plugins/auth-revision"} {
		old := configV8Node(before, strings.Split(field, "/"))
		next := configV8Node(root, strings.Split(field, "/"))
		a, b := old, next
		if !reflect.DeepEqual(a, b) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "read_only_field", "field": field})
			return
		}
	}
	stripAPIKeysAuthIndexesFromRoot(root)
	data, err = config.RenderConfigDocumentPreserve(root, commentSource)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_config", "message": err.Error()})
		return
	}
	next, err := config.ParseConfigBytes(data)
	if err != nil {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "invalid_config", "message": err.Error()})
		return
	}
	if err = config.ValidateV8Config(data); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_config", "message": err.Error()})
		return
	}
	data, _, err = config.NormalizeConfigLayout(data, true)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_config", "message": err.Error()})
		return
	}
	// Save the validated canonical tree directly: projecting runtime defaults
	// back onto it loses explicit nulls, empty maps, and opaque plugin settings.
	// The tree is rendered in the config file's format first, so a document TOML
	// cannot express is rejected before anything is written.
	encoded, err := EncodeConfig(h.configFilePath, data)
	if err != nil {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "invalid_config", "message": err.Error()})
		return
	}
	// writeConfigBytes retains the inode of a mounted configuration file.
	if err = writeConfigBytes(h.configFilePath, encoded); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "write_failed", "message": err.Error()})
		return
	}
	// Runtime-only ownership state is never sourced from an uploaded document.
	next.Home = h.cfg.Home
	// WriteConfig may normalize the bytes, so report the version actually on
	// disk. If it cannot be read back, omit the ETag instead of guessing one.
	// Like Home, SourceHash keeps its runtime owner and is published by the
	// reload, so it is not overwritten on next here.
	if saved, errSaved := os.ReadFile(h.configFilePath); errSaved == nil {
		c.Header("ETag", configV8ETag(config.ComputeSourceHash(saved)))
	}
	h.cfg = next
	snapshot := h.reloadSnapshotConfigLocked()
	h.reloadConfigAfterManagementSaveAsync(c.Request.Context(), snapshot)
	c.JSON(http.StatusOK, gin.H{"status": "ok", "config-version": 8})
}

// configV8ETag renders a config source hash as a strong ETag.
func configV8ETag(hash string) string {
	return `"` + hash + `"`
}

// ifMatchSatisfied reports whether the optional If-Match header lines pass
// the RFC 9110 strong comparison against currentHash. Repeated lines form
// one list. An absent header always passes; a present but blank header
// carries no tag and fails; the whole field "*" matches any current
// version; weak and malformed entity tags never match.
func ifMatchSatisfied(values []string, currentHash string) bool {
	if len(values) == 0 {
		return true
	}
	field := strings.TrimSpace(strings.Join(values, ","))
	if field == "*" {
		return true
	}
	for _, tag := range splitEntityTagList(field) {
		tag = strings.TrimSpace(tag)
		if len(tag) >= 2 && tag[0] == '"' && tag[len(tag)-1] == '"' && !strings.Contains(tag[1:len(tag)-1], `"`) && tag[1:len(tag)-1] == currentHash {
			return true
		}
	}
	return false
}

// splitEntityTagList splits an If-Match field value on list commas. A comma
// inside a quoted entity tag is tag content, not a separator.
func splitEntityTagList(field string) []string {
	parts := []string{}
	start := 0
	quoted := false
	for i := 0; i < len(field); i++ {
		switch field[i] {
		case '"':
			quoted = !quoted
		case ',':
			if !quoted {
				parts = append(parts, field[start:i])
				start = i + 1
			}
		}
	}
	return append(parts, field[start:])
}

var v8ICEServersPath = []string{"oauth", "providers", "codex", "live-media-relay", "ice-servers"}

// JSON reads redact TURN secrets. Preserve omitted credentials when a client
// writes that JSON back, matching endpoints rather than array positions.
// Whole-document TOML writes retain full replacement semantics.
func preserveV8TURNSecrets(root, before map[string]any) {
	next, _ := configV8Node(root, v8ICEServersPath).([]any)
	previous, _ := configV8Node(before, v8ICEServersPath).([]any)
	matched := make([]bool, len(previous))
	for _, entry := range next {
		server, ok := entry.(map[string]any)
		if !ok {
			continue
		}
		urls, exists := server["urls"]
		if !exists {
			continue
		}
		for index, oldEntry := range previous {
			old, ok := oldEntry.(map[string]any)
			if !ok || matched[index] || !reflect.DeepEqual(urls, old["urls"]) {
				continue
			}
			matched[index] = true
			for _, name := range []string{"username", "credential"} {
				if _, exists := server[name]; !exists {
					if secret, exists := old[name]; exists {
						server[name] = cloneConfigV8Value(secret)
					}
				}
			}
			break
		}
	}
}

func configV8Node(root any, parts []string) any {
	for _, part := range parts {
		object, ok := root.(map[string]any)
		if !ok {
			return nil
		}
		root = object[part]
	}
	return root
}

// deleteConfigV8Path prunes only empty ancestors of the removed field.
func deleteConfigV8Path(root any, parts []string) bool {
	object, ok := root.(map[string]any)
	if !ok || len(parts) == 0 {
		return false
	}
	child, exists := object[parts[0]]
	if !exists {
		return false
	}
	if len(parts) > 1 {
		if !deleteConfigV8Path(child, parts[1:]) {
			return false
		}
		if nested, ok := child.(map[string]any); !ok || len(nested) != 0 {
			return true
		}
	}
	delete(object, parts[0])
	return true
}

func cloneConfigV8Value(value any) any {
	switch value := value.(type) {
	case map[string]any:
		out := make(map[string]any, len(value))
		for key, child := range value {
			out[key] = cloneConfigV8Value(child)
		}
		return out
	case []any:
		out := make([]any, len(value))
		for index, child := range value {
			out[index] = cloneConfigV8Value(child)
		}
		return out
	default:
		return value
	}
}

func mergeConfigV8Patch(dst, src any) any {
	target, targetOK := dst.(map[string]any)
	update, updateOK := src.(map[string]any)
	if !targetOK || !updateOK {
		return src
	}
	for key, value := range update {
		target[key] = mergeConfigV8Patch(target[key], value)
	}
	return target
}

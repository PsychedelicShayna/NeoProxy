package management

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/watcher/synthesizer"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

type geminiKeyWithAuthIndex struct {
	config.GeminiKey
	AuthIndex string `json:"auth-index,omitempty"`
}

type claudeKeyWithAuthIndex struct {
	config.ClaudeKey
	AuthIndex string `json:"auth-index,omitempty"`
}

type codexKeyWithAuthIndex struct {
	config.CodexKey
	AuthIndex string `json:"auth-index,omitempty"`
}

type xaiKeyWithAuthIndex struct {
	config.XAIKey
	AuthIndex string `json:"auth-index,omitempty"`
}

type metaKeyWithAuthIndex struct {
	config.MetaKey
	AuthIndex string `json:"auth-index,omitempty"`
}

type vertexCompatKeyWithAuthIndex struct {
	config.VertexCompatKey
	AuthIndex string `json:"auth-index,omitempty"`
}

type openAICompatibilityAPIKeyWithAuthIndex struct {
	config.OpenAICompatibilityAPIKey
	AuthIndex string `json:"auth-index,omitempty"`
}

type openAICompatibilityWithAuthIndex struct {
	Name                  string                                   `json:"name"`
	Priority              int                                      `json:"priority,omitempty"`
	Disabled              bool                                     `json:"disabled"`
	Prefix                string                                   `json:"prefix,omitempty"`
	BaseURL               string                                   `json:"base-url"`
	APIKeyEntries         []openAICompatibilityAPIKeyWithAuthIndex `json:"api-key-entries,omitempty"`
	Models                []config.OpenAICompatibilityModel        `json:"models,omitempty"`
	Headers               map[string]string                        `json:"headers,omitempty"`
	SupportPromptCacheKey bool                                     `json:"support-prompt-cache-key,omitempty"`
	DisableCooling        *bool                                    `json:"disable-cooling,omitempty"`
	RequestRetry          *int                                     `json:"request-retry,omitempty"`
	RequestScopedErrors   []config.RequestScopedErrorRule          `json:"request-scoped-errors,omitempty"`
	AuthIndex             string                                   `json:"auth-index,omitempty"`
}

func liveAuthIndexFromManager(manager *coreauth.Manager) map[string]string {
	out := map[string]string{}
	if manager == nil {
		return out
	}
	// authManager.List() returns clones, so EnsureIndex only affects these copies.
	for _, auth := range manager.List() {
		if auth == nil {
			continue
		}
		id := strings.TrimSpace(auth.ID)
		if id == "" {
			continue
		}
		idx := strings.TrimSpace(auth.Index)
		if idx == "" {
			idx = auth.EnsureIndex()
		}
		if idx == "" {
			continue
		}
		out[id] = idx
	}
	return out
}

func (h *Handler) liveAuthIndexByID() map[string]string {
	if h == nil {
		return map[string]string{}
	}
	h.mu.Lock()
	manager := h.authManager
	h.mu.Unlock()
	return liveAuthIndexFromManager(manager)
}

func configMapScalar(node any, key string) string {
	value, _ := configMapScalarPresent(node, key)
	return value
}

func setMapScalar(node any, key, value string) {
	if mapping, ok := node.(map[string]any); ok && mapping != nil {
		mapping[key] = value
	}
}

func deleteMapKey(node any, key string) bool {
	mapping, ok := node.(map[string]any)
	if !ok {
		return false
	}
	_, present := mapping[key]
	delete(mapping, key)
	return present
}

func stripAPIKeysAuthIndexesFromGroup(group any) {
	mapping, ok := group.(map[string]any)
	if !ok {
		return
	}
	deleteMapKey(mapping, "auth_index")
	deleteMapKey(mapping, "auth-index")
	keys, _ := mapping["keys"].([]any)
	for _, key := range keys {
		deleteMapKey(key, "auth_index")
		deleteMapKey(key, "auth-index")
	}
}

func stripAPIKeysAuthIndexesFromGroups(groups any) {
	switch value := groups.(type) {
	case []any:
		for _, group := range value {
			stripAPIKeysAuthIndexesFromGroup(group)
		}
	case map[string]any:
		stripAPIKeysAuthIndexesFromGroup(value)
	}
}

func stripAPIKeysAuthIndexesFromProvidersMap(node any) {
	providers, _ := node.(map[string]any)
	for _, groups := range providers {
		stripAPIKeysAuthIndexesFromGroups(groups)
	}
}

func stripAPIKeysAuthIndexesFromRoot(root map[string]any) {
	stripAPIKeysAuthIndexesFromProvidersMap(root["api-keys"])
}

func stripAPIKeysAuthIndexesFromUpdate(parts []string, update any) {
	if len(parts) == 0 {
		root, _ := update.(map[string]any)
		stripAPIKeysAuthIndexesFromRoot(root)
		return
	}
	if parts[0] != "api-keys" {
		return
	}
	if len(parts) == 1 {
		stripAPIKeysAuthIndexesFromProvidersMap(update)
		return
	}
	stripAPIKeysAuthIndexesFromGroups(update)
}

func configMapScalarPresent(node any, key string) (string, bool) {
	mapping, ok := node.(map[string]any)
	if !ok {
		return "", false
	}
	value, present := mapping[key]
	if !present || value == nil {
		return "", false
	}
	switch value.(type) {
	case map[string]any, []any:
		return "", true
	default:
		return strings.TrimSpace(fmt.Sprint(value)), true
	}
}

func configMapHeadersPresent(node any, key string) (map[string]string, bool) {
	mapping, ok := node.(map[string]any)
	if !ok {
		return nil, false
	}
	headers, ok := mapping[key].(map[string]any)
	if !ok {
		return nil, false
	}
	out := make(map[string]string, len(headers))
	for name, value := range headers {
		switch value.(type) {
		case map[string]any, []any:
			return nil, false
		case nil:
			out[name] = ""
		default:
			out[name] = fmt.Sprint(value)
		}
	}
	return out, true
}

func resolveInheritedScalar(keyNode, groupNode any, field string) string {
	if val, ok := configMapScalarPresent(keyNode, field); ok {
		return val
	}
	if val, ok := configMapScalarPresent(groupNode, field); ok {
		return val
	}
	return ""
}

func resolveInheritedHeaders(keyNode, groupNode any, field string) map[string]string {
	if h, ok := configMapHeadersPresent(keyNode, field); ok {
		return h
	}
	if h, ok := configMapHeadersPresent(groupNode, field); ok {
		return h
	}
	return nil
}

func normalizeModelPrefixHelper(prefix string) string {
	trimmed := strings.TrimSpace(prefix)
	trimmed = strings.Trim(trimmed, "/")
	if trimmed == "" || strings.Contains(trimmed, "/") {
		return ""
	}
	return trimmed
}

func formatCredentialDedupKey(key, base, proxyURL, prefix string, headers map[string]string) string {
	var b strings.Builder
	b.WriteString(strings.TrimSpace(key))
	b.WriteByte(0)
	b.WriteString(strings.TrimSpace(base))
	b.WriteByte(0)
	b.WriteString(strings.TrimSpace(proxyURL))
	b.WriteByte(0)
	b.WriteString(normalizeModelPrefixHelper(prefix))
	b.WriteByte(0)
	b.WriteString(config.FormatSortedHeaders(config.NormalizeHeaders(headers)))
	return b.String()
}

func (h *Handler) injectV8APIKeyAuthIndexesLocked(root map[string]any, data []byte) {
	if h == nil || root == nil {
		return
	}
	apiKeysNode, ok := configV8Node(root, []string{"api-keys"}).(map[string]any)
	if !ok {
		return
	}

	cfg, errParse := config.ParseConfigBytes(data)
	if errParse != nil {
		cfg = h.cfg
	}
	if cfg == nil {
		return
	}

	synth := synthesizer.NewConfigSynthesizer()
	synthCtx := &synthesizer.SynthesisContext{
		Config:      cfg,
		Now:         time.Now(),
		IDGenerator: synthesizer.NewStableIDGenerator(),
	}
	auths, errSynth := synth.Synthesize(synthCtx)
	if errSynth != nil {
		return
	}

	liveIndexByID := liveAuthIndexFromManager(h.authManager)
	resolveAuthIndex := func(a *coreauth.Auth) string {
		if a == nil {
			return ""
		}
		if idx := strings.TrimSpace(liveIndexByID[a.ID]); idx != "" {
			return idx
		}
		return a.EnsureIndex()
	}

	authsByProvider := make(map[string][]*coreauth.Auth)
	for _, a := range auths {
		if a != nil {
			authsByProvider[a.Provider] = append(authsByProvider[a.Provider], a)
		}
	}

	providerAuthMap := make(map[string]map[string]string)
	for _, p := range []string{"gemini", "interactions", "vertex"} {
		providerAuthMap[p] = make(map[string]string)
	}

	for _, a := range authsByProvider["gemini"] {
		if a == nil || a.Attributes == nil {
			continue
		}
		idx, errConv := strconv.Atoi(a.Attributes["config_index"])
		if errConv != nil || idx < 0 || idx >= len(cfg.GeminiKey) {
			continue
		}
		e := cfg.GeminiKey[idx]
		key := formatCredentialDedupKey(e.APIKey, e.BaseURL, e.ProxyURL, e.Prefix, e.Headers)
		providerAuthMap["gemini"][key] = resolveAuthIndex(a)
	}
	for _, a := range authsByProvider["gemini-interactions"] {
		if a == nil || a.Attributes == nil {
			continue
		}
		idx, errConv := strconv.Atoi(a.Attributes["config_index"])
		if errConv != nil || idx < 0 || idx >= len(cfg.InteractionsKey) {
			continue
		}
		e := cfg.InteractionsKey[idx]
		key := formatCredentialDedupKey(e.APIKey, e.BaseURL, e.ProxyURL, e.Prefix, e.Headers)
		providerAuthMap["interactions"][key] = resolveAuthIndex(a)
	}
	for _, a := range authsByProvider["vertex"] {
		if a == nil || a.Attributes == nil {
			continue
		}
		idx, errConv := strconv.Atoi(a.Attributes["config_index"])
		if errConv != nil || idx < 0 || idx >= len(cfg.VertexCompatAPIKey) {
			continue
		}
		e := cfg.VertexCompatAPIKey[idx]
		key := strings.TrimSpace(e.APIKey) + "|" + strings.TrimSpace(e.BaseURL)
		providerAuthMap["vertex"][key] = resolveAuthIndex(a)
	}

	authByConfigIndex := make(map[string]map[string]*coreauth.Auth)
	for _, p := range []string{"codex", "claude", "xai", "meta"} {
		authByConfigIndex[p] = make(map[string]*coreauth.Auth)
		for _, a := range authsByProvider[p] {
			if a != nil && a.Attributes != nil {
				authByConfigIndex[p][a.Attributes["config_index"]] = a
			}
		}
	}

	for provider, value := range apiKeysNode {
		providerName := strings.TrimSpace(provider)
		groupsNode, ok := value.([]any)
		if !ok {
			continue
		}

		if providerName == "openai-compatibility" {
			validGroupIdx := 0
			for _, value := range groupsNode {
				group, ok := value.(map[string]any)
				if !ok {
					continue
				}
				groupBase := strings.TrimSpace(configMapScalar(group, "base-url"))
				if groupBase == "" {
					continue
				}
				targetGroupIdxStr := strconv.Itoa(validGroupIdx)
				validGroupIdx++

				isOpenAICompatAuth := func(a *coreauth.Auth) bool {
					if a == nil || a.Attributes == nil {
						return false
					}
					if a.Attributes["config_index"] != targetGroupIdxStr {
						return false
					}
					return a.Provider == "openai-compatibility" || strings.HasPrefix(a.Provider, "openai-compatible-")
				}

				keysNode, _ := configV8Node(group, []string{"keys"}).([]any)
				if len(keysNode) == 0 {
					for _, a := range auths {
						if isOpenAICompatAuth(a) && a.Attributes["api_key"] == "" {
							if idx := resolveAuthIndex(a); idx != "" {
								setMapScalar(group, "auth_index", idx)
							}
							break
						}
					}
					continue
				}

				var groupAuths []*coreauth.Auth
				for _, a := range auths {
					if isOpenAICompatAuth(a) {
						groupAuths = append(groupAuths, a)
					}
				}

				for kIdx, value := range keysNode {
					keyNode, ok := value.(map[string]any)
					if !ok {
						continue
					}
					if kIdx < len(groupAuths) {
						if idx := resolveAuthIndex(groupAuths[kIdx]); idx != "" {
							setMapScalar(keyNode, "auth_index", idx)
						}
					}
				}
			}
			continue
		}

		if providerName == "claude" {
			rawIdx := 0
			for _, value := range groupsNode {
				group, ok := value.(map[string]any)
				if !ok {
					continue
				}
				groupBase := strings.TrimSpace(configMapScalar(group, "base-url"))
				keysNode, ok := configV8Node(group, []string{"keys"}).([]any)
				if !ok {
					continue
				}
				for _, value := range keysNode {
					keyNode, ok := value.(map[string]any)
					if !ok {
						continue
					}
					apiKey := strings.TrimSpace(configMapScalar(keyNode, "api-key"))
					targetIdx := strconv.Itoa(rawIdx)
					rawIdx++
					if apiKey == "" && groupBase == "" {
						continue
					}
					if a := authByConfigIndex["claude"][targetIdx]; a != nil {
						if idx := resolveAuthIndex(a); idx != "" {
							setMapScalar(keyNode, "auth_index", idx)
						}
					}
				}
			}
			continue
		}

		if providerName == "codex" || providerName == "xai" {
			validIdx := 0
			for _, value := range groupsNode {
				group, ok := value.(map[string]any)
				if !ok {
					continue
				}
				groupBase := strings.TrimSpace(configMapScalar(group, "base-url"))
				if groupBase == "" {
					continue
				}
				keysNode, ok := configV8Node(group, []string{"keys"}).([]any)
				if !ok {
					continue
				}
				for _, value := range keysNode {
					keyNode, ok := value.(map[string]any)
					if !ok {
						continue
					}
					targetIdx := strconv.Itoa(validIdx)
					validIdx++
					if a := authByConfigIndex[providerName][targetIdx]; a != nil {
						if idx := resolveAuthIndex(a); idx != "" {
							setMapScalar(keyNode, "auth_index", idx)
						}
					}
				}
			}
			continue
		}

		if providerName == "meta" {
			validIdx := 0
			for _, value := range groupsNode {
				group, ok := value.(map[string]any)
				if !ok {
					continue
				}
				keysNode, ok := configV8Node(group, []string{"keys"}).([]any)
				if !ok {
					continue
				}
				for _, value := range keysNode {
					keyNode, ok := value.(map[string]any)
					if !ok {
						continue
					}
					apiKey := strings.TrimSpace(configMapScalar(keyNode, "api-key"))
					if apiKey == "" || strings.HasPrefix(apiKey, "dca:") {
						continue
					}
					targetIdx := strconv.Itoa(validIdx)
					validIdx++
					if a := authByConfigIndex["meta"][targetIdx]; a != nil {
						if idx := resolveAuthIndex(a); idx != "" {
							setMapScalar(keyNode, "auth_index", idx)
						}
					}
				}
			}
			continue
		}

		for _, value := range groupsNode {
			group, ok := value.(map[string]any)
			if !ok {
				continue
			}
			groupBase := strings.TrimSpace(configMapScalar(group, "base-url"))
			keysNode, ok := configV8Node(group, []string{"keys"}).([]any)
			if !ok {
				continue
			}
			for _, value := range keysNode {
				keyNode, ok := value.(map[string]any)
				if !ok {
					continue
				}
				apiKey := strings.TrimSpace(configMapScalar(keyNode, "api-key"))
				if providerName == "vertex" {
					if idx, ok := providerAuthMap["vertex"][apiKey+"|"+groupBase]; ok {
						setMapScalar(keyNode, "auth_index", idx)
					}
					continue
				}

				proxyURL := resolveInheritedScalar(keyNode, group, "proxy-url")
				prefix := resolveInheritedScalar(keyNode, group, "prefix")
				headers := resolveInheritedHeaders(keyNode, group, "headers")
				dedupKey := formatCredentialDedupKey(apiKey, groupBase, proxyURL, prefix, headers)
				if idx, ok := providerAuthMap[providerName][dedupKey]; ok {
					setMapScalar(keyNode, "auth_index", idx)
				}
			}
		}
	}
}

func (h *Handler) geminiKeysWithAuthIndex() []geminiKeyWithAuthIndex {
	if h == nil {
		return nil
	}
	liveIndexByID := h.liveAuthIndexByID()

	h.mu.Lock()
	defer h.mu.Unlock()
	if h.cfg == nil {
		return nil
	}

	idGen := synthesizer.NewStableIDGenerator()
	out := make([]geminiKeyWithAuthIndex, len(h.cfg.GeminiKey))
	for i := range h.cfg.GeminiKey {
		entry := h.cfg.GeminiKey[i]
		authIndex := ""
		key := strings.TrimSpace(entry.APIKey)
		base := strings.TrimSpace(entry.BaseURL)
		proxyURL := strings.TrimSpace(entry.ProxyURL)
		prefix := strings.TrimSpace(entry.Prefix)
		if key != "" || base != "" {
			id, _ := idGen.Next("gemini:apikey", key, base, proxyURL, prefix, config.FormatSortedHeaders(entry.Headers))
			authIndex = liveIndexByID[id]
		}
		out[i] = geminiKeyWithAuthIndex{
			GeminiKey: entry,
			AuthIndex: authIndex,
		}
	}
	return out
}

func (h *Handler) interactionsKeysWithAuthIndex() []geminiKeyWithAuthIndex {
	if h == nil {
		return nil
	}
	liveIndexByID := h.liveAuthIndexByID()

	h.mu.Lock()
	defer h.mu.Unlock()
	if h.cfg == nil {
		return nil
	}

	idGen := synthesizer.NewStableIDGenerator()
	out := make([]geminiKeyWithAuthIndex, len(h.cfg.InteractionsKey))
	for i := range h.cfg.InteractionsKey {
		entry := h.cfg.InteractionsKey[i]
		authIndex := ""
		key := strings.TrimSpace(entry.APIKey)
		base := strings.TrimSpace(entry.BaseURL)
		proxyURL := strings.TrimSpace(entry.ProxyURL)
		prefix := strings.TrimSpace(entry.Prefix)
		if key != "" || base != "" {
			id, _ := idGen.Next("gemini-interactions:apikey", key, base, proxyURL, prefix, config.FormatSortedHeaders(entry.Headers))
			authIndex = liveIndexByID[id]
		}
		out[i] = geminiKeyWithAuthIndex{
			GeminiKey: entry,
			AuthIndex: authIndex,
		}
	}
	return out
}

func (h *Handler) claudeKeysWithAuthIndex() []claudeKeyWithAuthIndex {
	if h == nil {
		return nil
	}
	liveIndexByID := h.liveAuthIndexByID()

	h.mu.Lock()
	defer h.mu.Unlock()
	if h.cfg == nil {
		return nil
	}

	idGen := synthesizer.NewStableIDGenerator()
	out := make([]claudeKeyWithAuthIndex, len(h.cfg.ClaudeKey))
	for i := range h.cfg.ClaudeKey {
		entry := h.cfg.ClaudeKey[i]
		authIndex := ""
		key := strings.TrimSpace(entry.APIKey)
		base := strings.TrimSpace(entry.BaseURL)
		proxyURL := strings.TrimSpace(entry.ProxyURL)
		prefix := strings.TrimSpace(entry.Prefix)
		if key != "" || base != "" {
			id, _ := idGen.Next("claude:apikey", key, base, proxyURL, prefix, config.FormatSortedHeaders(entry.Headers))
			authIndex = liveIndexByID[id]
		}
		out[i] = claudeKeyWithAuthIndex{
			ClaudeKey: entry,
			AuthIndex: authIndex,
		}
	}
	return out
}

func (h *Handler) codexKeysWithAuthIndex() []codexKeyWithAuthIndex {
	if h == nil {
		return nil
	}
	liveIndexByID := h.liveAuthIndexByID()

	h.mu.Lock()
	defer h.mu.Unlock()
	if h.cfg == nil {
		return nil
	}

	idGen := synthesizer.NewStableIDGenerator()
	out := make([]codexKeyWithAuthIndex, len(h.cfg.CodexKey))
	for i := range h.cfg.CodexKey {
		entry := h.cfg.CodexKey[i]
		authIndex := ""
		key := strings.TrimSpace(entry.APIKey)
		base := strings.TrimSpace(entry.BaseURL)
		proxyURL := strings.TrimSpace(entry.ProxyURL)
		prefix := strings.TrimSpace(entry.Prefix)
		if key != "" || base != "" {
			id, _ := idGen.Next("codex:apikey", key, base, proxyURL, prefix, config.FormatSortedHeaders(entry.Headers))
			authIndex = liveIndexByID[id]
		}
		out[i] = codexKeyWithAuthIndex{
			CodexKey:  entry,
			AuthIndex: authIndex,
		}
	}
	return out
}

func (h *Handler) xaiKeysWithAuthIndex() []xaiKeyWithAuthIndex {
	if h == nil {
		return nil
	}
	liveIndexByID := h.liveAuthIndexByID()

	h.mu.Lock()
	defer h.mu.Unlock()
	if h.cfg == nil {
		return nil
	}

	idGen := synthesizer.NewStableIDGenerator()
	out := make([]xaiKeyWithAuthIndex, len(h.cfg.XAIKey))
	for i := range h.cfg.XAIKey {
		entry := h.cfg.XAIKey[i]
		authIndex := ""
		key := strings.TrimSpace(entry.APIKey)
		base := strings.TrimSpace(entry.BaseURL)
		proxyURL := strings.TrimSpace(entry.ProxyURL)
		prefix := strings.TrimSpace(entry.Prefix)
		if key != "" || base != "" {
			id, _ := idGen.Next("xai:apikey", key, base, proxyURL, prefix, config.FormatSortedHeaders(entry.Headers))
			authIndex = liveIndexByID[id]
		}
		out[i] = xaiKeyWithAuthIndex{
			XAIKey:    entry,
			AuthIndex: authIndex,
		}
	}
	return out
}

func (h *Handler) metaKeysWithAuthIndex() []metaKeyWithAuthIndex {
	if h == nil {
		return nil
	}
	liveIndexByID := h.liveAuthIndexByID()

	h.mu.Lock()
	defer h.mu.Unlock()
	if h.cfg == nil {
		return nil
	}

	idGen := synthesizer.NewStableIDGenerator()
	out := make([]metaKeyWithAuthIndex, len(h.cfg.MetaKey))
	for i := range h.cfg.MetaKey {
		entry := h.cfg.MetaKey[i]
		authIndex := ""
		key := strings.TrimSpace(entry.APIKey)
		base := strings.TrimSpace(entry.BaseURL)
		proxyURL := strings.TrimSpace(entry.ProxyURL)
		prefix := strings.TrimSpace(entry.Prefix)
		if key != "" || base != "" {
			id, _ := idGen.Next("meta:apikey", key, base, proxyURL, prefix, config.FormatSortedHeaders(entry.Headers))
			authIndex = liveIndexByID[id]
		}
		out[i] = metaKeyWithAuthIndex{
			MetaKey:   entry,
			AuthIndex: authIndex,
		}
	}
	return out
}

func (h *Handler) vertexCompatKeysWithAuthIndex() []vertexCompatKeyWithAuthIndex {
	if h == nil {
		return nil
	}
	liveIndexByID := h.liveAuthIndexByID()

	h.mu.Lock()
	defer h.mu.Unlock()
	if h.cfg == nil {
		return nil
	}

	idGen := synthesizer.NewStableIDGenerator()
	out := make([]vertexCompatKeyWithAuthIndex, len(h.cfg.VertexCompatAPIKey))
	for i := range h.cfg.VertexCompatAPIKey {
		entry := h.cfg.VertexCompatAPIKey[i]
		id, _ := idGen.Next("vertex:apikey", entry.APIKey, entry.BaseURL, entry.ProxyURL)
		authIndex := liveIndexByID[id]
		out[i] = vertexCompatKeyWithAuthIndex{
			VertexCompatKey: entry,
			AuthIndex:       authIndex,
		}
	}
	return out
}

func (h *Handler) openAICompatibilityWithAuthIndex() []openAICompatibilityWithAuthIndex {
	if h == nil {
		return nil
	}
	liveIndexByID := h.liveAuthIndexByID()

	h.mu.Lock()
	defer h.mu.Unlock()
	if h.cfg == nil {
		return nil
	}

	normalized := normalizedOpenAICompatibilityEntries(h.cfg.OpenAICompatibility)
	out := make([]openAICompatibilityWithAuthIndex, len(normalized))
	idGen := synthesizer.NewStableIDGenerator()
	for i := range normalized {
		entry := normalized[i]
		providerName := strings.ToLower(strings.TrimSpace(entry.Name))
		if providerName == "" {
			providerName = "openai-compatibility"
		}
		idKind := fmt.Sprintf("openai-compatibility:%s", providerName)

		response := openAICompatibilityWithAuthIndex{
			Name:                  entry.Name,
			Priority:              entry.Priority,
			Disabled:              entry.Disabled,
			Prefix:                entry.Prefix,
			BaseURL:               entry.BaseURL,
			Models:                entry.Models,
			Headers:               entry.Headers,
			SupportPromptCacheKey: entry.SupportPromptCacheKey,
			DisableCooling:        entry.DisableCooling,
			RequestRetry:          entry.RequestRetry,
			RequestScopedErrors:   entry.RequestScopedErrors,
			AuthIndex:             "",
		}
		if len(entry.APIKeyEntries) == 0 {
			id, _ := idGen.Next(idKind, entry.BaseURL)
			response.AuthIndex = liveIndexByID[id]
		} else {
			response.APIKeyEntries = make([]openAICompatibilityAPIKeyWithAuthIndex, len(entry.APIKeyEntries))
			for j := range entry.APIKeyEntries {
				apiKeyEntry := entry.APIKeyEntries[j]
				id, _ := idGen.Next(idKind, apiKeyEntry.APIKey, entry.BaseURL, apiKeyEntry.ProxyURL)
				response.APIKeyEntries[j] = openAICompatibilityAPIKeyWithAuthIndex{
					OpenAICompatibilityAPIKey: apiKeyEntry,
					AuthIndex:                 liveIndexByID[id],
				}
			}
		}
		out[i] = response
	}
	return out
}

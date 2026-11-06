package config

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"strings"
	"syscall"

	log "github.com/sirupsen/logrus"
)

// LoadConfig loads the canonical TOML file, importing a legacy YAML sibling once.
func LoadConfig(configFile string) (*Config, error) { return LoadConfigOptional(configFile, false) }
func ComputeSourceHash(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// PrepareConfigFileForUpload removes plaintext management secrets before a local
// configuration is sent to storage. A rejected document remains untouched.
func PrepareConfigFileForUpload(configFile string) error {
	resolved, err := ResolveConfigPath(configFile)
	if err != nil {
		return err
	}
	data, err := os.ReadFile(resolved)
	if err != nil {
		return err
	}
	prepared, err := prepareConfigForUpload(data)
	if err != nil {
		return err
	}
	if bytes.Equal(prepared, data) {
		return nil
	}
	return WriteConfigFile(resolved, prepared, 0600)
}
func prepareConfigForUpload(data []byte) ([]byte, error) {
	cfg, err := DecodeConfig(data, ConfigFormatTOML)
	if err != nil {
		return nil, fmt.Errorf("failed to parse config file: %w", err)
	}
	prepared := data
	if secret := cfg.RemoteManagement.SecretKey; secret != "" && !looksLikeBcrypt(secret) {
		hashed, err := hashSecret(secret)
		if err != nil {
			return nil, fmt.Errorf("failed to hash remote management key: %w", err)
		}
		prepared, err = updateNestedScalarTOML(data, managementSecretKeyPath(data), hashed)
		if err != nil {
			return nil, err
		}
	}
	cleaned, _, err := NormalizeConfigLayout(prepared, false)
	if err != nil {
		return nil, err
	}
	root, err := ParseConfigDocument(cleaned)
	if err != nil {
		return nil, err
	}
	for _, plaintext := range plaintextManagementSecrets(data, cfg.RemoteManagement.SecretKey) {
		if nativeScalarExists(root, plaintext) {
			return nil, errors.New("plaintext remote management key is still present elsewhere in the config file")
		}
	}
	return cleaned, nil
}
func plaintextManagementSecrets(data []byte, effective string) []string {
	candidates := []string{effective}
	root, err := ParseConfigDocument(data)
	if err == nil {
		for _, path := range []string{"remote-management.secret-key", "management.secret-key"} {
			if value, ok := mapPath(root, path); ok {
				if secret, ok := value.(string); ok {
					candidates = append(candidates, secret)
				}
			}
		}
	}
	var out []string
	for _, secret := range candidates {
		if secret != "" && !looksLikeBcrypt(secret) {
			out = append(out, secret)
		}
	}
	return out
}
func nativeScalarExists(value any, want string) bool {
	switch v := value.(type) {
	case string:
		return v == want
	case map[string]any:
		for _, child := range v {
			if nativeScalarExists(child, want) {
				return true
			}
		}
	case []any:
		for _, child := range v {
			if nativeScalarExists(child, want) {
				return true
			}
		}
	}
	return false
}
func managementSecretKeyPath(data []byte) []string {
	root, err := ParseConfigDocument(data)
	if err == nil {
		if _, present := mapPath(root, "management.secret-key"); present {
			return []string{"management", "secret-key"}
		}
	}
	return []string{"remote-management", "secret-key"}
}

// applyLoadDefaults sets the values a config file decodes over, so that absent
// keys keep these defaults.
func applyLoadDefaults(cfg *Config) {
	cfg.Host = "" // Default empty: binds to all interfaces (IPv4 + IPv6)
	cfg.LoggingToFile = false
	cfg.LogsMaxTotalSizeMB = 0
	cfg.ErrorLogsMaxFiles = 10
	cfg.UsageStatisticsEnabled = false
	cfg.RedisUsageQueueRetentionSeconds = 60
	cfg.DisableCooling = false
	cfg.SaveCooldownStatus = false
	cfg.TransientErrorCooldownSeconds = 0
	cfg.DisableImageGeneration = DisableImageGenerationOff
	cfg.WebsocketAuth = true
	cfg.Pprof.Enable = false
	cfg.Pprof.Addr = DefaultPprofAddr
	cfg.Discovery.Enabled = false
	cfg.Discovery.ServiceType = DefaultDiscoveryServiceType
	cfg.Discovery.Subtypes = []string{"_chat-completions", "_responses", "_messages", "_generate-content", "_interactions"}
	cfg.RemoteManagement.PanelGitHubRepository = DefaultPanelGitHubRepository
	cfg.CredentialInFlight = DefaultCredentialInFlightConfig()
}

// LoadConfigOptional loads native TOML after resolving any one-time import.
func LoadConfigOptional(configFile string, optional bool) (*Config, error) {
	resolved, err := ResolveConfigPath(configFile)
	if err != nil {
		return nil, err
	}
	configFile = resolved
	raw, err := os.ReadFile(configFile)
	empty := func() *Config {
		cfg := &Config{CredentialInFlight: DefaultCredentialInFlightConfig()}
		cfg.NormalizePluginsConfig()
		cfg.NormalizeClaudeConfig()
		return cfg
	}
	if err != nil {
		if optional && (os.IsNotExist(err) || errors.Is(err, syscall.EISDIR)) {
			return empty(), nil
		}
		return nil, fmt.Errorf("failed to read config file: %w", err)
	}
	if optional && len(bytes.TrimSpace(raw)) == 0 {
		return empty(), nil
	}
	decoded, err := DecodeConfig(raw, ConfigFormatTOML)
	if err != nil {
		if optional {
			return empty(), nil
		}
		return nil, fmt.Errorf("failed to parse config file: %w", err)
	}
	cfg := *decoded
	if errValidate := validateTrustedProxies(cfg.TrustedProxies); errValidate != nil {
		return nil, errValidate
	}

	cfg.CredentialConcurrency = cfg.CredentialConcurrency.WithDefaults()
	if errValidate := cfg.CredentialInFlight.Validate(); errValidate != nil {
		return nil, errValidate
	}
	if cfg.Discovery.ServiceType == "" {
		cfg.Discovery.ServiceType = DefaultDiscoveryServiceType
	}
	if len(cfg.Discovery.Subtypes) == 0 {
		cfg.Discovery.Subtypes = []string{"_chat-completions", "_responses", "_messages", "_generate-content", "_interactions"}
	}
	if errValidate := cfg.Codex.LiveMediaRelay.Validate(); errValidate != nil {
		return nil, errValidate
	}
	if errValidate := cfg.ValidateCredentialWeights(); errValidate != nil {
		return nil, errValidate
	}

	// Hash remote management key if plaintext is detected (nested)
	// We consider a value to be already hashed if it looks like a bcrypt hash ($2a$, $2b$, or $2y$ prefix).
	if cfg.RemoteManagement.SecretKey != "" && !looksLikeBcrypt(cfg.RemoteManagement.SecretKey) {
		hashed, errHash := hashSecret(cfg.RemoteManagement.SecretKey)
		if errHash != nil {
			return nil, fmt.Errorf("failed to hash remote management key: %w", errHash)
		}
		cfg.RemoteManagement.SecretKey = hashed

		// Persist the hashed value back to the config file to avoid re-hashing on next startup.
		// Preserve TOML comments and ordering; update only the nested key.
		_ = SaveConfigPreserveCommentsUpdateNestedScalar(configFile, managementSecretKeyPath(raw), hashed)
	}

	cfg.RemoteManagement.PanelGitHubRepository = strings.TrimSpace(cfg.RemoteManagement.PanelGitHubRepository)
	if cfg.RemoteManagement.PanelGitHubRepository == "" {
		cfg.RemoteManagement.PanelGitHubRepository = DefaultPanelGitHubRepository
	}

	cfg.Pprof.Addr = strings.TrimSpace(cfg.Pprof.Addr)
	if cfg.Pprof.Addr == "" {
		cfg.Pprof.Addr = DefaultPprofAddr
	}

	if cfg.LogsMaxTotalSizeMB < 0 {
		cfg.LogsMaxTotalSizeMB = 0
	}

	if cfg.ErrorLogsMaxFiles < 0 {
		cfg.ErrorLogsMaxFiles = 10
	}

	if cfg.RedisUsageQueueRetentionSeconds <= 0 {
		cfg.RedisUsageQueueRetentionSeconds = 60
	} else if cfg.RedisUsageQueueRetentionSeconds > 3600 {
		log.WithField("value", cfg.RedisUsageQueueRetentionSeconds).Warn("redis-usage-queue-retention-seconds too large; clamping to 3600")
		cfg.RedisUsageQueueRetentionSeconds = 3600
	}

	if cfg.MaxRetryCredentials < 0 {
		cfg.MaxRetryCredentials = 0
	}

	cfg.NormalizePluginsConfig()
	cfg.NormalizeClaudeConfig()
	if errResolvePluginsDir := cfg.ResolvePluginsDir(); errResolvePluginsDir != nil && cfg.Plugins.Enabled {
		return nil, errResolvePluginsDir
	}

	// Sanitize Gemini API key configuration and migrate legacy entries.
	cfg.SanitizeGeminiKeys()

	// Sanitize native Interactions API key configuration.
	cfg.SanitizeInteractionsKeys()

	// Sanitize Vertex-compatible API keys.
	cfg.SanitizeVertexCompatKeys()

	// Sanitize Codex keys: drop entries without base-url
	cfg.SanitizeCodexKeys()

	// Sanitize xAI keys: drop entries without base-url
	cfg.SanitizeXAIKeys()

	// Sanitize Meta keys.
	cfg.SanitizeMetaKeys()

	// Sanitize GitLab Duo Agent keys.
	cfg.SanitizeGitLabDuoAgentKeys()

	// Sanitize Claude header defaults.
	cfg.SanitizeClaudeHeaderDefaults()

	// Sanitize Claude key headers
	cfg.SanitizeClaudeKeys()

	// Sanitize OpenAI compatibility providers: drop entries without base-url
	cfg.SanitizeOpenAICompatibility()

	// Normalize OAuth provider model exclusion map.
	cfg.OAuthExcludedModels = NormalizeOAuthExcludedModels(cfg.OAuthExcludedModels)

	// Normalize global OAuth model name aliases.
	cfg.SanitizeOAuthModelAlias()

	// Normalize global OAuth model settings.
	cfg.SanitizeOAuthSettings()

	// Normalize global OAuth request-scoped error rules.
	cfg.SanitizeOAuthRequestScopedErrors()

	// Validate raw payload rules and drop invalid entries.
	cfg.SanitizePayloadRules()

	current, err := os.ReadFile(configFile)
	if err != nil {
		return nil, err
	}
	cleaned, changed, err := NormalizeConfigLayout(current, false)
	if err != nil {
		return nil, err
	}
	if changed {
		if err = WriteConfigFile(configFile, cleaned, 0600); err != nil {
			return nil, fmt.Errorf("clean conflicting config fields: %w", err)
		}
	}
	cfg.SourceHash = ComputeSourceHash(raw)
	return &cfg, nil
}

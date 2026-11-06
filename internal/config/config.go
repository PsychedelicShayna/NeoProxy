// Package config provides configuration management for the CLI Proxy API server.
// It handles loading and parsing native TOML configuration files, and provides structured
// access to application settings including server port, authentication directory,
// debug settings, proxy configuration, and API keys.
package config

// Config represents the application's configuration, loaded from a TOML file.
type Config struct {
	// Models selects optional catalog sources independently for each catalog.
	Models ModelCatalogs `yaml:"models" toml:"models" json:"models"`

	SDKConfig `yaml:",inline" toml:""`
	// Host is the network host/interface on which the API server will bind.
	// Default is empty ("") to bind all interfaces (IPv4 + IPv6). Use "127.0.0.1" or "localhost" for local-only access.
	Host string `yaml:"host" toml:"host" json:"-"`
	// Port is the network port on which the API server will listen.
	Port int `yaml:"port" toml:"port" json:"-"`

	// GitHubToken is the global token for GitHub requests, taking precedence over GITHUB_TOKEN.
	GitHubToken string `yaml:"github-token" toml:"github-token" json:"-"`

	// TrustedProxies lists the IPs or CIDRs allowed to provide forwarded client IP headers.
	// The server applies this list at startup; changing it requires a restart.
	TrustedProxies []string `yaml:"trusted-proxies" toml:"trusted-proxies" json:"trusted-proxies"`

	// TLS config controls HTTPS server settings.
	TLS TLSConfig `yaml:"tls" toml:"tls" json:"tls"`

	// Home config is runtime-only and is populated from -home-jwt.
	Home HomeConfig `yaml:"-" toml:"-" json:"-"`

	// SourceHash is runtime-only: the SHA-256 of the config file bytes this config was
	// parsed from, or empty when the config was not loaded from a file.
	SourceHash string `yaml:"-" toml:"-" json:"-"`

	// CredentialConcurrency contains Home-authoritative credential lifecycle settings.
	CredentialConcurrency CredentialConcurrencyConfig `yaml:"credential-concurrency" toml:"credential-concurrency" json:"credential-concurrency"`

	// CredentialInFlight configures credential observation snapshots.
	CredentialInFlight CredentialInFlightConfig `yaml:"credential-in-flight" toml:"credential-in-flight" json:"credential-in-flight"`

	// RemoteManagement nests management-related options under 'remote-management'.
	RemoteManagement RemoteManagement `yaml:"remote-management" toml:"remote-management" json:"-"`

	// Plugins configures dynamic plugin discovery and per-plugin settings.
	Plugins PluginsConfig `yaml:"plugins" toml:"plugins" json:"plugins"`

	// AuthDir is the directory where authentication token files are stored.
	AuthDir string `yaml:"auth-dir" toml:"auth-dir" json:"-"`

	// Debug enables or disables debug-level logging and other debug features.
	Debug bool `yaml:"debug" toml:"debug" json:"debug"`

	// Pprof config controls the optional pprof HTTP debug server.
	Pprof PprofConfig `yaml:"pprof" toml:"pprof" json:"pprof"`

	// Discovery configures local network mDNS / DNS-SD service advertising.
	Discovery DiscoveryConfig `yaml:"discovery" toml:"discovery" json:"discovery"`

	// CommercialMode disables high-overhead request logging and HTTP middleware features to minimize per-request memory usage.
	CommercialMode bool `yaml:"commercial-mode" toml:"commercial-mode" json:"commercial-mode"`

	// LoggingToFile controls whether application logs are written to rotating files or stdout.
	LoggingToFile bool `yaml:"logging-to-file" toml:"logging-to-file" json:"logging-to-file"`

	// LogsMaxTotalSizeMB limits the total size (in MB) of log files under the logs directory.
	// When exceeded, the oldest log files are deleted until within the limit. Set to 0 to disable.
	LogsMaxTotalSizeMB int `yaml:"logs-max-total-size-mb" toml:"logs-max-total-size-mb" json:"logs-max-total-size-mb"`

	// ErrorLogsMaxFiles limits the number of error log files retained when request logging is disabled.
	// When exceeded, the oldest error log files are deleted. Default is 10. Set to 0 to disable cleanup.
	ErrorLogsMaxFiles int `yaml:"error-logs-max-files" toml:"error-logs-max-files" json:"error-logs-max-files"`

	// UsageStatisticsEnabled toggles in-memory usage aggregation; when false, usage data is discarded.
	UsageStatisticsEnabled bool `yaml:"usage-statistics-enabled" toml:"usage-statistics-enabled" json:"usage-statistics-enabled"`

	// RedisUsageQueueRetentionSeconds controls how long usage queue items are retained
	// in memory for Management API consumers.
	// Default: 60. Max: 3600.
	RedisUsageQueueRetentionSeconds int `yaml:"redis-usage-queue-retention-seconds" toml:"redis-usage-queue-retention-seconds" json:"redis-usage-queue-retention-seconds"`

	// DisableCooling disables auth/model cooldown scheduling when true unless a credential or provider overrides it.
	DisableCooling bool `yaml:"disable-cooling" toml:"disable-cooling" json:"disable-cooling"`

	// SaveCooldownStatus persists runtime cooldown status next to auth files when true.
	SaveCooldownStatus bool `yaml:"save-cooldown-status" toml:"save-cooldown-status" json:"save-cooldown-status"`

	// TransientErrorCooldownSeconds controls cooldowns for transient upstream errors (408/500/502/503/504/520-526).
	// 0 keeps the legacy default cooldown. Negative values disable these cooldowns.
	TransientErrorCooldownSeconds int `yaml:"transient-error-cooldown-seconds" toml:"transient-error-cooldown-seconds" json:"transient-error-cooldown-seconds"`

	// AuthAutoRefreshWorkers overrides the size of the core auth auto-refresh and manual refresh-all worker pool.
	// When <= 0, the default worker count is used.
	AuthAutoRefreshWorkers int `yaml:"auth-auto-refresh-workers" toml:"auth-auto-refresh-workers" json:"auth-auto-refresh-workers"`

	// RequestRetry defines the number of additional credential retry rounds after
	// the first round has exhausted its eligible credentials.
	RequestRetry int `yaml:"request-retry" toml:"request-retry" json:"request-retry"`
	// MaxRetryCredentials defines the maximum number of different credentials to
	// try in each credential retry round.
	// Set to 0 or a negative value to keep trying all available credentials (legacy behavior).
	MaxRetryCredentials int `yaml:"max-retry-credentials" toml:"max-retry-credentials" json:"max-retry-credentials"`
	// MaxRetryInterval defines the maximum positive cooldown wait, in seconds,
	// allowed before starting another credential retry round. A non-positive value
	// forbids positive cooldown waits; it does not disable same-round credential
	// failover or immediate additional rounds allowed by RequestRetry.
	MaxRetryInterval int `yaml:"max-retry-interval" toml:"max-retry-interval" json:"max-retry-interval"`

	// QuotaExceeded defines the behavior when a quota is exceeded.
	QuotaExceeded QuotaExceeded `yaml:"quota-exceeded" toml:"quota-exceeded" json:"quota-exceeded"`

	// Routing controls credential selection behavior.
	Routing RoutingConfig `yaml:"routing" toml:"routing" json:"routing"`

	// WebsocketAuth enables or disables authentication for the WebSocket API.
	WebsocketAuth bool `yaml:"ws-auth" toml:"ws-auth" json:"ws-auth"`

	// AntigravitySignatureCacheEnabled controls whether signature cache validation is enabled for thinking blocks.
	// When true (default), cached signatures are preferred and validated.
	// When false, client signatures are used directly after normalization (bypass mode).
	AntigravitySignatureCacheEnabled *bool `yaml:"antigravity-signature-cache-enabled,omitempty" toml:"antigravity-signature-cache-enabled,omitempty" json:"antigravity-signature-cache-enabled,omitempty"`

	AntigravitySignatureBypassStrict *bool `yaml:"antigravity-signature-bypass-strict,omitempty" toml:"antigravity-signature-bypass-strict,omitempty" json:"antigravity-signature-bypass-strict,omitempty"`

	// Antigravity configures provider-wide Antigravity request behavior.
	Antigravity AntigravityConfig `yaml:"antigravity" toml:"antigravity" json:"antigravity"`

	// GeminiKey defines Gemini API key configurations with optional routing overrides.
	GeminiKey []GeminiKey `yaml:"gemini-api-key" toml:"gemini-api-key" json:"gemini-api-key"`

	// InteractionsKey defines native Google Interactions API key configurations.
	InteractionsKey []GeminiKey `yaml:"interactions-api-key" toml:"interactions-api-key" json:"interactions-api-key"`

	// Codex defines a list of Codex API key configurations from the TOML configuration file.
	CodexKey []CodexKey `yaml:"codex-api-key" toml:"codex-api-key" json:"codex-api-key"`

	// XAIKey defines xAI API key configurations using the same structure as Codex API keys.
	XAIKey []XAIKey `yaml:"xai-api-key" toml:"xai-api-key" json:"xai-api-key"`

	// MetaKey defines Meta API key configurations using the same structure as Codex API keys.
	MetaKey []MetaKey `yaml:"meta-api-key" toml:"meta-api-key" json:"meta-api-key"`

	// GitLabDuoAgentKey defines GitLab personal access tokens served as GitLab
	// Duo Agent (NeoPi's GITLAB_TOKEN), in the Codex API key structure.
	GitLabDuoAgentKey []GitLabDuoAgentKey `yaml:"gitlab-duo-agent-api-key" toml:"gitlab-duo-agent-api-key" json:"gitlab-duo-agent-api-key"`

	// XAI configures provider-wide xAI request behavior.
	XAI XAIConfig `yaml:"xai" toml:"xai" json:"xai"`

	// Codex configures provider-wide Codex request behavior.
	Codex CodexConfig `yaml:"codex" toml:"codex" json:"codex"`

	// Claude configures provider-wide Claude request behavior.
	Claude ClaudeConfig `yaml:"claude" toml:"claude" json:"claude"`

	// ClaudeKey defines a list of Claude API key configurations from the TOML configuration file.
	ClaudeKey []ClaudeKey `yaml:"claude-api-key" toml:"claude-api-key" json:"claude-api-key"`

	// ClaudeHeaderDefaults configures default header values for Claude API requests.
	// These are used as fallbacks when the client does not send its own headers.
	ClaudeHeaderDefaults ClaudeHeaderDefaults `yaml:"claude-header-defaults" toml:"claude-header-defaults" json:"claude-header-defaults"`

	// DisableClaudeCloakMode globally disables Claude request cloaking when true.
	// Cloaking disguises requests as the official Claude Code CLI and replaces the
	// system prompt. When true, every Claude credential defaults to no cloaking
	// ("never"); a specific credential can still re-enable or override it via its own
	// cloak settings (the per claude-api-key "cloak" block, or a "cloak_mode" value in
	// the auth/OAuth token file). Default false preserves the per-client "auto" behavior.
	DisableClaudeCloakMode bool `yaml:"disable-claude-cloak-mode" toml:"disable-claude-cloak-mode" json:"disable-claude-cloak-mode"`

	// OpenAICompatibility defines OpenAI API compatibility configurations for external providers.
	OpenAICompatibility []OpenAICompatibility `yaml:"openai-compatibility" toml:"openai-compatibility" json:"openai-compatibility"`

	// VertexCompatAPIKey defines Vertex AI-compatible API key configurations for third-party providers.
	// Used for services that use Vertex AI-style paths but with simple API key authentication.
	VertexCompatAPIKey []VertexCompatKey `yaml:"vertex-api-key" toml:"vertex-api-key" json:"vertex-api-key"`

	// OAuthExcludedModels defines per-provider global model exclusions applied to OAuth/file-backed auth entries.
	OAuthExcludedModels map[string][]string `yaml:"oauth-excluded-models,omitempty" toml:"oauth-excluded-models,omitempty" json:"oauth-excluded-models,omitempty"`

	// OAuthModelAlias defines global model name aliases for OAuth/file-backed auth channels.
	// These aliases affect both model listing and model routing for supported channels:
	// vertex, aistudio, antigravity, claude, codex, kimi, xai, meta.
	//
	// NOTE: This does not apply to existing per-credential model alias features under:
	// gemini-api-key, interactions-api-key, codex-api-key, xai-api-key, meta-api-key, claude-api-key, openai-compatibility, and vertex-api-key.
	OAuthModelAlias map[string][]OAuthModelAlias `yaml:"oauth-model-alias,omitempty" toml:"oauth-model-alias,omitempty" json:"oauth-model-alias,omitempty"`

	// OAuthRequestScopedErrors defines per-provider request-scoped error rules applied to OAuth/file-backed auth entries.
	// Supported channels include: vertex, aistudio, antigravity, claude, codex, kimi, xai, meta, and OAuth plugin provider keys.
	//
	// NOTE: This applies only to OAuth credentials and does not affect per-credential request-scoped-errors under *-api-key.
	OAuthRequestScopedErrors map[string][]RequestScopedErrorRule `yaml:"oauth-request-scoped-errors,omitempty" toml:"oauth-request-scoped-errors,omitempty" json:"oauth-request-scoped-errors,omitempty"`

	// OAuthSettings defines per-channel model settings (such as max-context-length) applied to OAuth/file-backed auth entries.
	OAuthSettings map[string][]OAuthModelSetting `yaml:"oauth-settings,omitempty" toml:"oauth-settings,omitempty" json:"oauth-settings,omitempty"`

	// Payload defines default and override rules for provider payload parameters.
	Payload PayloadConfig `yaml:"payload" toml:"payload" json:"payload"`
}

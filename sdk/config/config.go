// Package config provides the public SDK configuration API.
//
// It re-exports the server configuration types and helpers so external projects can
// embed CLIProxyAPI without importing internal packages.
package config

import internalconfig "github.com/router-for-me/CLIProxyAPI/v8/internal/config"

type SDKConfig = internalconfig.SDKConfig

type Config = internalconfig.Config

type ConfigFormat = internalconfig.ConfigFormat

const (
	ConfigFormatTOML = internalconfig.ConfigFormatTOML
	ConfigFormatYAML = internalconfig.ConfigFormatYAML
)

func ResolveConfigPath(path string) (string, error) { return internalconfig.ResolveConfigPath(path) }
func DecodeConfig(data []byte, format ConfigFormat) (*Config, error) {
	return internalconfig.DecodeConfig(data, format)
}
func EncodeConfig(cfg *Config) ([]byte, error) { return internalconfig.EncodeConfig(cfg) }
func MarshalTOML(value any) ([]byte, error)    { return internalconfig.MarshalTOML(value) }
func ParseConfigDocument(data []byte) (map[string]any, error) {
	return internalconfig.ParseConfigDocument(data)
}
func RenderConfigDocument(root map[string]any) ([]byte, error) {
	return internalconfig.RenderConfigDocument(root)
}

type ModelCatalogs = internalconfig.ModelCatalogs

type ClientConfig = internalconfig.ClientConfig
type CodexClientConfig = internalconfig.CodexClientConfig
type CodexWebSearchResultCacheConfig = internalconfig.CodexWebSearchResultCacheConfig
type StreamingConfig = internalconfig.StreamingConfig
type ClaudeCodeConfig = internalconfig.ClaudeCodeConfig
type TLSConfig = internalconfig.TLSConfig
type DiscoveryConfig = internalconfig.DiscoveryConfig
type DiscoveryInterfacesConfig = internalconfig.DiscoveryInterfacesConfig
type RemoteManagement = internalconfig.RemoteManagement
type OAuthModelAlias = internalconfig.OAuthModelAlias
type OAuthModelSetting = internalconfig.OAuthModelSetting
type PayloadConfig = internalconfig.PayloadConfig
type PayloadRule = internalconfig.PayloadRule
type PayloadFilterRule = internalconfig.PayloadFilterRule
type PayloadModelRule = internalconfig.PayloadModelRule

type GeminiKey = internalconfig.GeminiKey
type CodexKey = internalconfig.CodexKey
type XAIKey = internalconfig.XAIKey
type XAIModel = internalconfig.XAIModel
type MetaKey = internalconfig.MetaKey
type MetaModel = internalconfig.MetaModel
type GitLabDuoAgentKey = internalconfig.GitLabDuoAgentKey
type ClaudeKey = internalconfig.ClaudeKey
type VertexCompatKey = internalconfig.VertexCompatKey
type VertexCompatModel = internalconfig.VertexCompatModel
type OpenAICompatibility = internalconfig.OpenAICompatibility
type OpenAICompatibilityAPIKey = internalconfig.OpenAICompatibilityAPIKey
type OpenAICompatibilityModel = internalconfig.OpenAICompatibilityModel

type TLS = internalconfig.TLSConfig

const (
	DefaultPanelGitHubRepository = internalconfig.DefaultPanelGitHubRepository
)

func LoadConfig(configFile string) (*Config, error) { return internalconfig.LoadConfig(configFile) }

func LoadConfigOptional(configFile string, optional bool) (*Config, error) {
	return internalconfig.LoadConfigOptional(configFile, optional)
}

func ResolveOAuthModelSetting(settings []OAuthModelSetting, modelID, metadataModelID, modelName string) *OAuthModelSetting {
	return internalconfig.ResolveOAuthModelSetting(settings, modelID, metadataModelID, modelName)
}

func ParseConfigBytes(data []byte) (*Config, error) { return internalconfig.ParseConfigBytes(data) }

func SaveConfigPreserveComments(configFile string, cfg *Config) error {
	return internalconfig.SaveConfigPreserveComments(configFile, cfg)
}

func SaveConfigPreserveCommentsUpdateNestedScalar(configFile string, path []string, value string) error {
	return internalconfig.SaveConfigPreserveCommentsUpdateNestedScalar(configFile, path, value)
}

func NormalizeCommentIndentation(data []byte) []byte {
	return internalconfig.NormalizeCommentIndentation(data)
}

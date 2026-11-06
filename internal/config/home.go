package config

// HomeConfig stores runtime-only Home control plane settings from -home-jwt.
type HomeConfig struct {
	Enabled                 bool          `yaml:"enabled" toml:"enabled" json:"enabled"`
	NodeID                  string        `yaml:"-" toml:"-" json:"-"`
	Host                    string        `yaml:"host" toml:"host" json:"-"`
	Port                    int           `yaml:"port" toml:"port" json:"-"`
	DisableClusterDiscovery bool          `yaml:"disable-cluster-discovery" toml:"disable-cluster-discovery" json:"-"`
	TLS                     HomeTLSConfig `yaml:"tls" toml:"tls" json:"-"`
}

// HomeTLSConfig configures client-side TLS for the home Redis connection.
type HomeTLSConfig struct {
	Enable              bool   `yaml:"enable" toml:"enable" json:"-"`
	ServerName          string `yaml:"server-name" toml:"server-name" json:"-"`
	InsecureSkipVerify  bool   `yaml:"insecure-skip-verify" toml:"insecure-skip-verify" json:"-"`
	CACert              string `yaml:"ca-cert" toml:"ca-cert" json:"-"`
	ClientCert          string `yaml:"-" toml:"-" json:"-"`
	ClientKey           string `yaml:"-" toml:"-" json:"-"`
	UseTargetServerName bool   `yaml:"-" toml:"-" json:"-"`
}

// NormalizeHomePort ensures that the CPA server port received from Home is valid,
// defaulting to 8317 when omitted or non-positive.
func NormalizeHomePort(port int) int {
	if port <= 0 {
		return 8317
	}
	return port
}

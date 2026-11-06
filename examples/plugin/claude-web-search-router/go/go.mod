module github.com/router-for-me/CLIProxyAPI/v7/examples/plugin/claude-web-search-router/go

go 1.26.0

require (
	github.com/router-for-me/CLIProxyAPI/v7 v7.0.0
	github.com/tidwall/gjson v1.18.0
	github.com/pelletier/go-toml/v2 v2.2.2
)

require (
	github.com/sirupsen/logrus v1.9.3 // indirect
	github.com/tidwall/match v1.1.1 // indirect
	github.com/tidwall/pretty v1.2.0 // indirect
	golang.org/x/sys v0.47.0 // indirect
)

replace github.com/router-for-me/CLIProxyAPI/v7 => ../../../..

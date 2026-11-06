package config

import (
	"fmt"
	"strings"
)

// ProjectV8ConfigAliases moves canonical fields into a requested historical subtree.
func ProjectV8ConfigAliases(root map[string]any, path string) {
	if path == "" {
		return
	}
	paths := append(append([]configPath{}, v8Aliases...), v8SharedStructPaths...)
	for _, alias := range paths {
		if path != alias.old && !strings.HasPrefix(alias.old, path+".") && !strings.HasPrefix(path, alias.old+".") {
			continue
		}
		value, ok := mapPath(root, alias.current)
		if !ok {
			continue
		}
		if m, ok := value.(map[string]any); ok && len(m) != 0 {
			continue
		}
		setMapPath(root, alias.old, cloneNative(value))
		deleteMapPath(root, alias.current)
	}
}

// NormalizeV8ConfigAliases resolves historical v8 names by field presence.
func NormalizeV8ConfigAliases(root map[string]any) error {
	for _, container := range v8SharedStructPaths {
		value, present := mapPath(root, container.old)
		if !present || value != nil {
			continue
		}
		for _, alias := range v8Aliases {
			if strings.HasPrefix(alias.old, container.old+".") {
				if _, exists := mapPath(root, alias.current); !exists {
					setMapPath(root, alias.current, nil)
				}
			}
		}
		deleteMapPath(root, container.old)
	}
	for _, alias := range v8Aliases {
		if value, ok := mapPath(root, alias.old); ok {
			if _, present := mapPath(root, alias.current); !present {
				setMapPath(root, alias.current, cloneNative(value))
			}
			deleteMapPath(root, alias.old)
		}
	}
	for _, alias := range v8SharedStructPaths {
		value, present := mapPath(root, alias.old)
		if !present {
			continue
		}
		m, isMap := value.(map[string]any)
		if value != nil && !isMap {
			return fmt.Errorf("%s must be a mapping", alias.old)
		}
		if isMap && len(m) != 0 {
			continue
		}
		if _, exists := mapPath(root, alias.current); !exists {
			setMapPath(root, alias.current, map[string]any{})
		}
		deleteMapPath(root, alias.old)
	}
	for _, prefix := range []string{"codex.live-media-relay", "oauth.providers.codex.live-media-relay"} {
		old := prefix + ".allow-private-remote-ips"
		if value, ok := mapPath(root, old); ok {
			if prefix == "codex.live-media-relay" {
				if _, present := mapPath(root, "oauth.providers.codex.live-media-relay.disable-private-remote-ips"); present {
					deleteMapPath(root, old)
					continue
				}
			}
			canonical := prefix + ".disable-private-remote-ips"
			if _, present := mapPath(root, canonical); present {
				return fmt.Errorf("%s and %s cannot both be configured", old, canonical)
			}
			allow, valid := value.(bool)
			if !valid {
				return fmt.Errorf("%s must be a boolean", old)
			}
			setMapPath(root, canonical, !allow)
			deleteMapPath(root, old)
		}
	}
	return nil
}

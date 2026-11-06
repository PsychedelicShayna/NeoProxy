package config

import (
	"fmt"
	"reflect"
	"sort"
	"strings"
	"sync"

	log "github.com/sirupsen/logrus"
)

var v8ConfigSchema = canonicalConfigSchema()
var v8MigrationWarnMu sync.RWMutex
var v8MigrationWarnFunc func(string, string)

// SetV8MigrationWarnFunc installs the optional migration warning sink.
func SetV8MigrationWarnFunc(warn func(section, msg string)) {
	v8MigrationWarnMu.Lock()
	v8MigrationWarnFunc = warn
	v8MigrationWarnMu.Unlock()
}
func warnV8Migration(section string) {
	msg := fmt.Sprintf("unrecognized configuration section %s was commented out during v8 migration", section)
	v8MigrationWarnMu.RLock()
	warn := v8MigrationWarnFunc
	v8MigrationWarnMu.RUnlock()
	if warn != nil {
		warn(section, msg)
	} else {
		log.Warn(msg)
	}
}

func canonicalConfigSchema() map[string]any {
	schema := map[string]any{"config-version": true}
	var walk func(reflect.Type, string)
	walk = func(t reflect.Type, path string) {
		if t.Kind() == reflect.Struct && t != durationType {
			if path != "" {
				mapped := path
				for _, entry := range v8StructPaths {
					if entry.old == path {
						mapped = entry.current
						break
					}
				}
				if _, exists := mapPath(schema, mapped); !exists {
					setMapPath(schema, mapped, map[string]any{})
				}
			}
			for i := 0; i < t.NumField(); i++ {
				field := t.Field(i)
				if field.PkgPath != "" {
					continue
				}
				tag := field.Tag.Get("toml")
				if tag == "" {
					tag = field.Tag.Get("yaml")
				}
				key := strings.Split(tag, ",")[0]
				if key == "-" {
					continue
				}
				if field.Anonymous {
					walk(field.Type, path)
					continue
				}
				if key == "" {
					key = strings.ToLower(field.Name)
				}
				child := key
				if path != "" {
					child = path + "." + key
				}
				walk(field.Type, child)
			}
			return
		}
		mapped := path
		for _, entry := range v8Paths {
			if entry.old == path {
				mapped = entry.current
				break
			}
		}
		setMapPath(schema, mapped, true)
	}
	walk(reflect.TypeOf(Config{}), "")
	// The canonical API-key groups have provider-specific validation after expansion.
	delete(schema, "gemini-api-key")
	delete(schema, "interactions-api-key")
	delete(schema, "vertex-api-key")
	delete(schema, "codex-api-key")
	delete(schema, "claude-api-key")
	delete(schema, "xai-api-key")
	delete(schema, "meta-api-key")
	delete(schema, "gitlab-duo-agent-api-key")
	delete(schema, "openai-compatibility")
	schema["api-keys"] = map[string]any{}
	for _, family := range v8KeyFamilies {
		setMapPath(schema, "api-keys."+family.current, true)
	}
	// These retired fields only survive as archive comments, never live settings.
	deleteMapPath(schema, "models.devin-catalog")
	return schema
}

func archiveUnknownConfig(root, schema map[string]any, path string) ([]string, error) {
	keys := make([]string, 0, len(root))
	for key := range root {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var comments []string
	for _, key := range keys {
		value := root[key]
		child := key
		if path != "" {
			child = path + "." + key
		}
		allowed, known := schema[key]
		if !known {
			encoded, err := nativeTOMLInline(value)
			if err != nil {
				return nil, err
			}
			comments = append(comments, "# "+child+" = "+encoded)
			delete(root, key)
			warnV8Migration(child)
			continue
		}
		if subSchema, ok := allowed.(map[string]any); ok {
			if sub, ok := value.(map[string]any); ok {
				nested, err := archiveUnknownConfig(sub, subSchema, child)
				if err != nil {
					return nil, err
				}
				comments = append(comments, nested...)
			}
		}
	}
	return comments, nil
}
func validateConfigSchema(root, schema map[string]any, path string) error {
	for key, value := range root {
		child := key
		if path != "" {
			child = path + "." + key
		}
		allowed, known := schema[key]
		if !known {
			return fmt.Errorf("unknown v8 configuration field %s", child)
		}
		if subSchema, ok := allowed.(map[string]any); ok {
			if sub, ok := value.(map[string]any); ok {
				if err := validateConfigSchema(sub, subSchema, child); err != nil {
					return err
				}
			} else if value != nil {
				return fmt.Errorf("%s must be a mapping", child)
			}
		}
	}
	return nil
}

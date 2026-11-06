package config

import (
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"time"
)

// DecodeConfig decodes native TOML or an explicitly requested legacy YAML import.
// It applies load defaults but leaves runtime sanitization to ParseConfigBytes/LoadConfig.
func DecodeConfig(data []byte, format ConfigFormat) (*Config, error) {
	var root map[string]any
	var err error
	if format == ConfigFormatYAML {
		root, err = decodeLegacyYAML(data)
	} else {
		root, err = ParseConfigDocument(data)
	}
	if err != nil {
		return nil, err
	}
	return decodeConfigMap(root)
}

func decodeConfigMap(root map[string]any) (*Config, error) {
	flat, err := flattenV8Map(root)
	if err != nil {
		return nil, err
	}
	var cfg Config
	applyLoadDefaults(&cfg)
	if err = validateCredentialWeightMap(flat); err != nil {
		return nil, err
	}
	if err = decodeNativeValue(reflect.ValueOf(&cfg).Elem(), flat, "", false); err != nil {
		return nil, err
	}
	if err = cfg.Models.Validate(); err != nil {
		return nil, err
	}
	for _, path := range v8Paths {
		if strings.HasPrefix(path.current, "oauth.providers.") {
			if _, ok := mapPath(root, path.current); ok {
				if cfg.OAuthOnlyFields == nil {
					cfg.OAuthOnlyFields = map[string]bool{}
				}
				cfg.OAuthOnlyFields[path.old] = true
			}
		}
	}
	return &cfg, nil
}

// EncodeConfig preserves OAuth-only field scope when creating runtime snapshots.
func EncodeConfig(cfg *Config) ([]byte, error) {
	if cfg == nil {
		return nil, fmt.Errorf("nil config")
	}
	value, err := nativeConfigValue(reflect.ValueOf(cfg))
	if err != nil {
		return nil, err
	}
	root := value.(map[string]any)
	for _, path := range v8Paths {
		if cfg.OAuthOnlyFields[path.old] {
			if value, ok := mapPath(root, path.old); ok {
				deleteMapPath(root, path.old)
				setMapPath(root, path.current, value)
			}
		}
	}
	return RenderConfigDocument(root)
}

// PluginInstanceConfigFromMap preserves the entire plugin-owned document.
func PluginInstanceConfigFromMap(raw map[string]any) (PluginInstanceConfig, error) {
	enabled := false
	cfg := PluginInstanceConfig{Enabled: &enabled, Raw: cloneMap(raw)}
	if value, ok := raw["enabled"]; ok && value != nil {
		v, ok := value.(bool)
		if !ok {
			return cfg, fmt.Errorf("parse plugin enabled: must be a boolean")
		}
		cfg.Enabled = &v
	}
	if value, ok := raw["priority"]; ok && value != nil {
		if err := decodeNativeValue(reflect.ValueOf(&cfg.Priority).Elem(), value, "plugin.priority", false); err != nil {
			return cfg, err
		}
	}
	return cfg, nil
}

var durationType = reflect.TypeOf(time.Duration(0))
var pluginInstanceType = reflect.TypeOf(PluginInstanceConfig{})
var imageGenerationModeType = reflect.TypeOf(DisableImageGenerationMode(0))
var concurrencyType = reflect.TypeOf(CredentialConcurrencyConfig{})

func decodeNativeValue(dst reflect.Value, source any, path string, strict bool) error {
	fail := func(want string) error { return fmt.Errorf("%s must be %s", path, want) }
	if source == nil {
		switch dst.Kind() {
		case reflect.Map, reflect.Slice, reflect.Pointer, reflect.Interface:
			dst.SetZero()
		}
		return nil
	}
	if dst.Kind() == reflect.Pointer {
		if dst.IsNil() {
			dst.Set(reflect.New(dst.Type().Elem()))
		}
		return decodeNativeValue(dst.Elem(), source, path, strict)
	}
	if dst.Type() == durationType {
		if text, ok := source.(string); ok {
			value, err := time.ParseDuration(text)
			if err != nil {
				return fmt.Errorf("%s must be a duration: %w", path, err)
			}
			dst.SetInt(int64(value))
			return nil
		}
		return fail("a duration string")
	}
	if dst.Type() == imageGenerationModeType {
		var mode DisableImageGenerationMode
		switch value := source.(type) {
		case bool:
			if value {
				mode = DisableImageGenerationAll
			}
		case string:
			var err error
			mode, err = parseDisableImageGenerationString(value)
			if err != nil {
				return err
			}
		default:
			return fail("a boolean, chat, or passthrough")
		}
		dst.SetInt(int64(mode))
		return nil
	}
	if dst.Type() == reflect.TypeOf(CodexWebSearchResultCacheConfig{}) {
		strict = true
	}
	if dst.Type() == pluginInstanceType {
		raw, ok := source.(map[string]any)
		if !ok {
			return fail("a mapping")
		}
		cfg, err := PluginInstanceConfigFromMap(raw)
		if err != nil {
			return err
		}
		dst.Set(reflect.ValueOf(cfg))
		return nil
	}
	switch dst.Kind() {
	case reflect.Interface:
		dst.Set(reflect.ValueOf(cloneNative(source)))
		return nil
	case reflect.Struct:
		root, ok := source.(map[string]any)
		if !ok {
			return fail("a mapping")
		}
		known := map[string]bool{}
		for i := 0; i < dst.NumField(); i++ {
			field := dst.Type().Field(i)
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
				embedded := map[string]any{}
				for j := 0; j < field.Type.NumField(); j++ {
					member := field.Type.Field(j)
					name := strings.Split(member.Tag.Get("toml"), ",")[0]
					if name == "" {
						name = strings.Split(member.Tag.Get("yaml"), ",")[0]
					}
					if member.PkgPath != "" || name == "-" {
						continue
					}
					known[name] = true
					if value, present := root[name]; present {
						embedded[name] = value
					}
				}
				if err := decodeNativeValue(dst.Field(i), embedded, path, strict); err != nil {
					return err
				}
				continue
			}
			if key == "" {
				key = strings.ToLower(field.Name)
			}
			known[key] = true
			if value, present := root[key]; present {
				child := key
				if path != "" {
					child = path + "." + key
				}
				if err := decodeNativeValue(dst.Field(i), value, child, strict); err != nil {
					return err
				}
			}
		}
		if dst.Type() == concurrencyType {
			c := dst.Addr().Interface().(*CredentialConcurrencyConfig)
			_, c.lifecycleConfigRevisionPresent = root["lifecycle-config-revision"]
			_, c.observationBarrierRevisionPresent = root["observation-barrier-revision"]
			_, c.cpaHeartbeatTimeoutPresent = root["cpa-heartbeat-timeout"]
			_, c.cpaCancelBoundPresent = root["cpa-cancel-bound"]
			_, c.reclaimGracePresent = root["reclaim-grace"]
			_, c.cleanupIntervalPresent = root["cleanup-interval"]
			_, c.releaseFlushIntervalPresent = root["release-flush-interval"]
			_, c.releaseMaxBackoffPresent = root["release-max-backoff"]
			_, c.busyRetryMinPresent = root["busy-retry-min"]
			_, c.busyRetryMaxPresent = root["busy-retry-max"]
			_, c.maxLimitPresent = root["max-limit"]
		}
		if strict {
			for key := range root {
				if !known[key] {
					return fmt.Errorf("unknown configuration field %s%s", path+".", key)
				}
			}
		}
		if dst.Type() == reflect.TypeOf(CodexWebSearchResultCacheConfig{}) {
			return dst.Interface().(CodexWebSearchResultCacheConfig).Validate()
		}
		return nil
	case reflect.Map:
		root, ok := source.(map[string]any)
		if !ok {
			return fail("a mapping")
		}
		out := reflect.MakeMapWithSize(dst.Type(), len(root))
		for key, value := range root {
			entry := reflect.New(dst.Type().Elem()).Elem()
			if err := decodeNativeValue(entry, value, path+"."+key, strict); err != nil {
				return err
			}
			out.SetMapIndex(reflect.ValueOf(key).Convert(dst.Type().Key()), entry)
		}
		dst.Set(out)
		return nil
	case reflect.Slice:
		values, ok := source.([]any)
		if !ok {
			return fail("a list")
		}
		out := reflect.MakeSlice(dst.Type(), len(values), len(values))
		for i, value := range values {
			if err := decodeNativeValue(out.Index(i), value, fmt.Sprintf("%s[%d]", path, i), strict); err != nil {
				return err
			}
		}
		dst.Set(out)
		return nil
	case reflect.String:
		value, ok := source.(string)
		if !ok {
			if strings.HasSuffix(path, "web-search-result-cache.ttl") {
				switch n := source.(type) {
				case int64:
					value = strconv.FormatInt(n, 10)
				case int:
					value = strconv.Itoa(n)
				default:
					return fail("a duration string or integer seconds")
				}
			} else {
				return fail("a string")
			}
		}
		dst.SetString(value)
		return nil
	case reflect.Bool:
		value, ok := source.(bool)
		if !ok {
			return fail("a boolean")
		}
		dst.SetBool(value)
		return nil
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		var value int64
		switch n := source.(type) {
		case int64:
			value = n
		case int:
			value = int64(n)
		case uint64:
			if n > uint64(^uint64(0)>>1) {
				return fail("an integer within range")
			}
			value = int64(n)
		default:
			return fail("an integer")
		}
		if dst.OverflowInt(value) {
			return fail("an integer within range")
		}
		dst.SetInt(value)
		return nil
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		var value uint64
		switch n := source.(type) {
		case int64:
			if n < 0 {
				return fail("a non-negative integer")
			}
			value = uint64(n)
		case int:
			if n < 0 {
				return fail("a non-negative integer")
			}
			value = uint64(n)
		case uint64:
			value = n
		default:
			return fail("an integer")
		}
		if dst.OverflowUint(value) {
			return fail("an integer within range")
		}
		dst.SetUint(value)
		return nil
	case reflect.Float32, reflect.Float64:
		var value float64
		switch n := source.(type) {
		case float64:
			value = n
		case int64:
			value = float64(n)
		case int:
			value = float64(n)
		default:
			return fail("a number")
		}
		if dst.OverflowFloat(value) {
			return fail("a number within range")
		}
		dst.SetFloat(value)
		return nil
	default:
		return fmt.Errorf("unsupported config type %s at %s", dst.Type(), path)
	}
}

func nativeConcurrencyValue(c CredentialConcurrencyConfig) map[string]any {
	out := map[string]any{}
	integer := func(key string, value int64, present bool) {
		if present || value != 0 {
			out[key] = value
		}
	}
	duration := func(key string, value time.Duration, present bool) {
		if present || value != 0 {
			out[key] = value.String()
		}
	}
	integer("lifecycle-config-revision", c.LifecycleConfigRevision, c.lifecycleConfigRevisionPresent)
	integer("observation-barrier-revision", c.ObservationBarrierRevision, c.observationBarrierRevisionPresent)
	duration("cpa-heartbeat-timeout", c.CPAHeartbeatTimeout, c.cpaHeartbeatTimeoutPresent)
	duration("cpa-cancel-bound", c.CPACancelBound, c.cpaCancelBoundPresent)
	duration("reclaim-grace", c.ReclaimGrace, c.reclaimGracePresent)
	duration("cleanup-interval", c.CleanupInterval, c.cleanupIntervalPresent)
	duration("release-flush-interval", c.ReleaseFlushInterval, c.releaseFlushIntervalPresent)
	duration("release-max-backoff", c.ReleaseMaxBackoff, c.releaseMaxBackoffPresent)
	duration("busy-retry-min", c.BusyRetryMin, c.busyRetryMinPresent)
	duration("busy-retry-max", c.BusyRetryMax, c.busyRetryMaxPresent)
	integer("max-limit", c.MaxLimit, c.maxLimitPresent)
	return out
}

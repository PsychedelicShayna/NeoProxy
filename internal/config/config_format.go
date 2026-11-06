package config

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	log "github.com/sirupsen/logrus"
)

// ConfigFormat identifies native TOML and the explicitly supported legacy importer.
type ConfigFormat string

const (
	ConfigFormatYAML ConfigFormat = "yaml"
	ConfigFormatTOML ConfigFormat = "toml"
)

func ParseConfigFormat(name string) (ConfigFormat, error) {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "toml":
		return ConfigFormatTOML, nil
	case "yaml", "yml":
		return ConfigFormatYAML, nil
	default:
		return "", fmt.Errorf("unknown config format %q (want toml)", name)
	}
}
func (f ConfigFormat) Extension() string {
	if f == ConfigFormatYAML {
		return ".yaml"
	}
	return ".toml"
}
func (f ConfigFormat) ContentType() string {
	if f == ConfigFormatYAML {
		return "application/yaml; charset=utf-8"
	}
	return "application/toml; charset=utf-8"
}
func configFormatFromExtension(path string) (ConfigFormat, bool) {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".toml":
		return ConfigFormatTOML, true
	case ".yaml", ".yml":
		return ConfigFormatYAML, true
	default:
		return "", false
	}
}
func DetectConfigFormat(path string, data []byte) ConfigFormat {
	if format, ok := configFormatFromExtension(path); ok {
		return format
	}
	return sniffConfigFormat(data)
}
func sniffConfigFormat(data []byte) ConfigFormat {
	for _, line := range bytes.Split(data, []byte("\n")) {
		trimmed := strings.TrimSpace(string(line))
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		if strings.HasPrefix(trimmed, "[") {
			return ConfigFormatTOML
		}
		equals, colon := strings.Index(trimmed, "="), strings.Index(trimmed, ":")
		if equals > 0 && (colon < 0 || equals < colon) {
			return ConfigFormatTOML
		}
		return ConfigFormatYAML
	}
	return ConfigFormatTOML
}
func ConfigFileFormat(path string) ConfigFormat {
	if format, ok := configFormatFromExtension(path); ok {
		return format
	}
	data, _ := os.ReadFile(path)
	return sniffConfigFormat(data)
}

// DefaultConfigFile always selects the canonical native file. ResolveConfigPath
// performs the one-time import when only config.yaml exists.
func DefaultConfigFile(dir string) string { return filepath.Join(dir, "config.toml") }

// ResolveConfigPath resolves legacy paths to their canonical TOML sibling. If a
// sibling already exists, TOML wins. Otherwise YAML is imported atomically with
// the source's permissions, never modified or deleted. Missing native paths are
// returned unchanged so optional/cloud configuration can still be created later.
func ResolveConfigPath(path string) (string, error) {
	if path == "" {
		path = DefaultConfigFile("")
	}
	format, _ := configFormatFromExtension(path)
	canonical := path
	var candidates []string
	switch format {
	case ConfigFormatYAML:
		canonical = strings.TrimSuffix(path, filepath.Ext(path)) + ".toml"
		candidates = []string{path}
	case ConfigFormatTOML:
		stem := strings.TrimSuffix(path, filepath.Ext(path))
		candidates = []string{stem + ".yaml", stem + ".yml"}
	default:
		data, err := os.ReadFile(path)
		if errors.Is(err, os.ErrNotExist) {
			return path, nil
		}
		if err != nil {
			return "", err
		}
		if sniffConfigFormat(data) != ConfigFormatYAML {
			return path, nil
		}
		canonical = path + ".toml"
		candidates = []string{path}
	}
	if _, err := os.Stat(canonical); err == nil {
		for _, legacy := range candidates {
			if _, legacyErr := os.Stat(legacy); legacyErr == nil {
				log.WithFields(log.Fields{"config": canonical, "legacy-config": legacy}).Warn("Both TOML and legacy YAML configuration exist; using TOML and leaving YAML untouched")
			}
		}
		return canonical, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	for _, legacy := range candidates {
		info, err := os.Stat(legacy)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return "", err
		}
		data, err := os.ReadFile(legacy)
		if err != nil {
			return "", err
		}
		converted, err := YAMLToTOML(data)
		if err != nil {
			return "", fmt.Errorf("import legacy config: %w", err)
		}
		if err = atomicConfigWrite(canonical, converted, info.Mode().Perm(), true); err != nil {
			if !errors.Is(err, os.ErrExist) {
				return "", fmt.Errorf("write imported TOML config: %w", err)
			}
			log.WithFields(log.Fields{"config": canonical, "legacy-config": legacy}).Warn("TOML configuration appeared during legacy import; using TOML and leaving YAML untouched")
		} else {
			log.WithFields(log.Fields{"config": canonical, "legacy-config": legacy}).Warn("Imported legacy YAML configuration into TOML; YAML was left untouched")
		}
		return canonical, nil
	}
	return canonical, nil
}

// ReadConfigFile returns native TOML bytes and exact source bytes for hashes.
func ReadConfigFile(path string) (data, raw []byte, format ConfigFormat, err error) {
	path, err = ResolveConfigPath(path)
	if err != nil {
		return nil, nil, "", err
	}
	raw, err = os.ReadFile(path)
	if err != nil {
		return nil, nil, "", err
	}
	if _, err = ParseConfigDocument(raw); err != nil {
		return nil, raw, ConfigFormatTOML, err
	}
	return raw, raw, ConfigFormatTOML, nil
}

// EncodeConfigFile validates an already-native TOML document.
func EncodeConfigFile(path string, data []byte) ([]byte, ConfigFormat, error) {
	_, err := ParseConfigDocument(data)
	return data, ConfigFormatTOML, err
}

// WriteConfigFile atomically replaces a native TOML file and preserves existing
// permissions. A legacy path is resolved first and can never overwrite YAML.
func WriteConfigFile(path string, data []byte, perm os.FileMode) error {
	if _, err := ParseConfigDocument(data); err != nil {
		return err
	}
	resolved, err := ResolveConfigPath(path)
	if err != nil {
		return err
	}
	if info, errStat := os.Stat(resolved); errStat == nil {
		perm = info.Mode().Perm()
		if perm&0222 == 0 {
			return &os.PathError{Op: "write", Path: resolved, Err: os.ErrPermission}
		}
	} else if !errors.Is(errStat, os.ErrNotExist) {
		return errStat
	}
	return atomicConfigWrite(resolved, data, perm, false)
}

func atomicConfigWrite(path string, data []byte, perm os.FileMode, exclusive bool) error {
	file, err := os.CreateTemp(filepath.Dir(path), ".config-*.toml")
	if err != nil {
		return err
	}
	name := file.Name()
	defer os.Remove(name)
	if err = file.Chmod(perm); err == nil {
		_, err = file.Write(data)
	}
	if err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if exclusive {
		return os.Link(name, path)
	}
	return os.Rename(name, path)
}

// ConvertConfigFile accepts legacy YAML only as an import source. TOML is the
// only supported target; native bytes pass through without reserialization.
func ConvertConfigFile(data []byte, from, to ConfigFormat, source string) ([]byte, error) {
	if to != ConfigFormatTOML {
		return nil, fmt.Errorf("configuration output must be TOML")
	}
	if from == ConfigFormatYAML {
		converted, err := YAMLToTOML(data)
		if err != nil {
			return nil, err
		}
		if source != "" {
			header := "# Converted from " + strings.NewReplacer("\n", " ", "\r", " ").Replace(source) + " by `neoproxy config convert --to toml`.\n"
			converted = append([]byte(header), converted...)
		}
		return converted, nil
	}
	if _, err := ParseConfigDocument(data); err != nil {
		return nil, err
	}
	return data, nil
}

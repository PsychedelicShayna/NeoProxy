package config

import (
	"os"
	"path/filepath"
)

func CopyConfigTemplate(src, dst string) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	if DetectConfigFormat(src, data) == ConfigFormatYAML {
		data, err = YAMLToTOML(data)
		if err != nil {
			return err
		}
	}

	if err = os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return err
	}

	return WriteConfigFile(dst, data, 0o600)
}

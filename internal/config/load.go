package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const maxConfigFileBytes int64 = 1 << 20

func LoadFile(path string) (Config, error) {
	if path == "" {
		return Config{}, fmt.Errorf("config file path is required")
	}
	st, err := os.Stat(path)
	if err != nil {
		return Config{}, err
	}
	if !st.Mode().IsRegular() {
		return Config{}, fmt.Errorf("config path is not a regular file")
	}
	if st.Size() > maxConfigFileBytes {
		return Config{}, fmt.Errorf("config exceeds 1 MiB limit")
	}
	f, err := os.Open(path)
	if err != nil {
		return Config{}, err
	}
	defer f.Close()

	switch strings.ToLower(filepath.Ext(path)) {
	case ".yaml", ".yml":
		return DecodeYAML(f)
	case ".json":
		return DecodeJSON(f)
	default:
		return Config{}, fmt.Errorf("unsupported config extension: %s", filepath.Ext(path))
	}
}

// Package config implements the deliberately small v0.1 configuration.
package config

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/mgertz/factory/internal/paths"
	"go.yaml.in/yaml/v3"
)

const Template = `# Factory v0.1 configuration (YAML).
# Logs are written to $XDG_STATE_HOME/factory/factory.log,
# or ~/.local/state/factory/factory.log when XDG_STATE_HOME is unset.
logging:
  # Minimum log level: debug, info, warn, or error.
  level: info
`

type Config struct {
	Logging struct {
		Level string `yaml:"level"`
	} `yaml:"logging"`
}

func Init(path string) error {
	if err := paths.EnsureDir(filepath.Dir(path)); err != nil {
		return err
	}
	f, err := paths.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL)
	if errors.Is(err, os.ErrExist) {
		return fmt.Errorf("configuration already exists: %s (not overwritten)", path)
	}
	if err != nil {
		return err
	}
	_, writeErr := io.WriteString(f, Template)
	closeErr := f.Close()
	return errors.Join(writeErr, closeErr)
}

func Load(path string) (Config, error) {
	var cfg Config
	f, err := paths.OpenFile(path, os.O_RDONLY)
	if errors.Is(err, os.ErrNotExist) {
		return cfg, fmt.Errorf("configuration file not found: %s\nCreate one using: factory config init", path)
	}
	if err != nil {
		return cfg, err
	}
	defer f.Close()
	decoder := yaml.NewDecoder(f)
	decoder.KnownFields(true)
	if err := decoder.Decode(&cfg); err != nil {
		return cfg, fmt.Errorf("invalid configuration %s: %w", path, err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return cfg, fmt.Errorf("invalid configuration %s: expected exactly one YAML document", path)
	}
	if _, err := cfg.Level(); err != nil {
		return cfg, fmt.Errorf("invalid configuration %s: %w", path, err)
	}
	return cfg, nil
}

func (c Config) Level() (slog.Level, error) {
	switch c.Logging.Level {
	case "debug":
		return slog.LevelDebug, nil
	case "info":
		return slog.LevelInfo, nil
	case "warn":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	default:
		return 0, fmt.Errorf("logging.level must be debug, info, warn, or error (got %q)", c.Logging.Level)
	}
}

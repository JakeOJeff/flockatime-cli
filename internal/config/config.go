// Package config loads and validates ~/.snapshot-agent.toml.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
)

// DefaultInterval is used when interval_seconds is absent or zero.
const DefaultInterval = 300

// Config is the on-disk agent configuration.
type Config struct {
	Endpoint        string    `toml:"endpoint"`
	APIKey          string    `toml:"api_key"`
	IntervalSeconds int       `toml:"interval_seconds"`
	Projects        []Project `toml:"project"`
}

// Project is one watched directory.
type Project struct {
	Name string `toml:"name"`
	Path string `toml:"path"`
}

// DefaultPath returns ~/.snapshot-agent.toml.
func DefaultPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("locating home directory: %w", err)
	}
	return filepath.Join(home, ".snapshot-agent.toml"), nil
}

// Load reads, validates, and normalizes the config at path. It refuses to
// proceed if the file is readable by anyone but its owner.
func Load(path string) (*Config, error) {
	if err := checkPerms(path); err != nil {
		return nil, err
	}
	// TODO: decode TOML into Config, then Validate.
	return nil, fmt.Errorf("config.Load: not implemented yet")
}

// Validate reports whether the config is usable, and fills in defaults.
func (c *Config) Validate() error {
	// TODO: require endpoint + api_key, require >=1 project, resolve and
	// stat each project path, reject duplicate project names, apply
	// DefaultInterval.
	return fmt.Errorf("config.Validate: not implemented yet")
}

// checkPerms refuses a config file that group or other can read.
func checkPerms(path string) error {
	fi, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("reading config: %w", err)
	}
	if runtime.GOOS == "windows" {
		// Windows fakes the Unix mode bits; ACLs are the real control.
		return nil
	}
	if mode := fi.Mode().Perm(); mode&0o077 != 0 {
		return fmt.Errorf("config %s has mode %04o; must be 0600", path, mode)
	}
	return nil
}

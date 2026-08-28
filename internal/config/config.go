// Package config loads and validates ~/.snapshot-agent.toml.
package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
)

// DefaultInterval is used when interval_seconds is absent or zero.
const DefaultInterval = 300

// ErrNoConfig reports a config file that simply is not there yet, so callers
// can print the how-to-fix instead of a raw syscall error.
var ErrNoConfig = errors.New("no config file")

// Config is the on-disk agent configuration.
type Config struct {
	Endpoint        string    `toml:"endpoint"`
	APIKey          string    `toml:"api_key"`
	IntervalSeconds int       `toml:"interval_seconds"`
	QueuePath       string    `toml:"queue_path"`
	Activity        Activity  `toml:"activity"`
	Projects        []Project `toml:"project"`
}

// Activity controls when the daemon captures at all. With the default source
// it captures every interval forever; with "wakatime" it goes dormant once the
// editor has been quiet for IdleMultiplier intervals.
type Activity struct {
	Source         string `toml:"source"`
	IdleMultiplier int    `toml:"idle_multiplier"`
	PollSeconds    int    `toml:"poll_seconds"`
	WakaTimeDir    string `toml:"wakatime_dir"`
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
// proceed if the file is readable by anyone but its owner. requireEndpoint is
// false for commands that never transmit, such as `once`.
func Load(path string, requireEndpoint bool) (*Config, error) {
	if err := checkPerms(path); err != nil {
		return nil, err
	}
	var c Config
	if _, err := toml.DecodeFile(path, &c); err != nil {
		return nil, fmt.Errorf("parsing %s: %w", path, err)
	}
	if err := c.Validate(requireEndpoint); err != nil {
		return nil, err
	}
	return &c, nil
}

// Validate fills in defaults and reports whether the config is usable.
func (c *Config) Validate(requireEndpoint bool) error {
	if c.IntervalSeconds <= 0 {
		c.IntervalSeconds = DefaultInterval
	}
	if c.QueuePath == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return fmt.Errorf("locating home directory: %w", err)
		}
		c.QueuePath = filepath.Join(home, ".snapshot-agent-queue.db")
	} else {
		qp, err := ExpandPath(c.QueuePath)
		if err != nil {
			return fmt.Errorf("queue_path: %w", err)
		}
		c.QueuePath = qp
	}
	if err := c.Activity.applyDefaults(c.IntervalSeconds); err != nil {
		return err
	}
	if requireEndpoint {
		if strings.TrimSpace(c.Endpoint) == "" {
			return fmt.Errorf("endpoint is required")
		}
		if strings.TrimSpace(c.APIKey) == "" {
			return fmt.Errorf("api_key is required")
		}
	}
	c.Endpoint = strings.TrimRight(c.Endpoint, "/")
	if len(c.Projects) == 0 {
		return fmt.Errorf("no [[project]] entries; nothing to snapshot")
	}
	seen := make(map[string]bool, len(c.Projects))
	for i := range c.Projects {
		p := &c.Projects[i]
		if p.Name == "" {
			return fmt.Errorf("project %d: name is required", i+1)
		}
		if seen[p.Name] {
			return fmt.Errorf("duplicate project name %q", p.Name)
		}
		seen[p.Name] = true

		abs, err := ExpandPath(p.Path)
		if err != nil {
			return fmt.Errorf("project %q: %w", p.Name, err)
		}
		fi, err := os.Stat(abs)
		if err != nil {
			return fmt.Errorf("project %q: %w", p.Name, err)
		}
		if !fi.IsDir() {
			return fmt.Errorf("project %q: not a directory", p.Name)
		}
		p.Path = abs
	}
	return nil
}

// applyDefaults fills in the [activity] section. The default source is
// "always", so a config written before this section existed keeps its old
// behaviour of capturing on every tick.
func (a *Activity) applyDefaults(intervalSeconds int) error {
	if a.Source == "" {
		a.Source = "always"
	}
	if a.IdleMultiplier <= 0 {
		a.IdleMultiplier = 2
	}
	if a.PollSeconds <= 0 {
		a.PollSeconds = 30
	}
	// Polling slower than the capture interval would delay a wake past the
	// first snapshot it is meant to trigger.
	if a.PollSeconds > intervalSeconds {
		a.PollSeconds = intervalSeconds
	}
	if a.WakaTimeDir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return fmt.Errorf("locating home directory: %w", err)
		}
		a.WakaTimeDir = filepath.Join(home, ".wakatime")
		return nil
	}
	dir, err := ExpandPath(a.WakaTimeDir)
	if err != nil {
		return fmt.Errorf("wakatime_dir: %w", err)
	}
	a.WakaTimeDir = dir
	return nil
}

// IdleAfter is how long the editor may stay quiet before the daemon goes
// dormant: IdleMultiplier whole capture intervals.
func (c *Config) IdleAfter() time.Duration {
	return time.Duration(c.Activity.IdleMultiplier*c.IntervalSeconds) * time.Second
}

// ExpandPath resolves a leading ~ and returns an absolute path.
func ExpandPath(p string) (string, error) {
	if p == "" {
		return "", fmt.Errorf("path is required")
	}
	if p == "~" || strings.HasPrefix(p, "~/") || strings.HasPrefix(p, `~\`) {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		p = filepath.Join(home, strings.TrimPrefix(strings.TrimPrefix(p[1:], "/"), `\`))
	}
	return filepath.Abs(p)
}

// checkPerms refuses a config file that group or other can read.
func checkPerms(path string) error {
	fi, err := os.Stat(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("%w at %s", ErrNoConfig, path)
		}
		return fmt.Errorf("reading config %s: %w", path, err)
	}
	if runtime.GOOS == "windows" {
		// Windows fakes the Unix mode bits; ACLs are the real control.
		return nil
	}
	if mode := fi.Mode().Perm(); mode&0o077 != 0 {
		return fmt.Errorf("config %s has mode %04o; must be 0600 (chmod 600 %s)", path, mode, path)
	}
	return nil
}

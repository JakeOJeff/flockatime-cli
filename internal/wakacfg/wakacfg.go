// Package wakacfg turns on debug logging in ~/.wakatime.cfg, which is what
// lets the agent see which file you are editing. It edits one key and leaves
// every other line --- api_key, api_url, comments --- byte for byte alone.
package wakacfg

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// ErrNotInstalled means there is no WakaTime config, so no editor plugin is
// sending heartbeats (Hackatime included) and there is nothing to follow yet.
var ErrNotInstalled = errors.New("no WakaTime config")

// Path is ~/.wakatime.cfg, or $WAKATIME_HOME/.wakatime.cfg, which is where
// wakatime-cli itself looks.
func Path() (string, error) {
	if h := os.Getenv("WAKATIME_HOME"); h != "" {
		return filepath.Join(h, ".wakatime.cfg"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".wakatime.cfg"), nil
}

// EnableDebug sets `debug = true` under [settings] in the file at path.
// It reports whether the file changed.
func EnableDebug(path string) (bool, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return false, fmt.Errorf("%w at %s", ErrNotInstalled, path)
	}
	if err != nil {
		return false, err
	}
	out, changed := WithDebug(string(b))
	if !changed {
		return false, nil
	}
	fi, err := os.Stat(path)
	if err != nil {
		return false, err
	}
	return true, os.WriteFile(path, []byte(out), fi.Mode().Perm())
}

var (
	sectionRe = regexp.MustCompile(`^\s*\[([^\]]+)\]\s*$`)
	debugRe   = regexp.MustCompile(`(?i)^\s*debug\s*[=:]`)
)

// WithDebug returns cfg with debug = true in its [settings] section: an
// existing debug line is rewritten in place, a missing one is added under the
// section header, and a missing section is appended. Line endings follow the
// file's own.
func WithDebug(cfg string) (string, bool) {
	nl := "\n"
	if strings.Contains(cfg, "\r\n") {
		nl = "\r\n"
	}
	lines := strings.Split(strings.ReplaceAll(cfg, "\r\n", "\n"), "\n")

	settings := -1 // index of the [settings] header
	for i, line := range lines {
		if m := sectionRe.FindStringSubmatch(line); m != nil {
			if settings >= 0 {
				break // left [settings] without finding a debug key
			}
			if strings.EqualFold(strings.TrimSpace(m[1]), "settings") {
				settings = i
			}
			continue
		}
		if settings >= 0 && debugRe.MatchString(line) {
			value := strings.TrimSpace(line[strings.IndexAny(line, "=:")+1:])
			if strings.EqualFold(value, "true") {
				return cfg, false
			}
			lines[i] = "debug = true"
			return strings.Join(lines, nl), true
		}
	}

	if settings >= 0 {
		lines = append(lines[:settings+1], append([]string{"debug = true"}, lines[settings+1:]...)...)
		return strings.Join(lines, nl), true
	}

	out := strings.TrimRight(strings.Join(lines, nl), nl)
	if out != "" {
		out += nl + nl
	}
	return out + "[settings]" + nl + "debug = true" + nl, true
}

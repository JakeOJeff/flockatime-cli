// Package activity reports when the user was last seen working, so the daemon
// can go dormant instead of snapshotting an untouched tree all night.
package activity

import (
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// Source reports the moment of the most recent editor activity.
type Source interface {
	// LastActivity returns when the user was last seen working, or the zero
	// time if the source cannot tell.
	LastActivity() time.Time
	// Describe names the source for doctor and startup logging.
	Describe() string
}

// Always reports continuous activity, so the daemon never goes dormant. It is
// the default: a config with no [activity] section behaves as it always did.
type Always struct{}

func (Always) LastActivity() time.Time { return time.Now() }
func (Always) Describe() string        { return "always active" }

// WakaTime infers activity from the files wakatime-cli rewrites on every
// heartbeat flush. It only ever stats them --- nothing here can delay, alter,
// or drop a heartbeat, so WakaTime and Hackatime data are untouched. WakaTime
// exposes no hook to push at us, which is why this polls.
type WakaTime struct{ Dir string }

// beatFiles are rewritten on each heartbeat cycle. The offline queue is read
// too: when the server is unreachable wakatime-cli banks heartbeats locally
// instead of sending them, and that must not look like the user went idle.
var beatFiles = []string{"wakatime-internal.cfg", "offline_heartbeats.bdb"}

// LastActivity is the newest mtime among the heartbeat files. It reads mtime
// rather than the heartbeats_last_sent_at field inside the cfg: that file is
// undocumented internal state whose field names may change between releases,
// while its modification time cannot.
func (w WakaTime) LastActivity() time.Time {
	var newest time.Time
	for _, name := range beatFiles {
		fi, err := os.Stat(filepath.Join(w.Dir, name))
		if err != nil {
			continue // not every install has both files
		}
		if m := fi.ModTime(); m.After(newest) {
			newest = m
		}
	}
	return newest
}

func (w WakaTime) Describe() string { return "wakatime heartbeats in " + w.Dir }

// Active reports whether the last activity is recent enough to keep
// capturing. A source that cannot tell counts as idle, so an agent pointed at
// a missing WakaTime install stays quiet rather than snapshotting forever.
func Active(src Source, idleAfter time.Duration, now time.Time) bool {
	last := src.LastActivity()
	return !last.IsZero() && now.Sub(last) <= idleAfter
}

// New returns the source named in the config.
func New(name, wakatimeDir string) (Source, error) {
	switch name {
	case "", "always":
		return Always{}, nil
	case "wakatime":
		if _, err := os.Stat(wakatimeDir); err != nil {
			return nil, fmt.Errorf("activity source %q: %w", name, err)
		}
		return WakaTime{Dir: wakatimeDir}, nil
	default:
		return nil, fmt.Errorf("unknown activity source %q (want \"wakatime\" or \"always\")", name)
	}
}

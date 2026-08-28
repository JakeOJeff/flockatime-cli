package activity

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Project is a repository WakaTime saw you editing, resolved to its root.
type Project struct {
	Name string
	Path string
}

// tailBytes is how much of the log to read. Heartbeats are appended, so the
// recent ones are at the end; reading the whole file would grow unbounded with
// it. 256 KB covers hours of editing at any plausible heartbeat rate.
const tailBytes = 256 << 10

// maxWalkUp bounds the search for a repository root, so a file that sits
// outside any checkout cannot walk the agent up to the volume root and back.
const maxWalkUp = 40

// heartbeat is the part of a wakatime-cli log line we care about. Everything
// else in the line --- the message, the caller, the plugin --- is ignored.
type heartbeat struct {
	File string  `json:"file"`
	Time float64 `json:"time"`
}

// RecentProjects returns the repositories WakaTime recorded edits in since the
// given moment, newest first, one entry per repository.
//
// This is the whole point of reading the log rather than scanning the disk:
// WakaTime already knows which file you are in, so the agent snapshots the
// project you are actually working on instead of every repository it can find.
// It requires `debug = true` in ~/.wakatime.cfg --- without it wakatime-cli
// logs only errors, and there is nothing here to read.
func (w WakaTime) RecentProjects(since time.Time) []Project {
	var out []Project
	seen := make(map[string]bool)
	home, _ := os.UserHomeDir()

	for _, hb := range w.recentHeartbeats(since) {
		root := repoRoot(hb.File, home)
		if root == "" || seen[root] {
			continue
		}
		seen[root] = true
		out = append(out, Project{Name: filepath.Base(root), Path: root})
	}
	return out
}

// recentHeartbeats parses the tail of the log and returns the entries newer
// than since, most recent first.
func (w WakaTime) recentHeartbeats(since time.Time) []heartbeat {
	f, err := os.Open(filepath.Join(w.Dir, "wakatime.log"))
	if err != nil {
		return nil
	}
	defer f.Close()

	fi, err := f.Stat()
	if err != nil {
		return nil
	}
	if fi.Size() > tailBytes {
		if _, err := f.Seek(fi.Size()-tailBytes, 0); err != nil {
			return nil
		}
	}

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64<<10), 1<<20) // log lines can be long
	if fi.Size() > tailBytes {
		sc.Scan() // discard the partial line the seek landed inside
	}

	var out []heartbeat
	cutoff := since.Unix()
	for sc.Scan() {
		line := sc.Bytes()
		if !strings.Contains(string(line), `"file"`) {
			continue // not a heartbeat line
		}
		var hb heartbeat
		if err := json.Unmarshal(line, &hb); err != nil || hb.File == "" {
			continue
		}
		if int64(hb.Time) < cutoff {
			continue
		}
		out = append(out, hb)
	}
	// Newest first: the log is append-ordered, so reverse it.
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out
}

// repoRoot walks up from a file to the nearest folder holding .git, which is
// how WakaTime itself decides what a project is --- so the names the agent
// reports line up with the ones on your WakaTime or Hackatime dashboard.
// Returns "" for a file that is not inside a repository.
//
// home is refused as a root even when it is a repository. Keeping dotfiles in
// a repo at ~ is common, and without this guard editing any loose file --- a
// download, a scratch note --- would resolve to the home directory and set the
// agent walking and hashing everything the user owns.
func repoRoot(file, home string) string {
	dir := filepath.Dir(filepath.Clean(file))
	if home != "" {
		home = filepath.Clean(home)
	}
	for i := 0; i < maxWalkUp; i++ {
		if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
			if home != "" && strings.EqualFold(dir, home) {
				return "" // a dotfiles repo is not a project
			}
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "" // reached the volume root
		}
		dir = parent
	}
	return ""
}

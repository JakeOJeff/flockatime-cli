package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"syscall"
	"time"

	"snapshot-agent/internal/activity"
	"snapshot-agent/internal/config"
	"snapshot-agent/internal/queue"
	"snapshot-agent/internal/snapshot"
	"snapshot-agent/internal/transport"
)

// batchLimit caps how much backlog rides along with one request.
const batchLimit = 200

// errReported means the command has already explained the failure on stdout
// and main should exit non-zero without printing it a second time.
var errReported = errors.New("already reported")

// cmdOnce captures a single snapshot per project and prints the payload. With
// a directory argument it skips the config entirely, which is the quickest way
// to see exactly what the agent would send.
func cmdOnce(args []string) error {
	// Not nil: an empty capture must encode as [], not null, so a consumer
	// piping this into jq gets an array either way.
	snaps := []snapshot.Snapshot{}

	if len(args) > 0 {
		dir, err := config.ExpandPath(args[0])
		if err != nil {
			return err
		}
		s, err := snapshot.Capture(filepath.Base(dir), dir, agentVersion)
		if err != nil {
			return err
		}
		snaps = append(snaps, *s)
	} else {
		path, err := configPath()
		if err != nil {
			return err
		}
		cfg, err := config.Load(path, false)
		if err != nil {
			return err
		}
		// Resolve projects the same way `run` does, so `once` shows what
		// would actually be sent rather than only what was pinned by hand.
		src, err := activity.New(cfg.Activity.Source, cfg.Activity.WakaTimeDir)
		if err != nil {
			return err
		}
		for _, p := range activeProjects(cfg, src, time.Now().Add(-cfg.IdleAfter())) {
			s, err := snapshot.Capture(p.Name, p.Path, agentVersion)
			if err != nil {
				return fmt.Errorf("project %q: %w", p.Name, err)
			}
			snaps = append(snaps, *s)
		}
	}

	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(snaps)
}

// cmdRun is the daemon loop: capture every interval, send, and fall back to
// the on-disk queue whenever the endpoint is unreachable.
func cmdRun(args []string) error {
	path, err := configPath()
	if err != nil {
		return err
	}
	cfg, err := config.Load(path, true)
	if err != nil {
		return err
	}

	src, err := activity.New(cfg.Activity.Source, cfg.Activity.WakaTimeDir)
	if err != nil {
		return err
	}

	q, err := queue.Open(cfg.QueuePath)
	if err != nil {
		return err
	}
	defer q.Close()

	client := transport.New(cfg.Endpoint, cfg.APIKey)
	lastHash := make(map[string]string, len(cfg.Projects))
	interval := time.Duration(cfg.IntervalSeconds) * time.Second
	idleAfter := cfg.IdleAfter()
	poll := time.Duration(cfg.Activity.PollSeconds) * time.Second

	logf("capturing every %s, endpoint %s", interval, cfg.Endpoint)
	logf("activity: %s --- dormant after %s quiet, checked every %s", src.Describe(), idleAfter, poll)
	if len(cfg.Projects) > 0 {
		logf("%d project(s) pinned in the config; others come from WakaTime", len(cfg.Projects))
	}

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)

	// Two clocks on purpose. The poll only moves the state machine, so a
	// returning user is picked up within poll rather than within interval;
	// the capture clock is what actually costs a walk and a request.
	pollT := time.NewTicker(poll)
	defer pollT.Stop()
	captureT := time.NewTicker(interval)
	defer captureT.Stop()

	// capture resolves which projects are live right now and snapshots them.
	// The list is rebuilt every time rather than fixed at startup, so opening
	// a project WakaTime has never seen before needs no restart and no config.
	capture := func() {
		projects := activeProjects(cfg, src, time.Now().Add(-idleAfter))
		if len(projects) == 0 {
			logf("nothing to capture --- WakaTime has not reported a file in a repository recently")
			return
		}
		tick(projects, client, q, lastHash)
	}

	// Start in whatever state the machine is already in: joining a session
	// already in progress captures at once, a quiet machine stays silent
	// until the first heartbeat.
	active := activity.Active(src, idleAfter, time.Now())
	if active {
		capture()
	} else {
		logf("dormant --- no activity in the last %s; waiting for a heartbeat", idleAfter)
	}

	for {
		select {
		case now := <-pollT.C:
			switch on := activity.Active(src, idleAfter, now); {
			case on && !active:
				active = true
				logf("waking --- activity detected")
				// Capture immediately: waiting for the next capture tick
				// would lose the first interval of the session.
				capture()
				captureT.Reset(interval)
			case !on && active:
				// A last snapshot marks the end of the session, so the
				// collector sees a close rather than a silent gap.
				active = false
				logf("going dormant --- no activity for %s", idleAfter)
				capture()
			}
		case <-captureT.C:
			if active {
				capture()
			}
		case <-stop:
			logf("shutting down")
			return nil
		}
	}
}

// activeProjects is what the agent captures on a given tick: the projects
// WakaTime saw edits in recently, plus any listed by hand in the config.
//
// Nothing is scanned for. WakaTime already resolved which file you are in, so
// the agent snapshots that repository and no others --- which is why there is
// no root to configure and no cost that scales with how many checkouts you
// happen to own.
func activeProjects(cfg *config.Config, src activity.Source, since time.Time) []config.Project {
	out := append([]config.Project{}, cfg.Projects...)

	w, ok := src.(activity.WakaTime)
	if !ok {
		return out
	}
	seen := make(map[string]bool, len(out))
	for _, p := range out {
		seen[p.Path] = true
	}
	for _, p := range w.RecentProjects(since) {
		if seen[p.Path] {
			continue // already listed by hand; the manual name wins
		}
		seen[p.Path] = true
		out = append(out, config.Project{Name: p.Name, Path: p.Path})
	}
	return out
}

// tick captures every project once, then sends the backlog and this round in a
// single request. Anything that fails to leave the machine lands in the queue.
func tick(projects []config.Project, client *transport.Client, q *queue.Queue, lastHash map[string]string) {
	fresh := make([]snapshot.Snapshot, 0, len(projects))

	for _, p := range projects {
		s, err := snapshot.Capture(p.Name, p.Path, agentVersion)
		if err != nil {
			logf("project %q: capture failed: %v", p.Name, err)
			continue
		}
		// Unchanged trees still report in, just without the file list, so
		// the timeline stays dense without repeating itself.
		if lastHash[p.Name] == s.TreeHash {
			fresh = append(fresh, s.AsUnchanged())
			logf("project %q: unchanged (%d files)", p.Name, s.FileCount)
		} else {
			fresh = append(fresh, *s)
			logf("project %q: %d files, %d lines, tree %s", p.Name, s.FileCount, s.TotalLines, short(s.TreeHash))
		}
		lastHash[p.Name] = s.TreeHash
	}

	backlog, err := q.Drain(batchLimit)
	if err != nil {
		logf("reading queue: %v", err)
	}

	// Oldest first: the backlog leads, this tick follows.
	batch := append(append([]snapshot.Snapshot{}, backlog...), fresh...)
	if len(batch) == 0 {
		return
	}

	if err := client.Send(batch); err != nil {
		logf("send failed (%v); queueing %d snapshot(s)", err, len(fresh))
		for _, s := range fresh {
			if err := q.Push(s); err != nil {
				logf("queueing failed: %v", err)
			}
		}
		return
	}

	// Only ack what was actually acknowledged by the server.
	if len(backlog) > 0 {
		if err := q.Ack(len(backlog)); err != nil {
			logf("clearing queue: %v", err)
		} else {
			logf("flushed %d queued snapshot(s)", len(backlog))
		}
	}
	logf("sent %d snapshot(s)", len(batch))
}

// cmdDoctor validates the config and reports what the agent would do, without
// sending anything.
func cmdDoctor(args []string) error {
	path, err := configPath()
	if err != nil {
		return err
	}
	fmt.Printf("config:   %s\n", path)

	cfg, err := config.Load(path, true)
	if err != nil {
		fmt.Printf("status:   INVALID --- %v\n", err)
		if errors.Is(err, config.ErrNoConfig) {
			printConfigHelp(path)
		}
		return errReported
	}
	fmt.Printf("endpoint: %s/v1/snapshots\n", cfg.Endpoint)
	fmt.Printf("interval: %ds\n", cfg.IntervalSeconds)
	fmt.Printf("api_key:  %s\n", mask(cfg.APIKey))

	printActivity(cfg)

	client := transport.New(cfg.Endpoint, cfg.APIKey)
	if err := client.Reachable(); err != nil {
		fmt.Printf("reach:    UNREACHABLE --- %v\n", err)
	} else {
		fmt.Printf("reach:    ok\n")
	}

	if q, err := queue.Open(cfg.QueuePath); err == nil {
		n, _ := q.Len()
		fmt.Printf("queue:    %s (%d waiting)\n", cfg.QueuePath, n)
		q.Close()
	} else {
		fmt.Printf("queue:    unavailable --- %v\n", err)
	}

	src, _ := activity.New(cfg.Activity.Source, cfg.Activity.WakaTimeDir)
	projects := cfg.Projects
	if src != nil {
		projects = activeProjects(cfg, src, time.Now().Add(-cfg.IdleAfter()))
	}

	fmt.Printf("\nprojects (%d pinned, %d from WakaTime):\n",
		len(cfg.Projects), len(projects)-len(cfg.Projects))
	if len(projects) == 0 {
		printNoProjectsHelp(cfg)
	}
	for _, p := range projects {
		s, err := snapshot.Capture(p.Name, p.Path, agentVersion)
		if err != nil {
			fmt.Printf("  %-16s %s\n      ERROR %v\n", p.Name, p.Path, err)
			continue
		}
		git := "no git"
		if s.Git != nil {
			git = fmt.Sprintf("%s @ %s", s.Git.Branch, short(s.Git.Head))
			if s.Git.Dirty {
				git += " (dirty)"
			}
		}
		fmt.Printf("  %-16s %s\n      %d files, %d lines, tree %s, %s\n",
			p.Name, p.Path, s.FileCount, s.TotalLines, short(s.TreeHash), git)
	}
	return nil
}

// printActivity reports which state `run` would start in and why --- the
// first question to ask when the agent is quiet and you expected snapshots.
func printActivity(cfg *config.Config) {
	src, err := activity.New(cfg.Activity.Source, cfg.Activity.WakaTimeDir)
	if err != nil {
		fmt.Printf("activity: INVALID --- %v\n", err)
		return
	}
	idleAfter := cfg.IdleAfter()
	fmt.Printf("activity: %s (dormant after %s quiet)\n", src.Describe(), idleAfter)

	last := src.LastActivity()
	if last.IsZero() {
		fmt.Printf("          no activity signal found --- would start dormant\n")
		return
	}
	state := "dormant"
	if activity.Active(src, idleAfter, time.Now()) {
		state = "ACTIVE"
	}
	fmt.Printf("          last activity %s ago --- would start %s\n",
		time.Since(last).Round(time.Second), state)
}

// printNoProjectsHelp explains an empty project list. Almost always the cause
// is that wakatime-cli logs only errors until `debug = true` is set, so there
// is nothing in the log to learn a project from.
func printNoProjectsHelp(cfg *config.Config) {
	if cfg.Activity.Source != "wakatime" {
		fmt.Printf("  none --- add a [[project]] entry, or set [activity] source = \"wakatime\"\n")
		return
	}
	log := filepath.Join(cfg.Activity.WakaTimeDir, "wakatime.log")
	fmt.Printf("  none yet.\n\n")
	fmt.Printf("  Projects are read from %s, which\n", log)
	fmt.Printf("  only records the file you are editing when debug logging is on.\n\n")
	fmt.Printf("  Add this to ~/.wakatime.cfg under [settings]:\n\n")
	fmt.Printf("      debug = true\n\n")
	fmt.Printf("  Then edit a file in a git repository and run doctor again. This\n")
	fmt.Printf("  changes only what WakaTime writes locally --- your heartbeats and\n")
	fmt.Printf("  your dashboard are unaffected.\n")
}

func logf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "%s  %s\n", time.Now().Format(time.RFC3339), fmt.Sprintf(format, args...))
}

// short trims a hash for human-readable output.
func short(h string) string {
	if len(h) > 12 {
		return h[:12]
	}
	if h == "" {
		return "-"
	}
	return h
}

// mask keeps the api_key out of terminal scrollback and screenshots.
func mask(k string) string {
	if len(k) <= 4 {
		return "****"
	}
	return "****" + k[len(k)-4:]
}

// printConfigHelp is what `doctor` prints when there is no config at all ---
// the first thing anyone hits on a fresh machine.
func printConfigHelp(path string) {
	fmt.Printf(`
fix:      create %s with at least:

            endpoint = "https://collect.example.com"
            api_key = "your-key"

            [[project]]
            name = "flockatime"
            path = "D:/products/flockatime"

          snapshot-agent.example.toml in this repo is a ready-made copy.
          On Windows write paths with forward slashes or in 'single quotes':
          TOML reads \U in a double-quoted string as an escape code.
`, path)
	if runtime.GOOS != "windows" {
		fmt.Printf("          Then: chmod 600 %s\n", path)
	}
	fmt.Printf("\nno config needed:  snapshot-agent once .   (snapshots this directory, sends nothing)\n")
}

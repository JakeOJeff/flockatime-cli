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
	"snapshot-agent/internal/aitrace"
	"snapshot-agent/internal/cadence"
	"snapshot-agent/internal/config"
	"snapshot-agent/internal/fingerprint"
	"snapshot-agent/internal/linestate"
	"snapshot-agent/internal/queue"
	"snapshot-agent/internal/snapshot"
	"snapshot-agent/internal/transport"
	"snapshot-agent/internal/update"
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
		// No config is needed here, but if there is one its path_salt
		// applies, so the hashes match what `run` sends.
		c, err := newCapturer(optionalConfig())
		if err != nil {
			return err
		}
		s, err := c.capture(filepath.Base(dir), dir, false)
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
		c, err := newCapturer(cfg)
		if err != nil {
			return err
		}
		// Resolve projects the same way `run` does, so `once` shows what
		// would actually be sent rather than only what was pinned by hand.
		src, err := newSource(cfg, c)
		if err != nil {
			return err
		}
		for _, p := range activeProjects(cfg, src, time.Now().Add(-cfg.IdleAfter())) {
			s, err := c.capture(p.Name, p.Path, false)
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

// optionalConfig is the config if there is a usable one, else nil.
func optionalConfig() *config.Config {
	path, err := configPath()
	if err != nil {
		return nil
	}
	cfg, err := config.Load(path, false)
	if err != nil {
		return nil
	}
	return cfg
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

	// Update before opening the queue: the restarted process needs its lock.
	update.CleanupOld()
	upd := &autoUpdater{enabled: cfg.AutoUpdateEnabled()}
	if exe, ok := upd.check(); ok {
		return &restartError{exe: exe}
	}
	if !upd.enabled {
		logf("auto-update is off")
	}

	q, err := queue.Open(cfg.QueuePath)
	if err != nil {
		return err
	}
	defer q.Close()

	client := transport.New(cfg.Endpoint, cfg.APIKey)
	capt, err := newCapturer(cfg)
	if err != nil {
		return err
	}
	src, err := newSource(cfg, capt)
	if err != nil {
		return err
	}
	capt.poller = cadence.New()
	saves := &savePoll{p: capt.poller}
	defer saves.stop()
	interval := time.Duration(cfg.IntervalSeconds) * time.Second
	idleAfter := cfg.IdleAfter()
	poll := time.Duration(cfg.Activity.PollSeconds) * time.Second

	logf("capturing every %s, endpoint %s", interval, cfg.Endpoint)
	logf("activity: %s --- dormant after %s quiet, checked every %s", src.Describe(), idleAfter, poll)
	if len(cfg.Projects) > 0 {
		logf("%d project(s) pinned in the config; others come from WakaTime and AI sessions", len(cfg.Projects))
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

	// lastProjects is what the previous capture covered. Going dormant happens
	// exactly when the newest heartbeat ages past idleAfter, so a fresh lookup
	// at that instant finds nothing --- the closing snapshot has to be taken
	// against the projects the session was actually spent in.
	var lastProjects []config.Project
	var lastCapture time.Time

	// capture resolves which projects are live right now and snapshots them.
	// The list is rebuilt every time rather than fixed at startup, so opening
	// a project WakaTime has never seen before needs no restart and no config.
	//
	// Before a capture (but never the closing one, which should not wait on a
	// download) it checks for a new release, and returns a restartError when
	// one was installed.
	capture := func(closing bool) error {
		if !closing {
			if exe, ok := upd.check(); ok {
				return &restartError{exe: exe}
			}
		}
		projects := activeProjects(cfg, src, time.Now().Add(-idleAfter))
		if len(projects) == 0 && closing {
			projects = lastProjects
		}
		if len(projects) == 0 {
			logf("nothing to capture --- no WakaTime heartbeat or AI session in a repository recently")
			return nil
		}
		lastProjects = projects
		tick(projects, client, q, capt)
		lastCapture = time.Now()
		return nil
	}

	// Start in whatever state the machine is already in: joining a session
	// already in progress captures at once, a quiet machine stays silent
	// until the first heartbeat.
	active := activity.Active(src, idleAfter, time.Now())
	if active {
		saves.start()
		if err := capture(false); err != nil {
			return err
		}
	} else {
		logf("dormant --- no activity in the last %s; waiting for a heartbeat", idleAfter)
	}
	// report leaves a status file for `doctor`. It runs on every poll too,
	// so its age alone tells doctor whether the agent is alive.
	report := func() {
		writeStatus(capt.status(active, saves.cancel != nil, lastCapture, cfg.Activity.PollSeconds))
	}
	report()

	for {
		select {
		case now := <-pollT.C:
			switch on := activity.Active(src, idleAfter, now); {
			case on && !active:
				active = true
				logf("waking --- activity detected")
				saves.start()
				// Capture immediately: waiting for the next capture tick
				// would lose the first interval of the session.
				if err := capture(false); err != nil {
					return err
				}
				captureT.Reset(interval)
			case !on && active:
				// A last snapshot marks the end of the session, so the
				// collector sees a close rather than a silent gap.
				active = false
				logf("going dormant --- no activity for %s", idleAfter)
				capture(true)
				saves.stop()
			}
			report()
		case <-captureT.C:
			if active {
				if err := capture(false); err != nil {
					return err
				}
			}
			report()
		case <-stop:
			logf("shutting down")
			st := capt.status(active, false, lastCapture, cfg.Activity.PollSeconds)
			st.Stopped = true
			writeStatus(st)
			return nil
		}
	}
}

// activeProjects is what the agent captures on a given tick: any projects
// listed by hand in the config, then the repositories WakaTime saw edits in
// and AI agent sessions worked in recently.
//
// Nothing is scanned for. WakaTime and the agents' transcripts already say
// which repository you were in, so the agent snapshots those and no others ---
// which is why there is no root to configure and no cost that scales with how
// many checkouts you happen to own.
func activeProjects(cfg *config.Config, src activity.Source, since time.Time) []config.Project {
	out := append([]config.Project{}, cfg.Projects...)
	seen := make(map[string]bool, len(out))
	for _, p := range out {
		seen[aitrace.Key(p.Path)] = true
	}
	type finder interface {
		RecentProjects(since time.Time) []activity.Project
	}
	for _, s := range activity.Flatten(src) {
		f, ok := s.(finder)
		if !ok {
			continue
		}
		for _, p := range f.RecentProjects(since) {
			if seen[aitrace.Key(p.Path)] {
				continue // already listed by hand, or found by an earlier source
			}
			seen[aitrace.Key(p.Path)] = true
			out = append(out, config.Project{Name: p.Name, Path: p.Path})
		}
	}
	return out
}

// tick captures every project once, then sends the backlog and this round in a
// single request. Anything that fails to leave the machine lands in the queue.
func tick(projects []config.Project, client *transport.Client, q *queue.Queue, capt *capturer) {
	fresh := make([]snapshot.Snapshot, 0, len(projects))

	for _, p := range projects {
		s, err := capt.capture(p.Name, p.Path, true)
		if err != nil {
			logf("project %q: capture failed: %v", p.Name, err)
			continue
		}
		// Unchanged trees still report in, just without the file list, so
		// the timeline stays dense without repeating itself.
		if s.PrevTreeHash == s.TreeHash {
			fresh = append(fresh, s.AsUnchanged())
			logf("project %q: unchanged (%d files, %d saves, seq %d)", p.Name, s.FileCount, len(s.SaveTimes), s.Seq)
		} else {
			fresh = append(fresh, *s)
			var added, removed, fps int
			for _, f := range s.Files {
				added += f.Added
				removed += f.Removed
				if len(f.Fingerprints) > 0 {
					fps++
				}
			}
			logf("project %q: %d files, %d lines (+%d -%d), %d saves, %d fingerprinted, seq %d, tree %s",
				p.Name, s.FileCount, s.TotalLines, added, removed, len(s.SaveTimes), fps, s.Seq, short(s.TreeHash))
		}
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
	printAI(cfg)
	printDaemon()

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
	} else if errors.Is(err, queue.ErrBusy) {
		fmt.Printf("queue:    %s (in use by the running agent)\n", cfg.QueuePath)
	} else {
		fmt.Printf("queue:    unavailable --- %v\n", err)
	}

	src, _ := sourceFor(cfg)
	projects := cfg.Projects
	if src != nil {
		projects = activeProjects(cfg, src, time.Now().Add(-cfg.IdleAfter()))
	}

	salt, saltErr := pathSalt(cfg)
	if saltErr != nil {
		fmt.Printf("salt:     ERROR --- %v\n", saltErr)
	} else if cfg.PathSalt != "" {
		fmt.Printf("salt:     path_salt from the config\n")
	} else if p, err := saltFile(); err == nil {
		fmt.Printf("salt:     %s\n", p)
	}
	saltID := linestate.SaltID(salt)
	stateDir, _ := linestate.Dir()
	fmt.Printf("\nprojects (%d pinned, %d from WakaTime):\n",
		len(cfg.Projects), len(projects)-len(cfg.Projects))
	if len(projects) == 0 {
		printNoProjectsHelp(cfg)
	}

	for _, p := range projects {
		// Count fingerprintable files in the same walk; doctor is run by
		// hand, so reading every file once more is fine here.
		fp := &fpCounter{}
		s, err := snapshot.CaptureWith(p.Name, p.Path, agentVersion, snapshot.Options{PathSalt: salt, Observer: fp})
		if err != nil {
			fmt.Printf("  %-16s %s\n      ERROR %v\n", p.Name, p.Path, err)
			continue
		}
		git := "no git"
		if s.Git != nil {
			git = fmt.Sprintf("%s @ %s", s.Git.Branch, short(s.Git.Head))
			if s.Git.Dirty {
				g := s.Git
				git += fmt.Sprintf(" (dirty: %d staged, %d modified, %d untracked, +%d -%d)",
					g.Staged, g.Modified, g.Untracked, g.Insertions, g.Deletions)
			}
			git += fmt.Sprintf(", %d commits, %d ahead / %d behind", s.Git.Commits, s.Git.Ahead, s.Git.Behind)
		}
		fmt.Printf("  %-16s %s\n      %d files, %d lines, tree %s, %s\n",
			p.Name, p.Path, s.FileCount, s.TotalLines, short(s.TreeHash), git)
		if s.SkippedFiles > 0 {
			fmt.Printf("      %d file(s) left out: over 2 MB or unreadable\n", s.SkippedFiles)
		}
		fmt.Printf("      state: %s\n", describeState(stateDir, p.Name, saltID))
		fmt.Printf("      fingerprints: %d of %d files can be fingerprinted (the rest are too short, binary, or unreadable)\n",
			fp.ok, s.FileCount)
		if s.BuildTouchedAt > 0 || s.TestTouchedAt > 0 {
			fmt.Printf("      last build %s, last test run %s\n", ago(s.BuildTouchedAt), ago(s.TestTouchedAt))
		}
	}
	return nil
}

// fpCounter is an Observer that counts files fingerprinting would cover.
type fpCounter struct{ ok int }

func (c *fpCounter) Observe(absPath string, f *snapshot.File) {
	if fp, err := fingerprint.File(absPath, fingerprint.MaxPerFile); err == nil && len(fp) > 0 {
		c.ok++
	}
}

// describeState explains a project's state file in terms of what the
// collector will see, so a reset chain or a quiet baseline is expected rather
// than suspicious.
func describeState(dir, project, saltID string) string {
	if dir == "" {
		return "UNAVAILABLE --- no home directory"
	}
	info, err := linestate.Inspect(dir, project, saltID)
	switch {
	case err != nil:
		return fmt.Sprintf("UNREADABLE --- %v; line counts and the chain restart at the next capture", err)
	case !info.Exists:
		return "none yet --- the first capture is a baseline (no line counts) and starts the chain at seq 1"
	case !info.Readable:
		return "CORRUPT --- the next capture is a baseline and the chain restarts at seq 1"
	case !info.SaltMatch:
		return fmt.Sprintf("seq %d; written with a different path salt, so the next capture is a quiet baseline (the chain continues)", info.Seq)
	}
	return fmt.Sprintf("ok --- seq %d, %d files remembered, %d KB, saved %s",
		info.Seq, info.Files, (info.Bytes+1023)/1024, ago(info.ModTime.Unix()))
}

// printDaemon reads what a running agent last reported about itself.
func printDaemon() {
	st, err := readStatus()
	if err != nil {
		fmt.Printf("daemon:   no status --- `run` has not started on this machine (or ~/.flockatime is unwritable)\n")
		return
	}
	age := time.Since(time.Unix(st.UpdatedAt, 0))
	// It reports on every activity poll, so three missed polls means gone.
	alive := !st.Stopped && age <= 3*time.Duration(max(st.PollSeconds, 1))*time.Second+5*time.Second
	state := "dormant"
	if st.Active {
		state = "active"
	}
	if alive {
		fmt.Printf("daemon:   running %s (pid %d), %s, last capture %s, last heard %s ago\n",
			st.Version, st.PID, state, ago(st.LastCaptureAt), age.Round(time.Second))
	} else if st.Stopped {
		fmt.Printf("daemon:   stopped cleanly %s ago (version %s)\n", age.Round(time.Second), st.Version)
	} else {
		fmt.Printf("daemon:   NOT RUNNING --- last heard from %s ago (version %s, pid %d)\n",
			age.Round(time.Second), st.Version, st.PID)
	}
	switch {
	case !alive:
	case st.CadenceOn:
		fmt.Printf("cadence:  polling %d file(s) in %d project(s), last pass %s (%d ms)\n",
			st.CadenceFiles, st.CadenceProjs, ago(st.LastPollAt), st.LastPollMs)
	default:
		fmt.Printf("cadence:  paused while dormant (resumes on the next heartbeat)\n")
	}
	if alive {
		fmt.Printf("          %d changed file(s) fingerprinted since it started\n", st.FingerprintedN)
	}
}

// ago renders a unix time as "3m ago", or "never" for zero.
func ago(unix int64) string {
	if unix <= 0 {
		return "never"
	}
	return time.Since(time.Unix(unix, 0)).Round(time.Second).String() + " ago"
}

// printActivity reports which state `run` would start in and why --- the
// first question to ask when the agent is quiet and you expected snapshots.
func printActivity(cfg *config.Config) {
	src, err := sourceFor(cfg)
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

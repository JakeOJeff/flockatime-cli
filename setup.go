package main

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"strings"

	"github.com/BurntSushi/toml"

	"snapshot-agent/internal/config"
	"snapshot-agent/internal/transport"
	"snapshot-agent/internal/wakacfg"
)

// setupInterval is how often an active session is captured. Hackatime's
// editor plugins send a heartbeat about every two minutes; matching that keeps
// the two timelines at the same grain.
const setupInterval = 120

// cmdSetup does everything the install one-liner promises, in one step:
// writes the config, points it at WakaTime so projects are picked up the way
// Hackatime picks them up, turns on the WakaTime debug log that makes that
// possible, and registers the agent to run at login. It is safe to re-run ---
// that is how an upgrade or a new key is applied.
func cmdSetup(args []string) error {
	flags := flag.NewFlagSet("setup", flag.ContinueOnError)
	endpoint := flags.String("endpoint", "", "collector URL, e.g. https://flockatime.example.workers.dev")
	key := flags.String("key", os.Getenv("FLOCKATIME_KEY"), "API key from the dashboard (or FLOCKATIME_KEY)")
	interval := flags.Int("interval", setupInterval, "seconds between captures while you are coding")
	noStart := flags.Bool("no-start", false, "write the config but do not install or start the background agent")
	if err := flags.Parse(args); err != nil {
		return err
	}
	*endpoint = strings.TrimRight(strings.TrimSpace(*endpoint), "/")
	*key = strings.TrimSpace(*key)
	if *endpoint == "" || *key == "" {
		return fmt.Errorf("usage: snapshot-agent setup --endpoint URL --key KEY")
	}

	// 1. The key, before anything is written: a typo should fail here, not
	//    silently on every tick in a hidden process.
	fmt.Printf("checking key with %s ... ", *endpoint)
	if err := transport.New(*endpoint, *key).Verify(); err != nil {
		fmt.Println("failed")
		return err
	}
	fmt.Println("ok")

	// 2. The agent's config.
	path, err := configPath()
	if err != nil {
		return err
	}
	if err := writeSetupConfig(path, *endpoint, *key, *interval); err != nil {
		return err
	}
	fmt.Printf("config:   %s\n", path)

	// 3. WakaTime's debug log, which is how the agent learns which project
	//    you are in. Nothing about your heartbeats changes.
	wakaReady := enableWakaTimeDebug()

	// 4. Run at login, and now.
	if *noStart {
		fmt.Printf("\nskipped the background agent (--no-start); start it with: snapshot-agent install\n")
		return nil
	}
	exe, err := selfPath()
	if err != nil {
		return err
	}
	fmt.Println()
	if err := installLoginItem(exe); err != nil {
		return err
	}

	fmt.Printf("\ndone. Code in any git repo with Hackatime running and it shows up on\n")
	fmt.Printf("%s within a couple of minutes.\n", *endpoint)
	if !wakaReady {
		fmt.Printf("\nOne step left: install Hackatime (https://hackatime.hackclub.com/setup),\n")
		fmt.Printf("then run this installer again so the agent can follow it.\n")
	}
	fmt.Printf("\nCheck on it any time with: snapshot-agent doctor\n")
	return nil
}

// writeSetupConfig writes the agent config for the one-command install. An
// existing config keeps its hand-pinned projects and queue location; only the
// connection and activity settings are replaced.
func writeSetupConfig(path, endpoint, key string, interval int) error {
	var cfg config.Config
	if _, err := toml.DecodeFile(path, &cfg); err != nil && !errors.Is(err, fs.ErrNotExist) {
		// An unreadable config is replaced, but kept alongside for reference.
		backup := path + ".bak"
		if rerr := os.Rename(path, backup); rerr == nil {
			fmt.Printf("replaced an unreadable config (saved as %s)\n", backup)
		}
		cfg = config.Config{}
	}
	cfg.Endpoint = endpoint
	cfg.APIKey = key
	cfg.IntervalSeconds = interval
	cfg.Activity.Source = "wakatime"

	var buf bytes.Buffer
	buf.WriteString("# Written by `snapshot-agent setup`. Re-run the installer to change the key.\n")
	buf.WriteString("# Projects come from WakaTime/Hackatime; add [[project]] entries to pin more.\n\n")
	if err := toml.NewEncoder(&buf).Encode(cfg); err != nil {
		return err
	}
	// 0600: the file holds the API key, and Load refuses anything looser.
	if err := os.WriteFile(path, buf.Bytes(), 0o600); err != nil {
		return fmt.Errorf("writing %s: %w", path, err)
	}
	return os.Chmod(path, 0o600)
}

// enableWakaTimeDebug turns on the WakaTime debug log and reports whether
// WakaTime is installed at all.
func enableWakaTimeDebug() bool {
	path, err := wakacfg.Path()
	if err != nil {
		fmt.Printf("wakatime: could not locate config: %v\n", err)
		return false
	}
	changed, err := wakacfg.EnableDebug(path)
	switch {
	case errors.Is(err, wakacfg.ErrNotInstalled):
		fmt.Printf("wakatime: not installed (no %s)\n", path)
		return false
	case err != nil:
		fmt.Printf("wakatime: could not edit %s: %v\n", path, err)
		fmt.Printf("          add `debug = true` under [settings] yourself\n")
		return false
	case changed:
		fmt.Printf("wakatime: turned on debug logging in %s\n", path)
	default:
		fmt.Printf("wakatime: debug logging already on in %s\n", path)
	}
	return true
}

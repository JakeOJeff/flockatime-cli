package main

import (
	"flag"
	"fmt"
	"strings"
	"time"

	"snapshot-agent/internal/config"
	"snapshot-agent/internal/snapshot"
)

// revealLookback is how far back WakaTime's log is searched for projects when
// no --project or --dir narrows it: a reviewer's question can come days later.
const revealLookback = 90 * 24 * time.Hour

// cmdReveal answers "which file is 9d2e...?" on the owner's machine. It hashes
// the names of the files a capture would see, with the same salt, and prints
// the matches. Nothing is sent; what to share is the owner's decision.
func cmdReveal(args []string) error {
	flags := flag.NewFlagSet("reveal", flag.ContinueOnError)
	project := flags.String("project", "", "only search the project with this name")
	dir := flags.String("dir", "", "search this directory instead of the configured projects")
	// Accept the hash before or after the flags.
	var hash string
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		hash, args = args[0], args[1:]
	}
	if err := flags.Parse(args); err != nil {
		return err
	}
	if hash == "" && flags.NArg() > 0 {
		hash = flags.Arg(0)
	}
	hash = strings.ToLower(strings.TrimSpace(hash))
	if len(hash) < 8 || strings.Trim(hash, "0123456789abcdef") != "" {
		return fmt.Errorf("usage: snapshot-agent reveal <path_hash> [--project NAME] [--dir PATH]\n  (at least the first 8 hex characters of the hash)")
	}

	cfg := optionalConfig()
	salt, err := pathSalt(cfg)
	if err != nil {
		return err
	}

	var projects []config.Project
	switch {
	case *dir != "":
		d, err := config.ExpandPath(*dir)
		if err != nil {
			return err
		}
		projects = []config.Project{{Name: *dir, Path: d}}
	case cfg == nil:
		return fmt.Errorf("no usable config; pass --dir PATH to search a directory")
	default:
		src, _ := sourceFor(cfg)
		projects = cfg.Projects
		if src != nil {
			projects = activeProjects(cfg, src, time.Now().Add(-revealLookback))
		}
	}

	found := 0
	for _, p := range projects {
		if *project != "" && p.Name != *project {
			continue
		}
		matches, err := snapshot.Reveal(p.Path, salt, hash)
		if err != nil {
			fmt.Printf("%s: %v\n", p.Name, err)
			continue
		}
		for _, m := range matches {
			fmt.Printf("%s\t%s\n", p.Name, m)
			found++
		}
	}
	if found == 0 {
		fmt.Printf("no file under the searched projects hashes to %s\n", hash)
		fmt.Printf("  (a deleted, renamed or newly ignored file can no longer be found)\n")
		return errReported
	}
	return nil
}

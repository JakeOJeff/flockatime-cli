// Command snapshot-agent records the shape of a project directory --- path
// hashes, content hashes, line counts --- and reports it to a collection
// endpoint. It never reads, transmits, or logs file contents.
package main

import (
	"errors"
	"fmt"
	"os"

	"snapshot-agent/internal/config"
)

// agentVersion is stamped at link time by the release build with
// -ldflags "-X main.agentVersion=...". A plain `go build` keeps this default.
var agentVersion = "0.1.0"

var usage = `snapshot-agent ` + agentVersion + `

usage:
  snapshot-agent once [dir]    capture one snapshot, print JSON, send nothing
  snapshot-agent run           daemon loop: capture every interval and POST
  snapshot-agent doctor        validate config, probe endpoint, print project stats
  snapshot-agent install       start the agent at every login, in the background
  snapshot-agent uninstall     stop starting it at login
  snapshot-agent devserver     a local endpoint that prints what it receives
  snapshot-agent version       print the agent version

config: ~/.snapshot-agent.toml (mode 0600)
`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}

	var err error
	switch os.Args[1] {
	case "once":
		err = cmdOnce(os.Args[2:])
	case "run":
		err = cmdRun(os.Args[2:])
	case "doctor":
		err = cmdDoctor(os.Args[2:])
	case "devserver":
		err = cmdDevServer(os.Args[2:])
	case "install":
		err = cmdInstall(os.Args[2:])
	case "uninstall":
		err = cmdUninstall(os.Args[2:])
	case "version":
		fmt.Println(agentVersion)
	case "-h", "--help", "help":
		fmt.Print(usage)
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n%s", os.Args[1], usage)
		os.Exit(2)
	}

	if err != nil {
		// doctor prints its own diagnosis --- do not say it twice.
		if !errors.Is(err, errReported) {
			fmt.Fprintln(os.Stderr, "snapshot-agent:", err)
			if errors.Is(err, config.ErrNoConfig) {
				fmt.Fprintln(os.Stderr, "  run `snapshot-agent doctor` to see how to create one")
			}
		}
		os.Exit(1)
	}
}

// configPath honours SNAPSHOT_AGENT_CONFIG so a test run never has to touch
// the real config in your home directory.
func configPath() (string, error) {
	if p := os.Getenv("SNAPSHOT_AGENT_CONFIG"); p != "" {
		return p, nil
	}
	return config.DefaultPath()
}

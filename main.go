// Command snapshot-agent records the shape of a project directory --- paths
// hashes, line counts, content hashes --- and reports them to a collection
// endpoint. It never reads, transmits, or logs file contents.
package main

import (
	"fmt"
	"os"
)

const agentVersion = "0.1.0"

const usage = `snapshot-agent ` + agentVersion + `

usage:
  snapshot-agent once     capture one snapshot per project, print JSON, send nothing
  snapshot-agent run      daemon loop: capture every interval and POST
  snapshot-agent doctor   validate config, probe the endpoint, print project stats
  snapshot-agent version  print the agent version
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
	case "version":
		fmt.Println(agentVersion)
	case "-h", "--help", "help":
		fmt.Print(usage)
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n%s", os.Args[1], usage)
		os.Exit(2)
	}

	if err != nil {
		fmt.Fprintln(os.Stderr, "snapshot-agent:", err)
		os.Exit(1)
	}
}

func cmdOnce(args []string) error   { return errNotImplemented("once") }
func cmdRun(args []string) error    { return errNotImplemented("run") }
func cmdDoctor(args []string) error { return errNotImplemented("doctor") }

func errNotImplemented(cmd string) error {
	return fmt.Errorf("%s: not implemented yet", cmd)
}

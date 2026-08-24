# snapshot-agent

Records the *shape* of a project directory --- path hashes, content hashes,
line counts --- and POSTs it to a collection endpoint on an interval.

It never reads, transmits, or logs file contents. Paths are sent as SHA-256
hashes, never in plaintext. No keystrokes, window titles, process lists, or
screenshots.

## Commands

    snapshot-agent once     capture one snapshot per project, print JSON, send nothing
    snapshot-agent run      daemon loop: capture every interval and POST
    snapshot-agent doctor   validate config, probe the endpoint, print project stats

## Config

`~/.snapshot-agent.toml`, mode 0600. The agent refuses to start if the file is
readable by anyone but its owner.

    endpoint = "https://collect.example.com"
    api_key = "..."
    interval_seconds = 300

    [[project]]
    name = "strict"
    path = "~/src/strict"

## Build

    go build -o snapshot-agent .

Static, no CGO.

## Status

Scaffolding only. Package boundaries, wire types, and command dispatch are in
place; `once` is the next piece to land, then the daemon and offline queue.

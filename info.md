# snapshot-agent

A small background program that records the **shape** of your code folders over
time — how many files, how long they are, what changed since last tick — and
POSTs that to a server you choose.

It never reads your code. File bytes go straight into SHA-256 and are thrown
away; even the file *names* leave as hashes. What a collector sees is
`9d2e4c...` is 412 lines and changed at 2:15pm — not what it's called or what's
in it.

Static Go binary, no CGO.

## What it sends, and what it never sends

```json
[{
  "project": "flockatime-cli",
  "captured_at": 1788371950,
  "tree_hash": "f397336f865d...",
  "file_count": 28,
  "total_lines": 3800,
  "git": { "head": "eb4cf53b...", "branch": "main", "dirty": false, "ahead": 0 },
  "files": [
    { "path_hash": "618cd5b8...", "content_hash": "52d9604e...", "lines": 10, "bytes": 298, "mtime": 1787687549 }
  ],
  "agent_version": "0.1.0"
}]
```

`tree_hash` is SHA-256 over the sorted `path_hash:content_hash` list — stable
across runs, different after a one-character edit or a rename.

**Never on the wire:** file contents, file names, folder names, keystrokes,
window titles, processes, screenshots, the clipboard. The only plaintext is the
project name, the git branch, and the commit SHA.

## Try it

```
go build -o snapshot-agent .     # snapshot-agent.exe on Windows
./snapshot-agent once .
```

That prints the JSON for the current folder and sends nothing. Search it for one
of your filenames — it isn't there.

To watch it actually send, start the built-in test collector in one terminal:

```
./snapshot-agent devserver 8799
```

Write `loop.toml`:

```toml
endpoint = "http://127.0.0.1:8799"
api_key = "demo-key"
interval_seconds = 10
queue_path = "./loop-queue.db"

[[project]]
name = "demo"
path = "C:/Users/you/some-project"
```

And in another terminal:

```
  $env:SNAPSHOT_AGENT_CONFIG = "./agent.toml"; ./snapshot-agent.exe run # on windows
SNAPSHOT_AGENT_CONFIG=./loop.toml ./snapshot-agent run
```

```
project "demo": 28 files, 3800 lines, tree f397336f865d
sent 1 snapshot(s) 
```
added thing 

Edit a file and the tree hash moves. Change nothing and it sends a 319-byte
`unchanged` marker instead of the 6 KB file list. Kill the devserver and
snapshots queue to disk with their original timestamps, then flush oldest-first
when it comes back.

> **Windows:** write paths with forward slashes or in 'single quotes'. TOML
> reads `\U` in a double-quoted string as an escape code.

## Commands

| Command | Does |
|---|---|
| `once [dir]` | one snapshot, printed as JSON, sends nothing. With a dir it needs no config |
| `run` | the daemon: capture every interval, POST, queue to disk on failure |
| `doctor` | validate config, probe the endpoint, list resolved projects. Sends nothing |
| `devserver [port]` | a local collector that prints what it receives. Default `127.0.0.1:8787` |
| `install` / `uninstall` | start at login, hidden, no Administrator |
| `version` | print the version |

Config lives at `~/.snapshot-agent.toml`, or wherever `SNAPSHOT_AGENT_CONFIG`
points. On Unix it must be `chmod 600`.

**Start with `doctor`** whenever something seems wrong — it says which config it
read, whether the endpoint answered, and what it would capture.

## Configuration

```toml
endpoint = "https://collect.example.com"   # "/v1/snapshots" is appended
api_key  = "your-key"                      # sent as: Authorization: Bearer ...
interval_seconds = 300                     # default 300
queue_path = "~/.snapshot-agent-queue.db"  # where failed sends wait

# Optional: capture only while you're actually editing, using WakaTime's
# heartbeat files. Read-only — it cannot affect your WakaTime data.
[activity]
source = "wakatime"

# Optional with source = "wakatime", required otherwise.
[[project]]
name = "demo"
path = "~/src/demo"
```

With `source = "wakatime"` you don't list projects. WakaTime already knows which
file you're editing, so the agent snapshots that repository, wakes on your first
heartbeat, and goes dormant ten minutes after you stop. It needs `debug = true`
under `[settings]` in `~/.wakatime.cfg` to see which file you're in; `doctor`
says so if it's missing.

## The collector
  
Anything that accepts this is a valid backend:

```
POST {endpoint}/v1/snapshots
Authorization: Bearer {api_key}
Content-Type: application/json

[ {snapshot}, {snapshot}, ... ]     always an array
```

- **Any 2xx means accepted.** Anything else and the agent queues and retries.
- The response body is ignored. An empty `202` is fine.
- Delivery is **at least once** — dedupe on `(project, captured_at, tree_hash)`.
  `captured_at` is never rewritten, so a snapshot delayed by an outage still
  reports when it was taken.
- Batches are oldest-first, up to 200 queued snapshots per request. Roughly
  200 bytes per file; the queue holds 5000 before dropping the oldest.

`devserver.go` is a working collector in 75 lines.

## Installing it

```
go build -o ~/bin/snapshot-agent.exe .    # somewhere permanent; the path is recorded
~/bin/snapshot-agent.exe doctor           # check it first
~/bin/snapshot-agent.exe install
```

On Windows that drops a hidden launcher in the Startup folder. On macOS and
Linux it prints the launchd plist or systemd unit to save. `uninstall` removes
it and leaves your config and queue alone.

## Tests

```
go test ./...
```

Covers `.gitignore` handling, tree-hash stability and sensitivity, line
counting, queue flush ordering and its 5000-entry cap, the activity gate's idle
boundary, and that a payload never contains a plaintext path.

# snapshot-agent

A small background program that records the **shape** of your code folders over
time — how many files, how long they are, what changed since last tick — and
POSTs that to a server you choose.

It never sends your code. File bytes are only ever hashed, and the file *names*
leave as salted hashes. What a collector sees is that `9d2e4c...` is 412 lines,
gained 6 of them at 2:15pm and shares fingerprints with some other file. It
never sees what the file is called or what's in it. The exact rules are in
[Privacy, precisely](#privacy-precisely).

Static Go binary, no CGO.

## Install in one command

On your flockatime dashboard, open **Connect the CLI** and press **Generate
install command**. It gives you one line with a fresh key in it:

```powershell
# Windows (PowerShell)
$env:FLOCKATIME_KEY='flk_...'; irm https://your-flockatime/install.ps1 | iex
```

```sh
# macOS / Linux
curl -fsSL https://your-flockatime/install.sh | FLOCKATIME_KEY=flk_... sh
```

That downloads the latest release from this repo, checks it against the
release's `SHA256SUMS`, puts it in `~/.flockatime/bin`, and runs
`snapshot-agent setup`, which:

1. checks the key with the server, before writing anything;
2. if WakaTime is installed, turns on `debug = true` in `~/.wakatime.cfg`, so
   the agent can see which file you are editing (nothing about your
   heartbeats changes);
3. writes `~/.snapshot-agent.toml` with `[activity] source = "wakatime"`, or
   `source = "ai"` when there is no WakaTime;
4. starts the agent now and at every login --- a hidden Startup item on
   Windows, a LaunchAgent on macOS, a systemd user unit on Linux.

From then on there is no project list. Whatever git repo you edit, or have
an AI agent (Claude Code, Codex) work in, is snapshotted while you work and
left alone when you stop. It uses the same name Hackatime shows (a
`.wakatime-project` file wins over the folder name). Hackatime is optional.
Without it, only AI sessions wake the agent, so coding you do entirely by
hand isn't captured until you install it and run the command again.

Run the same command again to upgrade or to switch keys.

## What it sends, and what it never sends

```json
[{
  "project": "flockatime-cli",
  "captured_at": 1788371950,
  "tree_hash": "f397336f865d...",
  "file_count": 28,
  "total_lines": 3800,
  "total_bytes": 120578,
  "skipped_files": 0,
  "git": {
    "head": "eb4cf53b...", "branch": "main", "dirty": true, "ahead": 0, "behind": 0,
    "staged": 0, "modified": 4, "untracked": 1, "conflicted": 0,
    "insertions": 222, "deletions": 33,
    "commits": 27, "last_commit_at": 1788370000, "stashes": 0, "ai_commits": 9
  },
  "files": [
    { "path_hash": "618cd5b8...", "content_hash": "52d9604e...", "lines": 10, "bytes": 298, "mtime": 1787687549,
      "added": 6, "removed": 1, "tail": 6, "mid": 1, "ai_lines": 7, "fp": [301247718934, 902811256601] }
  ],
  "capture_ms": 41,
  "os": "windows",
  "arch": "amd64",
  "agent_version": "0.3.0",
  "seq": 42,
  "prev": "a91c03d7e2b4...",
  "saves": [14, 31, 33, 95],
  "build_at": 1788371800,
  "test_at": 1788371500,
  "ai": {
    "lines": 2815, "files": 27,
    "writes": [{ "at": 1788371620, "tool": "claude-code", "lines": 40, "path_hash": "618cd5b8..." }],
    "minutes": [1788371580, 1788371640],
    "tools": { "claude-code": 40, "codex": 0 }
  }
}]
```

`tree_hash` is SHA-256 over the sorted `path_hash:content_hash` list — stable
across runs, different after a one-character edit or a rename.

The git block past `branch` is only counts: files staged, modified, untracked
or in conflict (from `git status`, paths discarded), uncommitted lines
added and removed against `HEAD` (`git diff --shortstat`), commits on `HEAD`,
the last commit's time, and stashes. `skipped_files` counts files left out for
being over 2 MB or unreadable. `capture_ms` is how long the walk and git took.

Each changed file also carries `added`, `removed`, `tail` and `mid`: lines
added and removed since the last snapshot, split by where they fall (`tail` is
the last ~10% of the file, `mid` everything before; `tail + mid = added +
removed`). They come from a Myers diff of per-line hashes kept in
`~/.flockatime/state/<project>.bin` --- 64-bit hashes only, never text, never
sent. A project's first snapshot is the baseline and has no counts; `once`
diffs against the saved state without updating it.

- **`seq` / `prev`:** each project's snapshots are numbered 1, 2, 3..., and
  `prev` is the `tree_hash` of the one before. A collector can check there
  are no gaps or forks, so forging a session means faking a whole consistent
  history, not one ping. The head lives in the state file. If that file is
  deleted or corrupt the chain restarts at 1, which `doctor` reports. The
  queue dropping its oldest entries past 5000 also leaves a gap.
- **`saves`:** seconds after the previous capture at which a known file's
  mtime moved. Between captures `run` stats the files of the last capture
  every 10 s, only while you're active, and never opens them. Several saves of
  one file between two polls count once.
- **`build_at` / `test_at`:** the newest mtime at the top of skipped build
  output (`dist/`, `target/`, an ignored `build/`, ...) and test caches
  (`.pytest_cache/`, an ignored `coverage/`, `.coverage`), read with stat
  only. Their contents are never walked.
- **`fp`:** up to 50 winnowed fingerprints per file (the MOSS technique).
  They are sent only for files whose content changed, or for every file on a
  project's first capture. Comments and whitespace are stripped, every
  30-token run is hashed, and the minimum hash of each window of 8 is kept.
  A partial copy survives reformatting and still shares fingerprints with its
  source. If fingerprints would push a snapshot past 1 MB they are dropped,
  least-changed files first. The file list itself is never cut.

### AI attribution

The agent reads the transcripts that AI coding agents already keep on disk
(Claude Code in `~/.claude/projects`, Codex in `~/.codex/sessions`). Every
file write an agent made is in them: Write/Edit/MultiEdit calls,
`apply_patch` patches, and shell heredocs (`cat > f <<EOF`). For each write
it keeps a hash of every line the agent produced, under the file it went
into. Each transcript is read once, from where the last read stopped. The
first read covers all your history and takes a few seconds.

- **`ai_lines`** (per file) counts lines that still match a line an agent
  wrote into *that* file. Matching ignores leading and trailing whitespace,
  so re-indenting a line keeps it attributed. Changing a line makes it the
  person's. Short generic lines (`}`, `end`, `)`) count only when they sit
  next to an attributed line, so your closing brace is never credited to an
  agent that wrote one somewhere else.
- **`ai.lines` / `ai.files`** total those for the project as captured.
- **`ai.writes`** are the agent writes since the last snapshot of this
  project: when, which tool, how many lines, which file (as a `path_hash`).
  At most 500 per snapshot, the latest kept.
- **`ai.minutes`** are the minutes (unix time, start of minute) in which an
  agent session was live in this repository, whether it was writing,
  reading, running commands or waiting on a prompt. Count them to get AI
  time. Compare them with `saves` and WakaTime time to see how much of a
  session the person drove.
- **`ai.tools`** gives lines written per tool. 0 means the tool was only in
  session here.
- **`git.ai_commits`** counts commits among the last 2000 whose author or
  message carries an AI marker (`Co-Authored-By: Claude`, aider's
  `(aider)`, Copilot, Cursor, Codex). Messages are scanned, never sent.

AI sessions also count as activity and name projects. A repository an agent
works in gets snapshotted without WakaTime and without a `[[project]]`
entry. Hackatime is optional now.

**What it can't see:** completions accepted inline in an editor (Copilot tab
completions, Cursor Tab), code pasted from a chat window, and agents whose
transcripts aren't read yet (Cursor's agent, Gemini CLI, aider). It also
can't see a transcript that was deleted before the agent read it. Under
`run` that window is a few seconds. A collector can still spot these the way
it spots pastes: a lot of `added` lines with few `saves` and no `ai.writes`.

### Privacy, precisely

**Never on the wire:** file contents, file names, folder names, keystrokes,
window titles, processes, screenshots, the clipboard. The only plaintext is the
project name, the git branch, and the commit SHA. AI attribution stays inside
this rule: it sends counts, times, tool names and path hashes.

- **Path hashes are salted.** Each `path_hash` is HMAC-SHA256 of the path,
  keyed with a secret that never leaves your machine: `path_salt` from the
  config, or a random one created once in `~/.flockatime/path-salt`. Unsalted,
  the hash of `src/main.rs` would be the same for everyone, and common names
  could be recovered from a dictionary. The trade-off is that two machines
  only produce matching path hashes if they share a `path_salt`. Upgrading
  from 0.2 changes every `path_hash` and `tree_hash` once.
- **Fingerprints are derived from content.** Building them means the file is
  tokenized in memory. The tokens are hashed and discarded, and nothing else
  is kept. A 30-token window can't be turned back into code. Anyone holding a
  *candidate* text can confirm a match, though, and that is what makes copy
  detection work.
- **Line hashes stay local.** The per-line hashes behind `added`/`removed`
  stay in `~/.flockatime/state` (mode 0600) and are never sent.
- **AI transcripts are read, not sent.** They hold your prompts and the
  agents' code, so reading them is a step past hashing file contents. What is
  kept from them, in `~/.flockatime/state/ai.gob` (mode 0600), is: transcript
  read offsets, per-file sets of line hashes, and an event log of
  times, tools, line counts, and local file and folder paths. Those paths
  never leave the machine; only their salted `path_hash` does. Turn it all
  off with `[ai] disabled = true`.
- **`reveal` maps a hash back to a name, on your machine only.** If a reviewer
  asks "what is `9d2e...`?", run `snapshot-agent reveal 9d2e4c18` to see which
  file it is. Nothing is sent, and whether to share the answer is up to you.

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
| `setup --endpoint URL --key KEY` | everything the install command does after downloading: check key, write config, enable WakaTime debug log, start at login |
| `once [dir]` | one snapshot, printed as JSON, sends nothing. With a dir it needs no config |
| `run` | the daemon: capture every interval, POST, queue to disk on failure |
| `doctor` | validate config, probe the endpoint, show whether the agent is running and polling, and per project the state file and fingerprint coverage. Sends nothing |
| `devserver [port]` | a local collector that prints what it receives. Default `127.0.0.1:8787` |
| `install` / `uninstall` | start (or stop) at login and now, hidden, no Administrator or sudo |
| `reveal HASH [--project N] [--dir P]` | show which local file a `path_hash` names. Sends nothing |
| `update [--check]` | install the newest GitHub release now (`run` also does this by itself) |
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
auto_update = true                         # default; false pins the installed version
path_salt = "..."                          # 32+ hex chars; share it to align path hashes across machines

# Optional: capture only while you're actually editing, using WakaTime's
# heartbeat files. Read-only — it cannot affect your WakaTime data.
[activity]
source = "wakatime"

# AI attribution is on by default. This turns it off, and stops AI sessions
# from waking the agent and naming projects.
# [ai]
# disabled = true

# Optional: WakaTime and AI sessions both find projects on their own.
# Required only with source = "always" and [ai] disabled.
[[project]]
name = "demo"
path = "~/src/demo"
```

With `source = "wakatime"` you don't list projects. WakaTime already knows which
file you're editing, so the agent snapshots that repository, wakes on your first
heartbeat, and goes dormant ten minutes after you stop. It needs `debug = true`
under `[settings]` in `~/.wakatime.cfg` to see which file you're in; `doctor`
says so if it's missing.

With `source = "ai"`, AI agent sessions alone decide when the agent is
active and which projects it captures. Use it on a machine without WakaTime.
With either source, AI sessions also wake the agent and add their projects
unless `[ai] disabled = true`.

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

## Installing from source

```
go build -o ~/bin/snapshot-agent.exe .    # somewhere permanent; the path is recorded
~/bin/snapshot-agent.exe setup --endpoint https://your-flockatime --key flk_...
```

`install` alone registers the login item for a config you wrote by hand: a
hidden launcher in the Startup folder on Windows, `~/Library/LaunchAgents/
com.snapshot-agent.plist` on macOS (log in `~/Library/Logs`), and a systemd user
unit on Linux (`journalctl --user -u snapshot-agent`). `uninstall` removes it
and leaves your config and queue alone.

## Releasing

Bump `agentVersion` in `main.go`, then push a matching tag:

```
git tag v0.2.0 && git push origin v0.2.0
```

The release workflow tests, builds every platform, and publishes archives with
version-less names (`snapshot-agent_windows_amd64.zip`, ...) plus `SHA256SUMS`,
which is what the install commands download.

Running agents pick a release up by themselves: `run` checks GitHub when it
starts and before captures (at most hourly), and if there is a newer `X.Y.Z` it
downloads this platform's archive, checks it against `SHA256SUMS`, runs the new
binary's `version` to confirm it, swaps it in place and restarts. Any failure
keeps the old binary. `snapshot-agent update` does the same by hand
(`--check` only reports); `auto_update = false` or `SNAPSHOT_AGENT_NO_UPDATE=1`
turns it off. A tag that isn't plain `X.Y.Z` (e.g. `v0.3.0-rc1`) is never
installed automatically.

## Tests

```
go test ./...
```

Covers `.gitignore` handling, tree-hash stability and sensitivity, line
counting, queue flush ordering and its 5000-entry cap, the activity gate's idle
boundary, and that a payload never contains a plaintext path.

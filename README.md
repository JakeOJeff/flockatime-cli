# snapshot-agent

A small background program that keeps a record of the **shape** of your code
folders over time — how many files there are, how long they are, and whether
anything changed since last time — and sends that record to a server you
choose.

It never reads your code. Not "reads it and promises to forget it" — the file's
bytes go straight into a fingerprint function and are thrown away, and even the
file *names* leave your machine as fingerprints rather than words.

---

## Contents

1. [What a "fingerprint" means here](#1-what-a-fingerprint-means-here)
2. [Try it in five minutes](#2-try-it-in-five-minutes)
3. [How it works, step by step](#3-how-it-works-step-by-step)
4. [Configuration](#4-configuration)
5. [Commands](#5-commands)
6. [What it sends](#6-what-it-sends)
7. [Running the tests](#7-running-the-tests)
8. [Project layout](#8-project-layout)

---

## 1. What a "fingerprint" means here

Everywhere below you'll see the word **hash**. A hash is a fixed-length string
of letters and numbers calculated from some input — here, SHA-256, which always
produces 64 characters.

Two things matter about it:

- The **same input always gives the same hash.** So if a file's hash is
  identical today and tomorrow, the file didn't change.
- You **cannot work backwards** from the hash to the input. `9d2e4c...` tells a
  server that *something* is there and whether it moved, but not what it says.

So when this program reports:

```
path_hash:    9d2e4c1b...   (instead of  src/billing/secrets.go)
content_hash: 7b1c8f30...   (instead of  the code inside that file)
```

the server can tell that a file exists, that it's 412 lines long, and that it
changed at 2:15pm — and cannot tell what it's called or what's in it.

---

## 2. Try it in five minutes

No server, no signup, no account. You need Go installed (`go version` should
print something) and two terminal windows.

### Step 1 — build it

```
go build -o snapshot-agent .
```

On Windows use `go build -o snapshot-agent.exe .` and add `.exe` to the
commands below.

### Step 2 — look at a snapshot without sending anything

Point it at any folder on your machine:

```
./snapshot-agent once .
```

You'll get JSON printed to the screen. **Read it closely** — this is the whole
point of the program. You will see line counts, byte sizes, and hashes. Search
it for one of your actual filenames. It isn't there. Nothing was sent anywhere;
`once` only prints.

### Step 3 — start a pretend server

The agent ships with a throwaway server so you can watch it work without
setting up a real one. In **terminal 1**:

```
./snapshot-agent devserver
```

It prints `devserver listening on http://127.0.0.1:8787` and then waits. Leave
it running.

### Step 4 — write a config file

Make a file called `agent.toml` in the same folder. Replace the two paths with
real ones on your machine:

```toml
endpoint = "http://127.0.0.1:8787"
api_key = "demo-key-abcd1234"
interval_seconds = 2
queue_path = "C:/Users/you/agent-queue.db"

[[project]]
name = "demo"
path = "C:/Users/you/some-project"
```

`interval_seconds = 2` is only for this demo — the real default is 300 (five
minutes).

> **Windows note:** in TOML, use forward slashes (`C:/Users/...`) or single
> quotes (`'C:\Users\...'`). A double-quoted string with backslashes will fail
> to parse, because TOML reads `\U` as an escape code.

### Step 5 — check the setup

In **terminal 2**:

```
SNAPSHOT_AGENT_CONFIG=./agent.toml ./snapshot-agent doctor
```

On Windows PowerShell:

```powershell
$env:SNAPSHOT_AGENT_CONFIG = ".\agent.toml"; .\snapshot-agent.exe doctor
```

You should see something like:

```
endpoint: http://127.0.0.1:8787/v1/snapshots
interval: 2s
api_key:  ****1234
reach:    ok
queue:    C:\Users\you\agent-queue.db (0 waiting)

projects:
  demo             C:\Users\you\some-project
      4 files, 10 lines, tree cb394448320e, master @ 39aa074f6b27 (dirty)
```

`reach: ok` means it found the server. `doctor` sends no snapshots — it only
looks.

### Step 6 — run it and watch

Still in terminal 2:

```
./snapshot-agent run
```

Terminal 2 reports what it captured; terminal 1 reports what arrived:

```
project "demo": 4 files, 10 lines, tree cb394448320e
sent 1 snapshot(s)
```

**Now try these three things and watch both windows:**

1. **Change nothing.** Every two seconds you'll see `unchanged`, and terminal 1
   shows a much smaller message — `307 bytes` instead of `1100 bytes`, with
   `unchanged (no file list)`. Still a heartbeat, no repetition.

2. **Edit one character** in any file in that folder and save. The `tree`
   fingerprint changes on the next tick and the full file list is sent again.
   Change the character back, and the fingerprint returns to its old value.

3. **Add a file to `.gitignore`.** It disappears from the file count.

### Step 7 — pull the plug (the interesting part)

Stop the pretend server in terminal 1 with `Ctrl+C`, and leave the agent
running. Terminal 2 starts saying:

```
send failed (... connection refused); queueing 1 snapshot(s)
```

Nothing is lost — it's being written to the queue file on disk. Let it collect
a few, then start `./snapshot-agent devserver` again in terminal 1. Within one
tick:

```
flushed 3 queued snapshot(s)
sent 4 snapshot(s)
```

And terminal 1 receives one batch of four, in the order they were taken:

```
--- batch of 4, 2811 bytes
  demo   captured_at=1787598181  ...
  demo   captured_at=1787598183  ...
  demo   captured_at=1787598185  ...
  demo   captured_at=1787598197  ...
```

Look at those timestamps: the first three are from *while the server was down*.
They kept the time they were actually taken. A gap in your network doesn't
become a gap in your history — and it doesn't become three snapshots all
falsely stamped "now" either.

Press `Ctrl+C` in both windows when you're done.

---

## 3. How it works, step by step

What follows is everything the program does, in the order it does it.

### Step 1 — Read the config

On startup it opens `~/.snapshot-agent.toml` (or whatever
`SNAPSHOT_AGENT_CONFIG` points at). It then checks the file's permissions: if
anyone other than the owner can read it, **the agent refuses to start**. The
file holds an API key, and a key readable by every account on a shared machine
isn't a secret. The fix it suggests is `chmod 600 ~/.snapshot-agent.toml`.

*(This check is skipped on Windows, where those permission bits are simulated
and always look the same. Windows uses ACLs instead, which this version does
not inspect.)*

It also checks that every project folder exists, that names aren't duplicated,
and expands `~` into your home directory.

### Step 2 — Walk the folder

Every interval, for each project, it walks the folder tree top-down and decides
what to record. Things are left out for four reasons:

| Reason | Examples |
|---|---|
| Always skipped by name | `.git`, `node_modules`, `target`, `dist`, `vendor` |
| Listed in a `.gitignore` | whatever your repo already ignores |
| Bigger than 2 MB | videos, datasets, compiled binaries |
| Not a normal file | symlinks, sockets, devices |

`.gitignore` files are handled the way git handles them: a `.gitignore` inside
`src/` applies only to things under `src/`, and rules from parent folders still
apply to children. An ignored folder is skipped whole — the agent doesn't
descend into it at all.

### Step 3 — Fingerprint each file

For every file that survives Step 2, it opens the file and streams it through
the hasher in 32 KB chunks. As each chunk passes, two things happen: it's fed
into SHA-256, and its newline characters are counted.

This is the part the whole design turns on. The bytes are never assembled into
a string, never stored, never logged, never held after the chunk is processed.
The buffer is reused for the next chunk. There is no code path in this program
that can produce your file's contents, because nothing ever keeps them.

It records: the content hash, the line count, the size in bytes, the
modification time, and a hash of the file's path relative to the project root.
The path is normalized to forward slashes first, so the same repository
fingerprints identically on Windows and on Linux.

### Step 4 — Fingerprint the whole tree

It builds one line per file reading `path_hash:content_hash`, **sorts those
lines**, and hashes the sorted list. That's the `tree_hash`.

Sorting is what makes it dependable. Filesystems can hand back entries in
different orders on different machines; sorting means the same set of files
always produces the same tree hash regardless. So:

- Same code, run twice → **same tree hash**.
- One character edited anywhere → **different tree hash**.
- A file renamed but not edited → **different tree hash** (because path hashes
  feed into it too).

### Step 5 — Read the git state

If the folder has a `.git`, the agent runs four ordinary `git` commands and
keeps four small facts: the current commit, the branch name, whether there are
uncommitted changes, and how many commits you are ahead of the upstream branch.

It runs `git` as a subprocess rather than embedding a git library, because
these four questions don't justify the dependency. Two safeguards: git is run
with prompts disabled so it can never sit waiting for a password, and every
call is killed after 10 seconds so a stuck repository can't freeze the daemon.
`git status --porcelain` does print changed filenames — the agent only checks
whether that output was empty, and discards it.

### Step 6 — Skip the repetition, keep the heartbeat

If the tree hash matches the last snapshot for that project, nothing about your
code changed. The agent still sends a snapshot, but with an empty file list and
`"unchanged": true`.

This is deliberate. Sending nothing would leave a hole in the timeline that
looks identical to the agent being switched off or the laptop being closed.
Sending the whole file list again would waste bandwidth restating a fact. The
unchanged marker says "still here, still identical" in about a quarter of the
bytes.

### Step 7 — Send it

Snapshots are POSTed to `{endpoint}/v1/snapshots` with an
`Authorization: Bearer {api_key}` header. The body is always a JSON **array**,
even for a single snapshot, because a backlog and the current tick often travel
together. Any 2xx response counts as success; anything else is treated as a
failure worth retrying.

### Step 8 — When sending fails

If the POST fails for any reason — no wifi, server restarting, laptop on a
train — the snapshot is written to a local database file (BoltDB) instead of
being dropped.

- Entries are stored under an ever-increasing number, so reading them back in
  key order returns them **oldest first**.
- On the next successful tick, the backlog goes out **ahead of** the fresh
  snapshot, in order, up to 200 per request.
- Entries are deleted only after the server has accepted them. If the agent
  crashes mid-send, the snapshot is still on disk and goes out next time.
- `captured_at` is written once, at capture, and **never rewritten**. A
  snapshot delivered an hour late still says when it was taken.
- The queue holds 5,000 snapshots. Past that, the oldest are dropped — at a
  five-minute interval that's about two and a half weeks offline before
  anything is lost.

---

## 4. Configuration

Location: `~/.snapshot-agent.toml`, or the path in `SNAPSHOT_AGENT_CONFIG`.
Must not be readable by anyone but you (`chmod 600`).

```toml
# Where snapshots are sent. "/v1/snapshots" is appended automatically.
endpoint = "https://collect.example.com"

# Sent as: Authorization: Bearer <api_key>
api_key = "..."

# How often to capture, in seconds. Default 300.
interval_seconds = 300

# Where undelivered snapshots wait. Default ~/.snapshot-agent-queue.db
queue_path = "~/.snapshot-agent-queue.db"

# One block per folder you want tracked. Repeat as needed.
[[project]]
name = "strict"
path = "~/src/strict"

[[project]]
name = "flockatime"
path = "D:/products/flockatime"
```

---

## 5. Commands

| Command | What it does |
|---|---|
| `snapshot-agent once` | Capture every configured project once, print the JSON, **send nothing**. |
| `snapshot-agent once <dir>` | Same, for one folder, ignoring the config entirely. The fastest way to see what would be sent. |
| `snapshot-agent run` | The daemon: capture every interval, send, queue on failure. `Ctrl+C` stops it cleanly. |
| `snapshot-agent doctor` | Check the config, probe the endpoint, print each project's resolved path and file count, and report how many snapshots are waiting in the queue. Sends nothing. |
| `snapshot-agent devserver [port]` | A local endpoint that prints what it receives and stores nothing. For trying the agent out. |
| `snapshot-agent version` | Print the version. |

---

## 6. What it sends

```json
[{
  "project": "strict",
  "captured_at": 1756039182,
  "tree_hash": "a3f9...",
  "file_count": 42,
  "total_lines": 3812,
  "git": { "head": "c4d1...", "branch": "main", "dirty": true, "ahead": 3 },
  "files": [
    { "path_hash": "9d2e...", "content_hash": "7b1c...", "lines": 412, "bytes": 10233, "mtime": 1756039100 }
  ],
  "agent_version": "0.1.0"
}]
```

When nothing changed, `files` is `[]` and `"unchanged": true` is present.
`git` is absent for folders that aren't repositories.

**What is never in there, by design:** file contents, file names, folder names
below the project root, keystrokes, window titles, running processes,
screenshots, the clipboard, or the timing of individual edits. The only plain
text on the wire is the project names you chose yourself, the branch name, and
the commit hash.

---

## 7. Running the tests

```
go test ./...
```

The suite covers the behaviours that would be easiest to get quietly wrong:

- **`.gitignore` is respected**, including nested ignore files that apply only
  to their own subtree, and the always-skipped folders.
- **Files over 2 MB are skipped.**
- **Path hashes are never plain text.**
- **The tree hash is stable** across repeated runs of the same unchanged
  folder, and **independent of walk order**.
- **The tree hash changes** on a single-character edit, and on a rename with no
  edit at all.
- **Line counting** handles empty files, missing trailing newlines, and blank
  lines.
- **The queue flushes oldest-first**, doesn't delete anything until the server
  accepts it, removes only the part that was actually sent, preserves
  `captured_at` across the outage, and drops the oldest entries at 5,000.

`go test -short ./...` skips the 5,000-entry queue test, which takes a few
seconds.

---

## 8. Project layout

```
.
├── main.go                     command dispatch
├── commands.go                 once / run / doctor, and the daemon tick
├── devserver.go                the throwaway local endpoint
└── internal/
    ├── config/config.go        TOML config, permission check, path expansion
    ├── snapshot/
    │   ├── snapshot.go         payload types, tree hash, capture
    │   ├── walk.go             tree walk, .gitignore, skip lists, size cap
    │   └── hash.go             streaming SHA-256 and line counting
    ├── gitstate/gitstate.go    the four git facts, read by subprocess
    ├── transport/client.go     POST /v1/snapshots
    └── queue/queue.go          the offline backlog
```

Static binary, no CGO. Dependencies: a TOML parser, a `.gitignore` matcher, and
BoltDB.

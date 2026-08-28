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
3. [Testing the WakaTime activity gate](#3-testing-the-wakatime-activity-gate)
4. [Setting it up so nobody has to run it](#4-setting-it-up-so-nobody-has-to-run-it)
5. [How it works, step by step](#5-how-it-works-step-by-step)
6. [Configuration](#6-configuration)
7. [Commands](#7-commands)
8. [What it sends](#8-what-it-sends)
9. [Running the tests](#9-running-the-tests)
10. [Project layout](#10-project-layout)

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

## 3. Testing the WakaTime activity gate

By default the agent captures every interval, forever. Set `source = "wakatime"`
and it only captures **while you are actually editing** — it wakes on your first
heartbeat, snapshots every interval while you work, goes quiet after you stop,
and wakes again the next time you type.

It does this by reading the modification time of the two files `wakatime-cli`
rewrites on every heartbeat:

```
~/.wakatime/wakatime-internal.cfg
~/.wakatime/offline_heartbeats.bdb
```

It **only ever `stat`s them.** It never opens them, never writes to them, and
never talks to the WakaTime or Hackatime API. Nothing it does can delay, alter
or drop a heartbeat, so your coding time is recorded exactly as it would be if
this agent weren't installed.

> Both files are watched, not just the first. When the server is unreachable,
> `wakatime-cli` banks heartbeats in the offline queue instead of sending them.
> If only `wakatime-internal.cfg` were watched, an outage would look exactly
> like you walking away from the keyboard.

### The trick: don't wait ten minutes

At the real defaults, going dormant takes `2 × 300s` = **ten minutes** of
silence. Nobody wants to test that. Two changes make the whole cycle run in
about a minute:

- **A tiny interval.** `interval_seconds = 10` makes dormancy arrive after 20s.
- **A fake WakaTime folder.** Point `wakatime_dir` at an empty directory of your
  own. Now `touch`ing a file in it *is* a heartbeat, and you control the clock
  instead of your editor. Your real `~/.wakatime` is never touched.

### Step 1 — a throwaway heartbeat folder

```
mkdir fakewaka
```

Leave it empty for now. Empty means "no heartbeat ever" — the agent should
start dormant and stay silent.

> **PowerShell users:** there is no `touch`, and the walkthrough below fakes a
> heartbeat four times. Define this once now and use `beat` wherever the steps
> say `touch fakewaka/wakatime-internal.cfg`:
>
> ```powershell
> function beat {
>   $f = 'fakewaka\wakatime-internal.cfg'
>   if (-not (Test-Path $f)) { New-Item -ItemType File $f | Out-Null }
>   (Get-Item $f).LastWriteTime = Get-Date
> }
> ```
>
> It has to handle both jobs `touch` does: **create** the file the first time,
> and **bump the modification time** every time after. Don't reach for
> `New-Item -Force` as a shortcut — on an existing file it truncates rather
> than touching it. Only the timestamp matters here; the agent never reads a
> byte of this file.

### Step 2 — a fast config

Save this as `loop.toml`, adjusting the two paths:

```toml
endpoint = "http://127.0.0.1:8787"
api_key = "demo-key-abcd1234"
interval_seconds = 10
queue_path = "./loop-queue.db"

[activity]
source          = "wakatime"
idle_multiplier = 2            # dormant after 2 x 10s = 20s of silence
poll_seconds    = 2            # check for a heartbeat every 2s
wakatime_dir    = "./fakewaka"

[[project]]
name = "demo"
path = "C:/Users/you/some-project"
```

`poll_seconds` is how often it checks *whether* to capture — cheap, one `stat`.
`interval_seconds` is how often it actually captures. They're separate on
purpose: a small poll means you get picked up seconds after returning, without
paying for a folder walk every two seconds.

### Step 3 — start the server and the agent

Terminal 1:

```
./snapshot-agent devserver
```

Terminal 2:

```
SNAPSHOT_AGENT_CONFIG=./loop.toml ./snapshot-agent run
```

It should announce itself and then **go quiet**:

```
activity: wakatime heartbeats in C:\Users\you\demo\fakewaka --- dormant after 20s quiet, checked every 2s
dormant --- no activity in the last 20s; waiting for a heartbeat
```

Terminal 1 stays empty. This is the whole point: a machine that's logged in but
not being coded on sends nothing at all.

### Step 4 — fake a heartbeat

In a third terminal (or terminal 2 after `Ctrl+C`-ing nothing — just open a new
one):

```
touch fakewaka/wakatime-internal.cfg
```

```powershell
beat
```

Within `poll_seconds`, terminal 2 wakes up and captures **immediately** rather
than waiting for the next interval:

```
waking --- activity detected
project "demo": 4 files, 10 lines, tree cb394448320e
sent 1 snapshot(s)
```

That immediacy matters. If it waited for the next tick, every session would
begin with up to a full interval of missing history.

### Step 5 — keep "coding"

Run that `touch` every few seconds:

```
for i in 1 2 3 4 5 6 7 8; do touch fakewaka/wakatime-internal.cfg; sleep 3; done
```

```powershell
1..8 | ForEach-Object { beat; Start-Sleep -Seconds 3 }
```

Terminal 2 now snapshots every 10 seconds, exactly as it would with the gate
turned off. Heartbeats keep it awake; they don't trigger individual snapshots.

### Step 6 — walk away

Stop touching the file and watch. Two more snapshots arrive — at +10s and +20s
— and then:

```
going dormant --- no activity for 20s
project "demo": unchanged (4 files)
sent 1 snapshot(s)
```

Then silence, indefinitely.

Those two trailing snapshots are **not a bug.** A 2× idle window means the
agent stays awake for two full intervals after your last keystroke, so the tail
end of a session is recorded rather than cut off mid-thought. The final one,
sent as it goes dormant, is a deliberate end-of-session marker: your collector
sees a clean close instead of a log that simply stops.

### Step 7 — come back

```
touch fakewaka/wakatime-internal.cfg
```

```powershell
beat
```

Within `poll_seconds`, `waking --- activity detected` again. The cycle is a
loop, not a one-shot.

### The whole thing, on one clock

Here is a real run at `interval_seconds = 10`, so `idle_after` is 20s:

```
[20:33:02] dormant --- no activity in the last 20s; waiting for a heartbeat
             (18 seconds of complete silence)
[20:33:18] * first heartbeat
[20:33:20] waking --- activity detected          <- 2s later, one poll
[20:33:20] 4 files, tree cb394448320e -> sent
[20:33:30] sent
[20:33:40] sent                                  <- every interval while active
[20:33:39] * last heartbeat
[20:33:50] sent                                  <- tail snapshot 1
[20:34:00] going dormant --- no activity for 20s <- tail snapshot 2, then quiet
[20:34:10] * heartbeat
[20:34:12] waking --- activity detected          <- 2s later
```

### Checking it against your real WakaTime

`doctor` reads the real files and tells you which state `run` would start in —
without sending anything, and without modifying a single byte of WakaTime state:

```
SNAPSHOT_AGENT_CONFIG=./agent.toml ./snapshot-agent doctor
```

```
activity: wakatime heartbeats in C:\Users\you\.wakatime (dormant after 10m0s quiet)
          last activity 43m1s ago --- would start dormant
```

Type something in your editor, wait for your plugin to send a heartbeat
(`heartbeat_rate_limit_seconds` in `~/.wakatime.cfg` controls how often — 30 to
120 seconds is typical), then run `doctor` again. It should flip to:

```
          last activity 12s ago --- would start ACTIVE
```

If it stays dormant while you're clearly typing, check in this order:

| Symptom | Likely cause |
|---|---|
| `activity: INVALID --- ...no such file` | `wakatime_dir` is wrong, or WakaTime was never installed |
| `no activity signal found` | The folder exists but holds neither heartbeat file — your plugin hasn't run yet |
| `last activity` never advances | Your editor plugin isn't firing at all; test it independently of this agent |
| Flips to dormant mid-session | `idle_multiplier × interval_seconds` is shorter than your natural pauses — raise the multiplier |

### Turning the gate off

Delete the `[activity]` block, or set `source = "always"`. The agent goes back
to capturing every interval regardless of what you're doing — which is the
right choice on a build server, where nobody is typing but you still want a
record.

---

## 4. Setting it up so nobody has to run it

Everything so far assumed you type `snapshot-agent run` yourself and list every
project by hand. Neither is necessary.

Worth being honest about the goal. WakaTime isn't magic either — somebody
installed the editor plugin once, and *that* is what runs `wakatime-cli`. The
target here is the same: **one command, once, then never again.** There is no
version of this where nothing is ever installed.

### Start at login

```
snapshot-agent install
```

```
installed: starts at every login, hidden, no Administrator needed
  binary:   C:\Users\you\bin\snapshot-agent.exe
  login item: C:\Users\you\AppData\Roaming\Microsoft\Windows\Start Menu\Programs\Startup\snapshot-agent.vbs
```

On Windows this writes a one-line launcher into your Startup folder. It uses
the Startup folder rather than a scheduled task on purpose: `schtasks
/sc onlogon` **requires Administrator**, and a program that watches your own
folders has no business asking for that. The launcher exists because the agent
is a console program — started directly it would leave a terminal window open
for as long as it runs, so the script starts it with the window hidden instead.

On macOS and Linux, `install` prints the launchd plist or systemd user unit to
save, because guessing at someone's init system is worse than showing them the
file.

Undo it at any time:

```
snapshot-agent uninstall
```

That removes the login item only. Your config, your queue, and any agent
already running are left alone.

> **A login item has no window, so it has nowhere to print.** If the config is
> broken, the agent exits at login and you see nothing at all. `install` checks
> the config first and warns you before that can happen, but if the agent ever
> seems to be doing nothing, `snapshot-agent doctor` is the thing to run.

### Stop listing projects by hand

You don't configure projects. WakaTime already knows which file you're editing,
so the agent reads that and snapshots the repository the file is in:

```toml
[activity]
source = "wakatime"
```

That's the whole configuration. No roots, no folder scanning, no `[[project]]`
entries. Open any repository on any drive and it's tracked; the agent has never
heard of it and doesn't need to.

```
projects (0 pinned, 2 from WakaTime):
  nomeadow         D:\products\nomeadow
      72 files, 23797 lines, tree 6a0f60311a6d, master @ 31d85c70d5ca (dirty)
  flockatime-cli   D:\products\flockatime-cli
      29 files, 3653 lines, tree f8678354d9c9, main @ bc597d0c820c (dirty)
```

Both of those were found because they were edited. Neither was configured.

This is also why the cost doesn't grow with how much code you own. The agent
snapshots the repository you're **in**, not every repository it can find — so
having 77 checkouts on a drive costs exactly the same as having one.

`[[project]]` entries still work if you want something watched whether or not
you touch it, and a pinned entry always wins over a detected one with the same
path.

### The one thing to turn on

Projects come from `~/.wakatime/wakatime.log`, and `wakatime-cli` only records
the file you're editing when debug logging is on. Add this to `~/.wakatime.cfg`
under `[settings]`:

```ini
debug = true
```

That changes only what WakaTime writes **locally**. Heartbeats, your coding
time, and your dashboard are all unaffected — the agent reads the file and
never writes to it. The cost is a busier log: roughly five lines per heartbeat.

`doctor` tells you when this is the problem rather than leaving you guessing:

```
projects (0 pinned, 0 from WakaTime):
  none yet.

  Projects are read from C:\Users\you\.wakatime\wakatime.log, which
  only records the file you are editing when debug logging is on.

  Add this to ~/.wakatime.cfg under [settings]:

      debug = true
```

> **A repository at your home directory is never treated as a project.** Keeping
> dotfiles in a repo at `~` is common, and without that rule, editing any loose
> file — a download, a scratch note — would resolve to your home directory and
> set the agent walking and hashing everything you own. Repositories *nested*
> under home are tracked normally.

### The whole zero-touch setup

The entire config:

```toml
endpoint = "https://collect.example.com"
api_key  = "..."

[activity]
source = "wakatime"
```

Plus `debug = true` in `~/.wakatime.cfg`, and once:

```
snapshot-agent install
```

From then on: log in, open a project, start typing. The agent wakes on your
first heartbeat, snapshots whichever repository you're in — one it has never
seen before is no different from one you use daily — every five minutes while
you work, and goes quiet ten minutes after you stop.

Everything about *when* and *what* comes from WakaTime. The only thing this
agent adds is the snapshot itself, and sending it somewhere else.

---

## 5. How it works, step by step

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

## 6. Configuration

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

# Optional. Omit this whole block and the agent captures every interval,
# forever, regardless of whether anyone is at the keyboard.
#
# source = "wakatime" gates capture on editor activity, read from the files
# wakatime-cli rewrites on each heartbeat. Read-only: it cannot affect your
# WakaTime or Hackatime data. See section 3.
[activity]
source          = "wakatime"   # "wakatime" or "always" (default)
idle_multiplier = 2            # dormant after 2 x interval_seconds of silence
poll_seconds    = 30           # how often to check for a heartbeat
wakatime_dir    = "~/.wakatime"

# One block per folder you want tracked. Repeat as needed.
[[project]]
name = "strict"
path = "~/src/strict"

[[project]]
name = "flockatime"
path = "D:/products/flockatime"
```

---

## 7. Commands

| Command | What it does |
|---|---|
| `snapshot-agent once` | Capture every configured project once, print the JSON, **send nothing**. |
| `snapshot-agent once <dir>` | Same, for one folder, ignoring the config entirely. The fastest way to see what would be sent. |
| `snapshot-agent run` | The daemon: capture every interval, send, queue on failure. With `[activity]` set to `wakatime`, it captures only while you're editing and goes dormant otherwise. `Ctrl+C` stops it cleanly. |
| `snapshot-agent doctor` | Check the config, probe the endpoint, print each project's resolved path and file count, report how many snapshots are waiting in the queue, and say which activity state `run` would start in. Sends nothing. |
| `snapshot-agent install` | Register the agent to start at every login, hidden, without Administrator. Warns first if the config would stop it starting. |
| `snapshot-agent uninstall` | Remove that login entry. Leaves the config, the queue, and any running agent alone. |
| `snapshot-agent devserver [port]` | A local endpoint that prints what it receives and stores nothing. For trying the agent out. |
| `snapshot-agent version` | Print the version. |

---

## 8. What it sends

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

## 9. Running the tests

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
- **Project detection** resolves an edited file to its repository root, collapses
  many files in one repo to a single project, ignores heartbeats older than the
  idle window, skips files outside any repository, reads only the tail of a long
  log, and **never treats the home directory as a project** even when it is a
  git repo.
- **The activity gate reads the newest heartbeat file**, counts a heartbeat
  banked in WakaTime's offline queue as activity (so a WakaTime outage isn't
  mistaken for you leaving), treats an unreadable or missing signal as idle
  rather than as "always awake", and wakes on the exact idle boundary but not
  one second past it.

`go test -short ./...` skips the 5,000-entry queue test, which takes a few
seconds.

---

## 10. Project layout

```
.
├── main.go                     command dispatch
├── commands.go                 once / run / doctor, and the daemon tick
├── devserver.go                the throwaway local endpoint
├── install.go                  register the agent to start at login
└── internal/
    ├── config/config.go        TOML config, permission check, path expansion
    ├── activity/activity.go    when to capture: WakaTime heartbeats, or always
    ├── activity/projects.go    what to capture: the repo WakaTime saw you edit
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

Build a Go CLI called `snapshot-agent`. Static binary, no CGO.

## What it does
Periodically records the *shape* of a project directory — file paths,
line counts, content hashes — and POSTs them to a collection endpoint.
It never reads, transmits, or logs file contents.

## Core loop
- Read config from `~/.snapshot-agent.toml`:
  `endpoint`, `api_key`, `interval_seconds` (default 300), and a list
  of `[[project]]` entries each with `name` and `path`.
- Every interval, for each project: walk the tree, respecting
  `.gitignore` (use go-git's gitignore matcher or github.com/sabhiram/go-gitignore).
  Always skip `.git/`, `node_modules/`, `target/`, `dist/`, `vendor/`,
  and any file over 2 MB.
- For each file record: SHA-256 of contents (hash only, discard the bytes),
  line count, size, mtime, and SHA-256 of the path relative to project root.
  Store the path hash, NOT the path.
- Compute a tree hash: SHA-256 over the sorted list of `path_hash:content_hash`.
- Read git state if `.git` exists: current HEAD sha, branch, dirty flag,
  commits ahead of upstream. Shell out to `git`; don't vendor a git library
  for this.

## Payload
POST JSON to `{endpoint}/v1/snapshots`, `Authorization: Bearer {api_key}`.
Send as an array — the offline queue means several may go at once.

```json
[{
  "project": "strict",
  "captured_at": 1756039182,
  "tree_hash": "a3f9...",
  "file_count": 42,
  "total_lines": 3812,
  "git": { "head": "c4d1...", "branch": "main", "dirty": true, "ahead": 3 },
  "files": [
    { "path_hash": "9d2e...", "content_hash": "7b1c...", "lines": 412, "bytes": 10233 }
  ],
  "agent_version": "0.1.0"
}]
```

## Offline queue
If the POST fails, persist the snapshot to a local BoltDB or SQLite file
and retry on the next tick. Flush the backlog oldest-first once a POST
succeeds. Cap the queue at 5000 snapshots, dropping oldest. Snapshots
carry their original `captured_at` — never rewrite it on flush.

## Skip unchanged
If the tree hash matches the previous snapshot for that project, still
send, but with an empty `files` array and a `"unchanged": true` field.
Keeps the timeline dense without repeating the file list every tick.

## Commands
- `snapshot-agent run` — the daemon loop
- `snapshot-agent once` — one snapshot, print JSON to stdout, don't send
- `snapshot-agent doctor` — validate config, check endpoint reachability,
  print resolved project paths and file counts

## Hard constraints
- Never read a file's bytes into anything but the hasher.
- Never log a file path, only path hashes.
- No keystroke capture, no window titles, no process lists, no screenshots.
- Config file mode 0600; refuse to start if it's world-readable.
- The whole thing should be under ~800 lines.

## Deliverables
- Working `once` command first, verified by hand on a real repo
- Then the daemon and queue
- `go test` covering: gitignore handling, tree-hash stability across runs,
  tree-hash change on single-character edit, queue flush ordering

Start with `once`. Show me the file layout before writing code.
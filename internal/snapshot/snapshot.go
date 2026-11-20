// Package snapshot captures the shape of a project tree. It records hashes,
// counts, and sizes --- never file contents, never plaintext paths.
package snapshot

import (
	"crypto/sha256"
	"encoding/hex"
	"runtime"
	"sort"
	"time"

	"snapshot-agent/internal/gitstate"
)

// Snapshot is one capture of one project, and is the unit of the wire payload.
type Snapshot struct {
	Project    string `json:"project"`
	CapturedAt int64  `json:"captured_at"`
	TreeHash   string `json:"tree_hash"`
	FileCount  int    `json:"file_count"`
	TotalLines int    `json:"total_lines"`
	TotalBytes int64  `json:"total_bytes"`
	// SkippedFiles were left out for being over the size cap or unreadable;
	// .gitignore'd files and the skip-listed folders are not counted.
	SkippedFiles int    `json:"skipped_files"`
	Git          *Git   `json:"git,omitempty"`
	Files        []File `json:"files"`
	Unchanged    bool   `json:"unchanged,omitempty"`
	// CaptureMs is how long the walk and git took, so a collector can see a
	// project getting too big to snapshot cheaply.
	CaptureMs    int64  `json:"capture_ms"`
	OS           string `json:"os"`
	Arch         string `json:"arch"`
	AgentVersion string `json:"agent_version"`

	// Seq numbers a project's snapshots 1, 2, 3...; PrevTreeHash is the tree
	// hash of seq-1. Together they let a collector check the history has no
	// gaps and no forks. Both persist in the line state file.
	Seq          uint64 `json:"seq,omitempty"`
	PrevTreeHash string `json:"prev,omitempty"`

	// SaveTimes are seconds after the previous capture at which a known file's
	// mtime moved, from a stat-only poll between captures.
	SaveTimes []int32 `json:"saves,omitempty"`

	// BuildTouchedAt and TestTouchedAt are the newest mtimes seen at the top of
	// build output (dist/, target/, build/...) and test caches
	// (.pytest_cache/, coverage/...). Their contents are never walked.
	BuildTouchedAt int64 `json:"build_at,omitempty"`
	TestTouchedAt  int64 `json:"test_at,omitempty"`

	// AI is what AI coding agents contributed, from their transcripts (see
	// internal/aitrace). Absent when no agent has touched the project.
	AI *AI `json:"ai,omitempty"`
}

// AI is a project's AI attribution and the AI activity since the previous
// snapshot. Lines and Files describe the tree as captured; Writes and Minutes
// are new since the last snapshot sent for this project.
type AI struct {
	Lines   int            `json:"lines"`             // lines that still match what an agent wrote
	Files   int            `json:"files"`             // files with any such line
	Writes  []AIWrite      `json:"writes,omitempty"`  // agent file writes, oldest first
	Minutes []int64        `json:"minutes,omitempty"` // unix minute starts an AI session was live here
	Tools   map[string]int `json:"tools,omitempty"`   // lines written per tool (0: session only)
}

// AIWrite is one agent writing into one file.
type AIWrite struct {
	At       int64  `json:"at"`
	Tool     string `json:"tool"`
	Lines    int    `json:"lines"`
	PathHash string `json:"path_hash"`
}

// File is one recorded file. PathHash holds a hash of the project-relative
// path, never the path itself.
type File struct {
	PathHash    string `json:"path_hash"`
	ContentHash string `json:"content_hash"`
	Lines       int    `json:"lines"`
	Bytes       int64  `json:"bytes"`
	ModTime     int64  `json:"mtime"`
	// Line-level change since the previous snapshot, filled in by an Observer
	// (see internal/linestate). Zero on a project's first capture.
	Added     int `json:"added,omitempty"`
	Removed   int `json:"removed,omitempty"`
	TailEdits int `json:"tail,omitempty"` // changed lines in the last ~10% of the file
	MidEdits  int `json:"mid,omitempty"`
	// AILines is how many of the file's lines match lines an AI agent wrote
	// into it and nobody has changed since.
	AILines int `json:"ai_lines,omitempty"`
	// Fingerprints are winnowed k-gram hashes of the normalized source (see
	// internal/fingerprint), sent only for files whose content changed.
	Fingerprints []uint64 `json:"fp,omitempty"`
}

// Observer sees each recorded file during the walk, with its absolute path,
// and may fill in fields of f. The path is handed over only here and is never
// stored on File, so it cannot reach the payload or a log line by accident.
type Observer interface {
	Observe(absPath string, f *File)
}

// Git is the repository state at capture time, absent for non-repo projects.
// Past branch it is only counts and a time; see gitstate.State.
type Git struct {
	Head         string `json:"head"`
	Branch       string `json:"branch"`
	Dirty        bool   `json:"dirty"`
	Ahead        int    `json:"ahead"`
	Behind       int    `json:"behind"`
	Staged       int    `json:"staged"`
	Modified     int    `json:"modified"`
	Untracked    int    `json:"untracked"`
	Conflicted   int    `json:"conflicted"`
	Insertions   int    `json:"insertions"`
	Deletions    int    `json:"deletions"`
	Commits      int    `json:"commits"`
	LastCommitAt int64  `json:"last_commit_at,omitempty"`
	Stashes      int    `json:"stashes"`
	// AICommits counts recent commits whose message or author names an AI
	// agent (Co-Authored-By: Claude, aider, Copilot...).
	AICommits int `json:"ai_commits,omitempty"`
}

// TreeHash is SHA-256 over the sorted "path_hash:content_hash" lines. Sorting
// makes it independent of walk order, so it is stable across runs and changes
// on any single-character edit.
func TreeHash(files []File) string {
	lines := make([]string, 0, len(files))
	for _, f := range files {
		lines = append(lines, f.PathHash+":"+f.ContentHash)
	}
	sort.Strings(lines)

	h := sha256.New()
	for _, l := range lines {
		h.Write([]byte(l))
		h.Write([]byte{'\n'})
	}
	return hex.EncodeToString(h.Sum(nil))
}

// Capture walks root and returns the snapshot for the named project.
func Capture(name, root, agentVersion string) (*Snapshot, error) {
	return CaptureWith(name, root, agentVersion, Options{})
}

// Options tune a capture. The zero value is a plain unsalted capture.
type Options struct {
	Observer Observer // may be nil
	PathSalt []byte   // nil hashes paths unsalted; see HashPath
}

// CaptureWith is Capture with Options.
func CaptureWith(name, root, agentVersion string, opt Options) (*Snapshot, error) {
	start := time.Now()
	res, err := walk(root, opt)
	if err != nil {
		return nil, err
	}
	files, skipped := res.files, res.skipped
	total := 0
	var bytes int64
	for _, f := range files {
		total += f.Lines
		bytes += f.Bytes
	}

	s := &Snapshot{
		Project:        name,
		CapturedAt:     start.Unix(),
		TreeHash:       TreeHash(files),
		FileCount:      len(files),
		TotalLines:     total,
		TotalBytes:     bytes,
		SkippedFiles:   skipped,
		BuildTouchedAt: res.buildAt,
		TestTouchedAt:  res.testAt,
		Files:          files,
		OS:             runtime.GOOS,
		Arch:           runtime.GOARCH,
		AgentVersion:   agentVersion,
	}

	if g, err := gitstate.Read(root); err == nil && g != nil {
		s.Git = &Git{
			Head: g.Head, Branch: g.Branch, Dirty: g.Dirty, Ahead: g.Ahead, Behind: g.Behind,
			Staged: g.Staged, Modified: g.Modified, Untracked: g.Untracked, Conflicted: g.Conflicted,
			Insertions: g.Insertions, Deletions: g.Deletions,
			Commits: g.Commits, LastCommitAt: g.LastCommitAt, Stashes: g.Stashes,
			AICommits: g.AICommits,
		}
	}
	s.CaptureMs = time.Since(start).Milliseconds()
	return s, nil
}

// AsUnchanged returns a copy that keeps the counts but drops the file list,
// which is what goes on the wire when the tree hash has not moved.
func (s *Snapshot) AsUnchanged() Snapshot {
	c := *s
	c.Files = []File{}
	c.Unchanged = true
	return c
}

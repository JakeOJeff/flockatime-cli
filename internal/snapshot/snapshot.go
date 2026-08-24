// Package snapshot captures the shape of a project tree. It records hashes,
// counts, and sizes --- never file contents, never plaintext paths.
package snapshot

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"time"

	"snapshot-agent/internal/gitstate"
)

// Snapshot is one capture of one project, and is the unit of the wire payload.
type Snapshot struct {
	Project      string `json:"project"`
	CapturedAt   int64  `json:"captured_at"`
	TreeHash     string `json:"tree_hash"`
	FileCount    int    `json:"file_count"`
	TotalLines   int    `json:"total_lines"`
	Git          *Git   `json:"git,omitempty"`
	Files        []File `json:"files"`
	Unchanged    bool   `json:"unchanged,omitempty"`
	AgentVersion string `json:"agent_version"`
}

// File is one recorded file. PathHash holds a hash of the project-relative
// path, never the path itself.
type File struct {
	PathHash    string `json:"path_hash"`
	ContentHash string `json:"content_hash"`
	Lines       int    `json:"lines"`
	Bytes       int64  `json:"bytes"`
	ModTime     int64  `json:"mtime"`
}

// Git is the repository state at capture time, absent for non-repo projects.
type Git struct {
	Head   string `json:"head"`
	Branch string `json:"branch"`
	Dirty  bool   `json:"dirty"`
	Ahead  int    `json:"ahead"`
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
	files, err := Walk(root)
	if err != nil {
		return nil, err
	}

	total := 0
	for _, f := range files {
		total += f.Lines
	}

	s := &Snapshot{
		Project:      name,
		CapturedAt:   time.Now().Unix(),
		TreeHash:     TreeHash(files),
		FileCount:    len(files),
		TotalLines:   total,
		Files:        files,
		AgentVersion: agentVersion,
	}

	if g, err := gitstate.Read(root); err == nil && g != nil {
		s.Git = &Git{Head: g.Head, Branch: g.Branch, Dirty: g.Dirty, Ahead: g.Ahead}
	}
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

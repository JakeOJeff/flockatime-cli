// Package snapshot captures the shape of a project tree. It records hashes,
// counts, and sizes --- never file contents, never plaintext paths.
package snapshot

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

// File is one recorded file. Path holds a hash of the project-relative path,
// never the path itself.
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

// TreeHash is SHA-256 over the sorted "path_hash:content_hash" lines. It is
// stable across runs and changes on any single-character edit.
func TreeHash(files []File) string {
	// TODO: sort by path hash, hash "path:content\n" per file.
	return ""
}

// Capture walks root and returns the snapshot for the named project.
func Capture(name, root string) (*Snapshot, error) {
	// TODO: Walk, hash, TreeHash, attach git state.
	return nil, errNotImplemented
}

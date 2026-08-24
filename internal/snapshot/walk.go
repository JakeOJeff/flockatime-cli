package snapshot

import "errors"

var errNotImplemented = errors.New("snapshot: not implemented yet")

// maxFileBytes is the per-file ceiling; larger files are skipped entirely.
const maxFileBytes = 2 << 20

// alwaysSkip are directory names skipped regardless of .gitignore.
var alwaysSkip = []string{".git", "node_modules", "target", "dist", "vendor"}

// Walk returns a record for every file under root that survives .gitignore,
// the alwaysSkip list, and the size cap.
func Walk(root string) ([]File, error) {
	// TODO: filepath.WalkDir with a gitignore matcher stack, then hashFile.
	return nil, errNotImplemented
}

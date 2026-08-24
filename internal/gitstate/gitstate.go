// Package gitstate reads repository state by shelling out to git. No git
// library is vendored for this.
package gitstate

import "errors"

var errNotImplemented = errors.New("gitstate: not implemented yet")

// State mirrors the git block of a snapshot payload.
type State struct {
	Head   string
	Branch string
	Dirty  bool
	Ahead  int
}

// Read returns the state of the repo at root, or nil if root has no .git.
func Read(root string) (*State, error) {
	// TODO: rev-parse HEAD, rev-parse --abbrev-ref HEAD,
	// status --porcelain, rev-list --count @{u}..HEAD.
	return nil, errNotImplemented
}

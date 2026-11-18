package snapshot

import (
	"encoding/json"
	"sort"
)

// MaxSnapshotBytes is the encoded size fingerprints may grow a snapshot to.
// The file list itself is never cut: past this, fingerprints are what goes,
// since a snapshot without them is still a complete record and one dropped
// whole is a gap in the chain. It also bounds what one queued entry costs.
const MaxSnapshotBytes = 1 << 20

// FitFingerprints drops fingerprints until the encoded snapshot is at most
// max bytes, or none are left. Files with the fewest changed lines lose theirs
// first, so the files that moved most keep them. It returns how many files
// lost their fingerprints.
func (s *Snapshot) FitFingerprints(max int) int {
	b, err := json.Marshal(s)
	if err != nil || len(b) <= max {
		return 0
	}
	size := len(b)

	var idx []int
	for i := range s.Files {
		if len(s.Files[i].Fingerprints) > 0 {
			idx = append(idx, i)
		}
	}
	sort.SliceStable(idx, func(a, b int) bool {
		fa, fb := &s.Files[idx[a]], &s.Files[idx[b]]
		return fa.Added+fa.Removed < fb.Added+fb.Removed
	})

	dropped := 0
	for _, i := range idx {
		if size <= max {
			break
		}
		fp, _ := json.Marshal(s.Files[i].Fingerprints)
		size -= len(fp) + len(`,"fp":`)
		s.Files[i].Fingerprints = nil
		dropped++
	}
	return dropped
}

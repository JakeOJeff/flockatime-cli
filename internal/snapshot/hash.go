package snapshot

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
)

// hashFile streams path through SHA-256 while counting lines. The bytes are
// never retained, copied out, or logged --- the hasher and the newline counter
// are their only consumers, and the buffer is reused every read.
func hashFile(path string) (contentHash string, lines int, err error) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()

	h := sha256.New()
	buf := make([]byte, 32*1024)
	var last byte
	var total int64

	for {
		n, readErr := f.Read(buf)
		if n > 0 {
			chunk := buf[:n]
			h.Write(chunk)
			for _, b := range chunk {
				if b == '\n' {
					lines++
				}
			}
			last = chunk[n-1]
			total += int64(n)
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return "", 0, readErr
		}
	}
	// A final line without a trailing newline still counts as a line.
	if total > 0 && last != '\n' {
		lines++
	}
	return hex.EncodeToString(h.Sum(nil)), lines, nil
}

// hashPath returns SHA-256 of a project-relative path, so the payload carries
// the shape of the tree without any of its names. Separators are normalized to
// "/" so the same repo hashes identically on Windows and Unix.
func hashPath(rel string) string {
	sum := sha256.Sum256([]byte(filepath.ToSlash(rel)))
	return hex.EncodeToString(sum[:])
}

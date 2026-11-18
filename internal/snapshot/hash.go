package snapshot

import (
	"crypto/hmac"
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

// HashPath hashes a project-relative path, so the payload carries the shape of
// the tree without any of its names. Separators are normalized to "/" so the
// same repo hashes identically on Windows and Unix.
//
// With a salt it is HMAC-SHA256(salt, path). Unsalted, a path hash is the same
// for everyone, so common names ("src/main.rs") can be found by hashing a
// dictionary of likely paths; the salt makes that guess useless without it.
func HashPath(rel string, salt []byte) string {
	p := []byte(filepath.ToSlash(rel))
	if len(salt) == 0 {
		sum := sha256.Sum256(p)
		return hex.EncodeToString(sum[:])
	}
	m := hmac.New(sha256.New, salt)
	m.Write(p)
	return hex.EncodeToString(m.Sum(nil))
}

// hashPath is the unsalted HashPath.
func hashPath(rel string) string { return HashPath(rel, nil) }

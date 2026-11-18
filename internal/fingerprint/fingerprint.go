// Package fingerprint produces MOSS-style winnowed fingerprints of a source
// file, so a collector can spot copied code --- whole files, partial copies,
// and copies that were only reindented or re-commented --- without ever
// receiving the code itself.
//
// The pipeline is: strip comments and whitespace for the file's language
// family, split what is left into tokens, hash each token, hash every run of
// K consecutive token hashes (a K-gram), then winnow: keep the minimum K-gram
// hash of every window of W consecutive K-grams (Schleimer, Wilkerson and
// Aiken, "Winnowing: Local Algorithms for Document Fingerprinting", 2003).
// Any shared run of at least W+K-1 tokens is guaranteed to share at least one
// fingerprint when no cap is applied.
//
// Privacy: the file's bytes exist only inside File and Tokens while they are
// being tokenized; nothing retains, logs or returns any text. Each
// fingerprint is a 53-bit digest of K=30 tokens' 64-bit hashes, so it cannot
// be inverted back to the code, nor guessed token by token. The honest caveat
// is that a collector already holding a candidate source text can fingerprint
// it the same way and confirm a match --- that is the whole point of the
// package, and it reveals nothing about code the collector does not have.
package fingerprint

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const (
	// K is the number of tokens in one K-gram. Thirty tokens is a few lines
	// of code: long enough that boilerplate rarely matches by accident and
	// that a fingerprint cannot be brute-forced back into text.
	K = 30
	// W is the winnowing window, in K-grams. Any shared run of W+K-1 tokens
	// is guaranteed to produce a shared fingerprint (before capping).
	W = 8
	// MaxPerFile is the default cap on fingerprints sent for one file.
	MaxPerFile = 50
	// MaxFileBytes is the largest file File will read; bigger files are
	// skipped, matching the walker's 2 MB limit.
	MaxFileBytes = 2 << 20
)

// mask53 keeps values exactly representable as JSON numbers in JavaScript.
const mask53 = 1<<53 - 1

// kgramPrime is the multiplier of the polynomial K-gram hash. Any odd 64-bit
// constant works; changing it changes every fingerprint, so it is fixed.
const kgramPrime uint64 = 0x9e3779b97f4a7c15

// File reads the file at path once, normalizes it according to its extension,
// and returns at most max fingerprints (see Winnow). A binary file (contains a
// NUL byte) or one with fewer than K tokens returns nil, nil. So does a file
// over MaxFileBytes, or anything that is not a regular file.
func File(path string, max int) ([]uint64, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > MaxFileBytes {
		return nil, nil
	}
	// Read through a limit in case the file grew since Stat.
	src, err := io.ReadAll(io.LimitReader(f, MaxFileBytes+1))
	// The bytes are cleared before returning so no copy of the content
	// outlives this call, even in memory awaiting garbage collection.
	defer clear(src)
	if err != nil {
		return nil, err
	}
	if len(src) > MaxFileBytes || bytes.IndexByte(src, 0) >= 0 {
		return nil, nil
	}
	return Winnow(Tokens(src, extOf(path)), max), nil
}

// extOf returns the lowercase extension used to pick a comment family. A few
// conventional extension-less names are mapped so they get their comments
// stripped too.
func extOf(path string) string {
	base := strings.ToLower(filepath.Base(path))
	switch base {
	case "dockerfile", "makefile", "gnumakefile":
		return ".dockerfile" // all three use # line comments
	}
	return strings.ToLower(filepath.Ext(base))
}

// Winnow hashes every K-gram of tokens, keeps the minimum hash of each window
// of W consecutive K-gram hashes (rightmost minimum on ties, recorded once per
// position --- standard Schleimer/Wilkerson/Aiken winnowing), deduplicates,
// and if more than max remain keeps the max numerically smallest (so two
// copies of the same code select overlapping sets). Output sorted ascending.
//
// Every value is masked to 53 bits before selection. Fewer than K tokens, or
// max <= 0, returns nil.
func Winnow(tokens []uint64, max int) []uint64 {
	if max <= 0 || len(tokens) < K {
		return nil
	}
	n := len(tokens) - K + 1
	hashes := make([]uint64, n)

	// Polynomial hash h = sum tokens[i+j] * P^(K-1-j), rolled one token at a
	// time. Unsigned overflow wraps identically on every platform, so the
	// rolled value equals the directly computed one.
	var top uint64 = 1 // P^(K-1)
	for j := 0; j < K-1; j++ {
		top *= kgramPrime
	}
	var h uint64
	for j := 0; j < K; j++ {
		h = h*kgramPrime + tokens[j]
	}
	hashes[0] = mix(h) & mask53
	for i := 1; i < n; i++ {
		h = (h-tokens[i-1]*top)*kgramPrime + tokens[i+K-1]
		hashes[i] = mix(h) & mask53
	}

	// A file with fewer than W K-grams is a single, shorter window.
	w := min(W, n)
	seen := make(map[uint64]struct{})
	last := -1
	for start := 0; start+w <= n; start++ {
		m := start
		for j := start + 1; j < start+w; j++ {
			if hashes[j] <= hashes[m] { // <= picks the rightmost minimum
				m = j
			}
		}
		if m != last {
			seen[hashes[m]] = struct{}{}
			last = m
		}
	}

	out := make([]uint64, 0, len(seen))
	for v := range seen {
		out = append(out, v)
	}
	sort.Slice(out, func(a, b int) bool { return out[a] < out[b] })
	if len(out) > max {
		out = out[:max:max]
	}
	return out
}

// mix is the splitmix64 finalizer. The polynomial hash is linear, so similar
// K-grams would land on similar values; mixing spreads them uniformly, which
// keeps "smallest N" selection an unbiased sample of the file.
func mix(x uint64) uint64 {
	x ^= x >> 30
	x *= 0xbf58476d1ce4e5b9
	x ^= x >> 27
	x *= 0x94d049bb133111eb
	x ^= x >> 31
	return x
}

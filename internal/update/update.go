// Package update replaces the running binary with the newest GitHub release.
//
// It downloads the same archive the install scripts use, checks it against the
// release's SHA256SUMS, extracts the binary, runs it once to confirm it is the
// promised version for this OS and architecture, and only then swaps it into
// place. Any failure leaves the current binary untouched.
//
// SHA256SUMS lives in the same release as the archive, so this catches a
// truncated or corrupted download, not a compromised release: whoever can
// publish a release can also publish matching sums.
package update

import (
	"archive/tar"
	"archive/zip"
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// Repo is where releases are published by .github/workflows/release.yml.
const Repo = "JakeOJeff/flockatime-cli"

// maxDownload bounds an archive; today's are a few MB.
const maxDownload = 100 << 20

var client = &http.Client{
	Timeout: 2 * time.Minute,
	// GitHub redirects downloads to its CDN; never let that drop to http.
	CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if req.URL.Scheme != "https" {
			return fmt.Errorf("refusing redirect to %s", req.URL.Scheme)
		}
		if len(via) >= 10 {
			return errors.New("too many redirects")
		}
		return nil
	},
}

// Latest returns the version of the newest release, without a leading "v".
// It reads the redirect of /releases/latest rather than calling the API, which
// has a 60-request-per-hour limit shared by everything on the network.
func Latest(ctx context.Context) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodHead,
		"https://github.com/"+Repo+"/releases/latest", nil)
	if err != nil {
		return "", err
	}
	noFollow := *client
	noFollow.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := noFollow.Do(req)
	if err != nil {
		return "", err
	}
	resp.Body.Close()
	loc := resp.Header.Get("Location")
	_, tag, ok := strings.Cut(loc, "/releases/tag/")
	if !ok || tag == "" {
		return "", fmt.Errorf("no release found (status %s)", resp.Status)
	}
	return strings.TrimPrefix(tag, "v"), nil
}

// Newer reports whether latest is a higher X.Y.Z than current. Anything that
// is not plain X.Y.Z on both sides --- a dev build, a pre-release --- is never
// replaced automatically.
func Newer(latest, current string) bool {
	l, ok1 := parse(latest)
	c, ok2 := parse(current)
	if !ok1 || !ok2 {
		return false
	}
	for i := range l {
		if l[i] != c[i] {
			return l[i] > c[i]
		}
	}
	return false
}

func parse(v string) ([3]int, bool) {
	var out [3]int
	parts := strings.Split(strings.TrimPrefix(v, "v"), ".")
	if len(parts) != 3 {
		return out, false
	}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return out, false
		}
		out[i] = n
	}
	return out, true
}

// AssetName is the release archive for this platform, as release.yml names it.
func AssetName() string {
	base := fmt.Sprintf("snapshot-agent_%s_%s", runtime.GOOS, runtime.GOARCH)
	if runtime.GOOS == "windows" {
		return base + ".zip"
	}
	return base + ".tar.gz"
}

func binName() string {
	if runtime.GOOS == "windows" {
		return "snapshot-agent.exe"
	}
	return "snapshot-agent"
}

// Apply downloads release version and installs it over the running binary.
// It returns the installed path; the running process is not restarted, the
// caller does that. (Ask for the path now: after the swap, os.Executable on
// Linux follows the moved inode to the .old file.)
func Apply(ctx context.Context, version string) (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	if exe, err = filepath.EvalSymlinks(exe); err != nil {
		return "", err
	}

	base := "https://github.com/" + Repo + "/releases/download/v" + version + "/"
	asset := AssetName()
	sums, err := fetch(ctx, base+"SHA256SUMS")
	if err != nil {
		return "", fmt.Errorf("SHA256SUMS: %w", err)
	}
	want, err := sumFor(sums, asset)
	if err != nil {
		return "", err
	}
	archive, err := fetch(ctx, base+asset)
	if err != nil {
		return "", fmt.Errorf("%s: %w", asset, err)
	}
	got := sha256.Sum256(archive)
	if hex.EncodeToString(got[:]) != want {
		return "", fmt.Errorf("%s does not match SHA256SUMS; not installing it", asset)
	}
	bin, err := extract(archive, asset, binName())
	if err != nil {
		return "", err
	}

	// Write beside the current binary so the final rename stays on one disk.
	ext := filepath.Ext(exe) // ".exe" on Windows, which it needs to run
	fresh := strings.TrimSuffix(exe, ext) + ".new" + ext
	if err := os.WriteFile(fresh, bin, 0o755); err != nil {
		return "", fmt.Errorf("writing %s: %w", fresh, err)
	}
	defer os.Remove(fresh) // no-op once renamed

	if err := selfTest(ctx, fresh, version); err != nil {
		return "", err
	}

	// Windows will not overwrite a running .exe but will rename it, so move
	// the old one aside rather than writing over it. CleanupOld removes it on
	// the next start.
	old := exe + ".old"
	os.Remove(old)
	if err := os.Rename(exe, old); err != nil {
		return "", fmt.Errorf("moving the current binary aside: %w", err)
	}
	if err := os.Rename(fresh, exe); err != nil {
		if rbErr := os.Rename(old, exe); rbErr != nil {
			return "", fmt.Errorf("installing %s: %v; restoring the old binary also failed: %v", exe, err, rbErr)
		}
		return "", fmt.Errorf("installing %s: %w", exe, err)
	}
	return exe, nil
}

// CleanupOld removes the binary a previous update moved aside. It can only go
// once the process that was running from it has exited, i.e. now.
func CleanupOld() {
	if exe, err := os.Executable(); err == nil {
		if exe, err = filepath.EvalSymlinks(exe); err == nil {
			os.Remove(exe + ".old")
		}
	}
}

// selfTest runs the new binary's `version` command. It proves the file is a
// working executable for this machine, and that it is the version we meant to
// install, before the working one is touched.
func selfTest(ctx context.Context, bin, version string) error {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, bin, "version").Output()
	if err != nil {
		return fmt.Errorf("new binary does not run: %w", err)
	}
	if got := strings.TrimSpace(string(out)); got != version {
		return fmt.Errorf("new binary reports version %q, expected %q", got, version)
	}
	return nil
}

func fetch(ctx context.Context, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s: %s", url, resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxDownload+1))
	if err != nil {
		return nil, err
	}
	if len(body) > maxDownload {
		return nil, fmt.Errorf("GET %s: larger than %d bytes", url, maxDownload)
	}
	return body, nil
}

// sumFor finds name in sha256sum output ("<hex>  <name>", or "*<name>" in
// binary mode).
func sumFor(sums []byte, name string) (string, error) {
	sc := bufio.NewScanner(bytes.NewReader(sums))
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) == 2 && strings.TrimPrefix(fields[1], "*") == name {
			return strings.ToLower(fields[0]), nil
		}
	}
	return "", fmt.Errorf("%s is not listed in SHA256SUMS", name)
}

// extract pulls the file called bin out of a .zip or .tar.gz release archive.
func extract(archive []byte, asset, bin string) ([]byte, error) {
	if strings.HasSuffix(asset, ".zip") {
		zr, err := zip.NewReader(bytes.NewReader(archive), int64(len(archive)))
		if err != nil {
			return nil, err
		}
		for _, f := range zr.File {
			if path.Base(f.Name) != bin || !f.Mode().IsRegular() {
				continue
			}
			rc, err := f.Open()
			if err != nil {
				return nil, err
			}
			defer rc.Close()
			return readCapped(rc)
		}
		return nil, fmt.Errorf("%s has no %s", asset, bin)
	}

	gz, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		return nil, err
	}
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return nil, fmt.Errorf("%s has no %s", asset, bin)
		}
		if err != nil {
			return nil, err
		}
		if h.Typeflag == tar.TypeReg && path.Base(h.Name) == bin {
			return readCapped(tr)
		}
	}
}

func readCapped(r io.Reader) ([]byte, error) {
	b, err := io.ReadAll(io.LimitReader(r, maxDownload+1))
	if err == nil && len(b) > maxDownload {
		err = errors.New("binary in archive is implausibly large")
	}
	return b, err
}

package update

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"testing"
)

func TestNewer(t *testing.T) {
	cases := []struct {
		latest, current string
		want            bool
	}{
		{"0.2.3", "0.2.2", true},
		{"0.10.0", "0.9.9", true},
		{"1.0.0", "0.99.99", true},
		{"0.2.2", "0.2.2", false},
		{"0.2.1", "0.2.2", false},
		{"v0.3.0", "0.2.2", true},
		{"0.3.0", "dev", false},       // dev builds are never replaced
		{"0.3.0-rc1", "0.2.2", false}, // nor jumped onto a pre-release
		{"", "0.2.2", false},
	}
	for _, tc := range cases {
		if got := Newer(tc.latest, tc.current); got != tc.want {
			t.Errorf("Newer(%q, %q) = %v, want %v", tc.latest, tc.current, got, tc.want)
		}
	}
}

func TestSumFor(t *testing.T) {
	sums := []byte("abc123  snapshot-agent_linux_amd64.tar.gz\nDEF456 *snapshot-agent_windows_amd64.zip\n")
	if got, err := sumFor(sums, "snapshot-agent_windows_amd64.zip"); err != nil || got != "def456" {
		t.Errorf("windows: %q, %v", got, err)
	}
	if got, err := sumFor(sums, "snapshot-agent_linux_amd64.tar.gz"); err != nil || got != "abc123" {
		t.Errorf("linux: %q, %v", got, err)
	}
	if _, err := sumFor(sums, "snapshot-agent_darwin_arm64.tar.gz"); err == nil {
		t.Error("missing asset should be an error")
	}
}

// The archives are laid out the way release.yml builds them: one folder per
// target holding the binary, the README, and the example config.
func TestExtract(t *testing.T) {
	want := []byte("\x7fELF pretend binary")

	var tgz bytes.Buffer
	gz := gzip.NewWriter(&tgz)
	tw := tar.NewWriter(gz)
	for name, body := range map[string][]byte{
		"snapshot-agent_linux_amd64/README.md":      []byte("readme"),
		"snapshot-agent_linux_amd64/snapshot-agent": want,
	} {
		tw.WriteHeader(&tar.Header{Name: name, Mode: 0o755, Size: int64(len(body)), Typeflag: tar.TypeReg})
		tw.Write(body)
	}
	tw.Close()
	gz.Close()
	got, err := extract(tgz.Bytes(), "x.tar.gz", "snapshot-agent")
	if err != nil || !bytes.Equal(got, want) {
		t.Errorf("tar.gz: %q, %v", got, err)
	}

	var zbuf bytes.Buffer
	zw := zip.NewWriter(&zbuf)
	w, _ := zw.Create("snapshot-agent_windows_amd64/snapshot-agent.exe")
	w.Write(want)
	zw.Close()
	got, err = extract(zbuf.Bytes(), "x.zip", "snapshot-agent.exe")
	if err != nil || !bytes.Equal(got, want) {
		t.Errorf("zip: %q, %v", got, err)
	}

	if _, err := extract(zbuf.Bytes(), "x.zip", "other"); err == nil {
		t.Error("missing binary should be an error")
	}
}

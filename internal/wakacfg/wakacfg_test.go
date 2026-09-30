package wakacfg

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestWithDebug(t *testing.T) {
	cases := []struct {
		name, in, want string
		changed        bool
	}{
		{
			name:    "hackatime config gains debug under settings",
			in:      "[settings]\napi_url = https://hackatime.hackclub.com/api/hackatime/v1\napi_key = abc\n",
			want:    "[settings]\ndebug = true\napi_url = https://hackatime.hackclub.com/api/hackatime/v1\napi_key = abc\n",
			changed: true,
		},
		{
			name:    "debug = false is flipped in place",
			in:      "[settings]\napi_key = abc\ndebug = false\n",
			want:    "[settings]\napi_key = abc\ndebug = true\n",
			changed: true,
		},
		{
			name:    "already on is left untouched",
			in:      "[settings]\ndebug = true\napi_key = abc\n",
			want:    "[settings]\ndebug = true\napi_key = abc\n",
			changed: false,
		},
		{
			name:    "a debug key in another section does not count",
			in:      "[other]\ndebug = true\n[settings]\napi_key = abc\n",
			want:    "[other]\ndebug = true\n[settings]\ndebug = true\napi_key = abc\n",
			changed: true,
		},
		{
			name:    "no settings section appends one",
			in:      "[git]\nsubmodules_disabled = false\n",
			want:    "[git]\nsubmodules_disabled = false\n\n[settings]\ndebug = true\n",
			changed: true,
		},
		{
			name:    "windows line endings are kept",
			in:      "[settings]\r\napi_key = abc\r\n",
			want:    "[settings]\r\ndebug = true\r\napi_key = abc\r\n",
			changed: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, changed := WithDebug(tc.in)
			if got != tc.want || changed != tc.changed {
				t.Errorf("WithDebug(%q)\n got  %q, %v\n want %q, %v", tc.in, got, changed, tc.want, tc.changed)
			}
		})
	}
}

func TestEnableDebugWithoutWakaTime(t *testing.T) {
	_, err := EnableDebug(filepath.Join(t.TempDir(), ".wakatime.cfg"))
	if !errors.Is(err, ErrNotInstalled) {
		t.Fatalf("err = %v, want ErrNotInstalled", err)
	}
}

func TestEnableDebugKeepsTheRestOfTheFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".wakatime.cfg")
	if err := os.WriteFile(path, []byte("[settings]\napi_key = secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if changed, err := EnableDebug(path); err != nil || !changed {
		t.Fatalf("changed=%v err=%v", changed, err)
	}
	b, _ := os.ReadFile(path)
	if string(b) != "[settings]\ndebug = true\napi_key = secret\n" {
		t.Errorf("file = %q", b)
	}
	if changed, _ := EnableDebug(path); changed {
		t.Error("second run changed the file again")
	}
}

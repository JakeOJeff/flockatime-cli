package aitrace

import (
	"encoding/json"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
)

// Agents often write files through the shell rather than their edit tools:
// `cat > f <<'EOF'`, `cat >> f <<EOF`, `tee f <<EOF`. heredocWrites finds
// those in a shell command and returns, per target file, the body written.
// Commands it cannot resolve (a $VAR target, a here-string piped elsewhere)
// are left alone: missing a write is better than crediting the wrong file.

var (
	heredocRe = regexp.MustCompile(`<<(-?)\s*(?:'([A-Za-z_][A-Za-z0-9_]*)'|"([A-Za-z_][A-Za-z0-9_]*)"|([A-Za-z_][A-Za-z0-9_]*))`)
	targetRe  = regexp.MustCompile(`(?:>>?|\btee(?:\s+-a)?)\s*("[^"]+"|'[^']+'|[^\s;|&<>()]+)`)
)

func heredocWrites(cmd, cwd string) map[string]string {
	out := map[string]string{}
	for {
		m := heredocRe.FindStringSubmatchIndex(cmd)
		if m == nil {
			return out
		}
		strip := m[3] > m[2]
		delim := ""
		for g := 4; g <= 8; g += 2 {
			if m[g] >= 0 {
				delim = cmd[m[g]:m[g+1]]
			}
		}
		// The target is on the line of the <<, before or after it.
		ls := strings.LastIndexByte(cmd[:m[0]], '\n') + 1
		nl := strings.IndexByte(cmd[m[1]:], '\n')
		if nl < 0 {
			return out
		}
		head := cmd[ls:m[0]] + " " + cmd[m[1]:m[1]+nl]
		body := cmd[m[1]+nl+1:]

		var b strings.Builder
		rest := ""
		for i, line := range strings.SplitAfter(body, "\n") {
			l := strings.TrimRight(line, "\r\n")
			if strip {
				l = strings.TrimLeft(l, "\t")
			}
			if l == delim {
				rest = strings.Join(strings.SplitAfter(body, "\n")[i+1:], "")
				break
			}
			b.WriteString(l)
			b.WriteByte('\n')
		}
		if t := targetRe.FindStringSubmatch(head); t != nil {
			if p := shellPath(strings.Trim(t[1], `"'`), cwd); p != "" {
				out[p] += b.String()
			}
		}
		if rest == "" {
			return out
		}
		cmd = rest
	}
}

// shellPath resolves a redirect target to an absolute path, or "" for one
// that cannot be a project file. Git Bash spells D:\x as /d/x.
func shellPath(p, cwd string) string {
	if p == "" || strings.ContainsAny(p, "$`*?") || p == "/dev/null" || strings.HasPrefix(p, "&") {
		return ""
	}
	if runtime.GOOS == "windows" && len(p) >= 3 && p[0] == '/' && p[2] == '/' && isLetter(p[1]) {
		p = strings.ToUpper(p[1:2]) + ":" + p[2:]
	}
	if strings.HasPrefix(p, "~") {
		return "" // whose home? not a project path we can trust
	}
	if !filepath.IsAbs(p) {
		if cwd == "" {
			return ""
		}
		p = filepath.Join(cwd, p)
	}
	return filepath.Clean(p)
}

func isLetter(c byte) bool { return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' }

// stringsWith returns every string in a decoded JSON value that contains
// marker, looking inside JSON-encoded strings and inside double-quoted string
// literals of code (Codex wraps tool calls in JS), up to a fixed depth.
func stringsWith(v any, marker string, depth int) []string {
	if depth > 6 {
		return nil
	}
	var out []string
	switch x := v.(type) {
	case map[string]any:
		for _, c := range x {
			out = append(out, stringsWith(c, marker, depth+1)...)
		}
	case []any:
		for _, c := range x {
			out = append(out, stringsWith(c, marker, depth+1)...)
		}
	case string:
		if !strings.Contains(x, marker) {
			return nil
		}
		t := strings.TrimSpace(x)
		if strings.HasPrefix(t, "{") || strings.HasPrefix(t, "[") {
			var inner any
			if json.Unmarshal([]byte(t), &inner) == nil {
				return stringsWith(inner, marker, depth+1)
			}
		}
		if lits := literalsWith(x, marker); len(lits) > 0 {
			return lits
		}
		out = append(out, x)
	}
	return out
}

// literalsWith decodes the double-quoted literals in src that contain marker.
// It only trusts a scan that starts at a quote following a call paren, comma
// or colon, which is where JS puts string arguments.
func literalsWith(src, marker string) []string {
	var out []string
	for i := 0; i < len(src); i++ {
		if src[i] != '"' || i == 0 || !strings.ContainsRune("(,:[ ", rune(src[i-1])) {
			continue
		}
		j := i + 1
		for j < len(src) && src[j] != '"' && src[j] != '\n' {
			if src[j] == '\\' {
				j++
			}
			j++
		}
		if j >= len(src) || src[j] != '"' {
			continue
		}
		var s string
		if json.Unmarshal([]byte(src[i:j+1]), &s) == nil && strings.Contains(s, marker) {
			out = append(out, s)
		}
		i = j
	}
	return out
}

package aitrace

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Sources says where each tool keeps its transcripts. Empty fields are
// skipped. DefaultSources fills them from the home directory.
type Sources struct {
	ClaudeDir string // ~/.claude/projects, one folder per working directory
	CodexDir  string // ~/.codex/sessions, YYYY/MM/DD/rollout-*.jsonl
}

// DefaultSources honours CLAUDE_CONFIG_DIR and CODEX_HOME like the tools do.
func DefaultSources() Sources {
	home, _ := os.UserHomeDir()
	claude := os.Getenv("CLAUDE_CONFIG_DIR")
	if claude == "" {
		claude = filepath.Join(home, ".claude")
	}
	codex := os.Getenv("CODEX_HOME")
	if codex == "" {
		codex = filepath.Join(home, ".codex")
	}
	return Sources{
		ClaudeDir: filepath.Join(claude, "projects"),
		CodexDir:  filepath.Join(codex, "sessions"),
	}
}

// Sync reads whatever the transcripts gained since the last Sync. Transcripts
// are append-only JSONL, so each is read from where the previous Sync stopped,
// and only complete lines are consumed. The first Sync reads them all.
func (s *Store) Sync(src Sources) (newEvents int) {
	before := s.NextSeq
	if src.ClaudeDir != "" {
		s.syncDir(src.ClaudeDir, s.claudeLine)
	}
	if src.CodexDir != "" {
		s.syncDir(src.CodexDir, s.codexLine)
	}
	return int(s.NextSeq - before)
}

// lineFunc handles one transcript line. st carries per-file context between
// lines (Codex states its working directory once, at the top).
type lineFunc func(line []byte, st *fileState)

type fileState struct {
	cwd  string
	seen map[string]bool // tick dedupe
}

func (s *Store) syncDir(dir string, fn lineFunc) {
	_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(p, ".jsonl") {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		off := s.Cursors[p]
		if info.Size() == off {
			return nil
		}
		if info.Size() < off {
			off = 0 // rewritten or truncated: start over
		}
		s.Cursors[p] = s.readFrom(p, off, fn)
		s.dirty = true
		return nil
	})
}

// readFrom processes complete lines of p from off and returns the new offset.
// Codex's working directory sits in the first line, so a resume past it
// re-reads that line for context without re-ingesting it.
func (s *Store) readFrom(p string, off int64, fn lineFunc) int64 {
	f, err := os.Open(p)
	if err != nil {
		return off
	}
	defer f.Close()

	st := &fileState{seen: map[string]bool{}}
	if off > 0 {
		if first, err := bufio.NewReader(f).ReadBytes('\n'); err == nil {
			var m codexMeta
			if json.Unmarshal(first, &m) == nil && m.Type == "session_meta" {
				st.cwd = m.Payload.Cwd
			}
		}
		if _, err := f.Seek(off, io.SeekStart); err != nil {
			return off
		}
	}
	r := bufio.NewReaderSize(f, 256<<10)
	for {
		line, err := r.ReadBytes('\n')
		if err != nil {
			return off // a partial last line waits for the next Sync
		}
		off += int64(len(line))
		fn(line, st)
	}
}

func parseTime(ts string) int64 {
	t, err := time.Parse(time.RFC3339Nano, ts)
	if err != nil {
		return 0
	}
	return t.Unix()
}

// --- Claude Code ---------------------------------------------------------

type claudeLine struct {
	Type      string `json:"type"`
	Timestamp string `json:"timestamp"`
	Cwd       string `json:"cwd"`
	Message   struct {
		Content json.RawMessage `json:"content"`
	} `json:"message"`
}

type claudeBlock struct {
	Type  string          `json:"type"`
	Name  string          `json:"name"`
	Input json.RawMessage `json:"input"`
}

type claudeInput struct {
	FilePath  string `json:"file_path"`
	Command   string `json:"command"`    // Bash
	Content   string `json:"content"`    // Write
	NewString string `json:"new_string"` // Edit
	Edits     []struct {
		NewString string `json:"new_string"`
	} `json:"edits"` // MultiEdit
}

// claudeLine: every user or assistant line is a sign of a live session in its
// cwd; assistant lines with a Write, Edit or MultiEdit tool call carry the
// exact text the agent put in the file.
func (s *Store) claudeLine(line []byte, st *fileState) {
	if !bytes.Contains(line, []byte(`"timestamp"`)) {
		return
	}
	var l claudeLine
	if json.Unmarshal(line, &l) != nil || (l.Type != "user" && l.Type != "assistant") {
		return
	}
	at := parseTime(l.Timestamp)
	s.tick(ToolClaudeCode, l.Cwd, at, st.seen)
	if l.Type != "assistant" || !bytes.Contains(l.Message.Content, []byte(`"tool_use"`)) {
		return
	}
	var blocks []claudeBlock
	if json.Unmarshal(l.Message.Content, &blocks) != nil {
		return
	}
	for _, b := range blocks {
		if b.Type != "tool_use" {
			continue
		}
		var in claudeInput
		if json.Unmarshal(b.Input, &in) != nil {
			continue
		}
		switch b.Name {
		case "Write", "Edit", "MultiEdit":
			if in.FilePath == "" {
				continue
			}
			text := in.Content + "\n" + in.NewString
			for _, e := range in.Edits {
				text += "\n" + e.NewString
			}
			s.addLines(ToolClaudeCode, resolve(in.FilePath, l.Cwd), text, at)
		case "Bash":
			for path, body := range heredocWrites(in.Command, l.Cwd) {
				s.addLines(ToolClaudeCode, path, body, at)
			}
		}
	}
}

// --- Codex ---------------------------------------------------------------

type codexMeta struct {
	Type    string `json:"type"`
	Payload struct {
		Cwd string `json:"cwd"`
	} `json:"payload"`
}

type codexLine struct {
	Timestamp string          `json:"timestamp"`
	Type      string          `json:"type"`
	Payload   json.RawMessage `json:"payload"`
}

const beginPatch = "*** Begin Patch"

// codexLine: the session's cwd comes from session_meta; every timestamped
// line ticks the session; and tool calls carry what the agent wrote, as
// apply_patch text or as shell heredocs. Codex has nested both in several
// shapes over time --- a plain argument, a JSON-encoded arguments string, a
// string literal inside a JS snippet --- so they are searched for, not
// addressed. Only calls are read, never their outputs, which can echo text
// the agent did not write.
func (s *Store) codexLine(line []byte, st *fileState) {
	var l codexLine
	if json.Unmarshal(line, &l) != nil {
		return
	}
	if l.Type == "session_meta" {
		var m codexMeta
		if json.Unmarshal(line, &m) == nil {
			st.cwd = m.Payload.Cwd
		}
	}
	at := parseTime(l.Timestamp)
	s.tick(ToolCodex, st.cwd, at, st.seen)
	if l.Type != "response_item" {
		return
	}
	hasPatch := bytes.Contains(l.Payload, []byte(beginPatch))
	hasHeredoc := bytes.Contains(l.Payload, []byte("<<"))
	if !hasPatch && !hasHeredoc {
		return
	}
	var v map[string]any
	if json.Unmarshal(l.Payload, &v) != nil {
		return
	}
	if t, _ := v["type"].(string); !strings.HasSuffix(t, "_call") {
		return
	}
	if hasPatch {
		for _, p := range stringsWith(v, beginPatch, 0) {
			for path, added := range parsePatch(p[strings.Index(p, beginPatch):]) {
				s.addLines(ToolCodex, resolve(path, st.cwd), added, at)
			}
		}
	}
	if hasHeredoc {
		for _, c := range stringsWith(v, "<<", 0) {
			for path, body := range heredocWrites(c, st.cwd) {
				s.addLines(ToolCodex, path, body, at)
			}
		}
	}
}

// parsePatch returns, per file named in a Codex patch, the lines it adds.
func parsePatch(p string) map[string]string {
	out := map[string]string{}
	var cur string
	var b strings.Builder
	flush := func() {
		if cur != "" && b.Len() > 0 {
			out[cur] += b.String()
		}
		b.Reset()
	}
	for _, line := range strings.Split(p, "\n") {
		line = strings.TrimSuffix(line, "\r")
		switch {
		case strings.HasPrefix(line, "*** Add File: "):
			flush()
			cur = strings.TrimSpace(strings.TrimPrefix(line, "*** Add File: "))
		case strings.HasPrefix(line, "*** Update File: "):
			flush()
			cur = strings.TrimSpace(strings.TrimPrefix(line, "*** Update File: "))
		case strings.HasPrefix(line, "*** Move to: "):
			flush()
			cur = strings.TrimSpace(strings.TrimPrefix(line, "*** Move to: "))
		case line == "*** End of File":
			// ends a hunk, not the file: later hunks may follow
		case strings.HasPrefix(line, "*** "):
			flush()
			cur = "" // Delete File, End Patch, End of File
		case strings.HasPrefix(line, "+") && cur != "":
			b.WriteString(line[1:])
			b.WriteByte('\n')
		}
	}
	flush()
	return out
}

func resolve(p, cwd string) string {
	if filepath.IsAbs(p) || cwd == "" {
		return p
	}
	return filepath.Join(cwd, p)
}

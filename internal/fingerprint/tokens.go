package fingerprint

import (
	"bytes"
	"unicode"
	"unicode/utf8"
)

// lang describes how comments and string literals look in one language
// family. Strings are tracked only so that a comment marker inside one
// ("http://...", "#fff") is not mistaken for a comment; their contents are
// tokenized like any other text.
type lang struct {
	line   []string    // line-comment openers, ending at the newline
	block  [][2]string // block-comment open/close pairs, checked before line
	quotes string      // string delimiters (backslash escapes honored)
	triple bool        // Python-style """ and ''' strings
}

var (
	cLike  = lang{line: []string{"//"}, block: [][2]string{{"/*", "*/"}}, quotes: "\"'`"}
	css    = lang{block: [][2]string{{"/*", "*/"}}, quotes: "\"'"}
	hash   = lang{line: []string{"#"}, quotes: "\"'`"}
	python = lang{line: []string{"#"}, quotes: "\"'", triple: true}
	sql    = lang{line: []string{"--"}, block: [][2]string{{"/*", "*/"}}, quotes: "\"'"}
	lua    = lang{line: []string{"--"}, block: [][2]string{{"--[[", "]]"}}, quotes: "\"'"}
	// Haskell uses ' inside identifiers (x'), so only " delimits strings.
	haskell = lang{line: []string{"--"}, block: [][2]string{{"{-", "-}"}}, quotes: "\""}
	// Markup text is full of apostrophes, so no string tracking at all.
	markup = lang{block: [][2]string{{"<!--", "-->"}}}
	plain  = lang{}
)

var families = map[string]*lang{}

func init() {
	register := func(l *lang, exts ...string) {
		for _, e := range exts {
			families[e] = l
		}
	}
	register(&cLike, ".go", ".c", ".h", ".cc", ".cpp", ".cxx", ".hpp", ".hh",
		".java", ".js", ".jsx", ".mjs", ".cjs", ".ts", ".tsx", ".rs", ".cs",
		".swift", ".kt", ".kts", ".scala", ".php", ".dart", ".zig", ".scss", ".less")
	// Plain CSS has no // comments, and url(http://...) must survive.
	register(&css, ".css")
	register(&python, ".py")
	register(&hash, ".rb", ".sh", ".bash", ".zsh", ".yaml", ".yml", ".toml",
		".r", ".pl", ".ex", ".exs", ".nim", ".cmake", ".dockerfile")
	register(&sql, ".sql")
	register(&lua, ".lua")
	register(&haskell, ".hs")
	register(&markup, ".html", ".htm", ".xml", ".vue", ".svelte")
}

// FNV-1a, 64-bit: stable, platform-independent, and cheap enough to fold in
// one byte at a time while scanning, so no token text is ever copied out.
const (
	fnvOffset uint64 = 0xcbf29ce484222325
	fnvPrime  uint64 = 0x100000001b3
)

// Tokens normalizes src for a file with extension ext (".go", ".py", ...
// lowercase, with dot): strips comments for the language family and all
// whitespace, and returns the token hashes in order.
//
// A token is a maximal run of identifier characters (letters, digits, '_',
// any non-ASCII rune) or a single punctuation/operator rune. Unknown
// extensions get whitespace stripping only.
func Tokens(src []byte, ext string) []uint64 {
	lg := families[ext]
	if lg == nil {
		lg = &plain
	}

	var out []uint64
	var h uint64
	inIdent := false
	flush := func() {
		if inIdent {
			out = append(out, h)
			inIdent = false
		}
	}
	punct := func(b []byte) {
		flush()
		out = append(out, fnv(fnvOffset, b))
	}

	var quote byte   // open string delimiter, 0 outside a string
	var triple bool  // the open string is """ or '''
	var escaped bool // previous rune was a backslash inside a string

	for i := 0; i < len(src); {
		if quote == 0 {
			if end, ok := lg.comment(src, i); ok {
				flush() // a comment separates tokens, like whitespace
				i = end
				continue
			}
		}
		r, size := utf8.DecodeRune(src[i:])
		cur := src[i : i+size]
		wasEscaped := escaped
		escaped = false

		switch {
		case r == '_' || r >= utf8.RuneSelf || unicode.IsLetter(r) || unicode.IsDigit(r):
			// Invalid UTF-8 decodes to RuneError (>= RuneSelf), so stray
			// bytes join identifiers instead of being dropped.
			if !inIdent {
				h, inIdent = fnvOffset, true
			}
			h = fnv(h, cur)
		case unicode.IsSpace(r):
			flush()
			// Ordinary strings cannot span lines; ending them at a newline
			// recovers from apostrophes that were never strings (Rust
			// lifetimes, shell words) without swallowing later comments.
			if r == '\n' && quote != 0 && quote != '`' && !triple && !wasEscaped {
				quote = 0
			}
		default:
			q := byte(r)
			isQuote := r < utf8.RuneSelf && bytes.IndexByte([]byte(lg.quotes), q) >= 0
			switch {
			case quote == 0 && isQuote:
				if lg.triple && bytes.HasPrefix(src[i:], []byte{q, q, q}) {
					punct(cur)
					punct(cur)
					punct(cur)
					quote, triple = q, true
					i += 3
					continue
				}
				quote = q
			case quote != 0 && q == quote && !wasEscaped:
				if triple {
					if bytes.HasPrefix(src[i:], []byte{q, q, q}) {
						punct(cur)
						punct(cur)
						punct(cur)
						quote, triple = 0, false
						i += 3
						continue
					}
				} else {
					quote = 0
				}
			case quote != 0 && r == '\\' && !wasEscaped:
				escaped = true
			}
			punct(cur)
		}
		i += size
	}
	flush()
	return out
}

// comment reports whether a comment starts at src[i] and, if so, the index
// just past it. Line comments stop before their newline; an unterminated
// block comment runs to the end of the file.
func (lg *lang) comment(src []byte, i int) (int, bool) {
	rest := src[i:]
	for _, b := range lg.block {
		if hasPrefix(rest, b[0]) {
			if j := bytes.Index(rest[len(b[0]):], []byte(b[1])); j >= 0 {
				return i + len(b[0]) + j + len(b[1]), true
			}
			return len(src), true
		}
	}
	for _, l := range lg.line {
		if hasPrefix(rest, l) {
			if j := bytes.IndexByte(rest, '\n'); j >= 0 {
				return i + j, true
			}
			return len(src), true
		}
	}
	return 0, false
}

func hasPrefix(b []byte, s string) bool {
	return len(b) >= len(s) && string(b[:len(s)]) == s
}

func fnv(h uint64, b []byte) uint64 {
	for _, c := range b {
		h ^= uint64(c)
		h *= fnvPrime
	}
	return h
}

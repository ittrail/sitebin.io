package abuse

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"hash"
	"path"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	// MaxScanBytes is how much of one file is matched against the rules. A
	// kit is a few kilobytes; the cap bounds what a hostile 100 MB "HTML"
	// file can cost. The whole file is still hashed.
	MaxScanBytes = 8 << 20
	// chunkSize is how much is matched at a time; overlap is how much of the
	// previous chunk is matched again, so a pattern straddling the edge is
	// still found. No pattern is meant to span more than overlap bytes.
	chunkSize = 64 << 10
	overlap   = 4 << 10
	// sniffBytes is how much of a file is checked for a NUL byte, which
	// marks it as binary: no kit is, and matching an image is wasted work.
	sniffBytes = 8 << 10
	excerptMax = 160
)

// activeExt are the files a browser renders or runs when a static host
// serves them. A rule blocks only in one of these; the same hit in a .txt or
// a .php (served as text or a download here — Caddy never sniffs a type) is
// only a flag.
var activeExt = map[string]bool{
	"html": true, "htm": true, "xhtml": true, "shtml": true, "svg": true,
	"js": true, "mjs": true, "cjs": true,
}

// binaryExt are skipped without reading a byte.
var binaryExt = map[string]bool{
	"png": true, "jpg": true, "jpeg": true, "gif": true, "webp": true, "avif": true, "ico": true,
	"bmp": true, "tif": true, "tiff": true, "heic": true, "psd": true,
	"woff": true, "woff2": true, "ttf": true, "otf": true, "eot": true,
	"mp4": true, "webm": true, "mov": true, "m4v": true, "mkv": true, "avi": true,
	"mp3": true, "wav": true, "ogg": true, "oga": true, "flac": true, "aac": true, "m4a": true,
	"zip": true, "gz": true, "tgz": true, "bz2": true, "xz": true, "7z": true, "rar": true,
	"br": true, "zst": true, "pdf": true, "wasm": true, "exe": true, "dll": true, "so": true,
}

func ext(p string) string { return strings.ToLower(strings.TrimPrefix(path.Ext(p), ".")) }

// IsActive reports whether a file at p is one a browser renders or runs.
func IsActive(p string) bool { return activeExt[ext(p)] }

// Hit is one rule that matched a file.
type Hit struct {
	Rule     string
	Severity Severity
	// Excerpt is the matched text in context: one line, lowercased, at most
	// excerptMax characters.
	Excerpt string
}

// Result is what a scan found in one file.
type Result struct {
	Path string
	// SHA256 fingerprints the file's bytes; empty for a file that was not
	// scanned (binary).
	SHA256 string
	// Active reports whether the file is rendered or run by a browser.
	Active bool
	Hits   []Hit
}

// Blocks reports whether the result holds a block hit in an active file —
// the only kind that holds a site.
func (r Result) Blocks() bool {
	if !r.Active {
		return false
	}
	for _, h := range r.Hits {
		if h.Severity == Block {
			return true
		}
	}
	return false
}

// Scan matches one file as it is written. It is an io.Writer that never
// fails, so it can sit beside the real destination in an io.MultiWriter.
type Scan struct {
	rs      *RuleSet
	path    string
	h       hash.Hash
	sniffed int
	binary  bool
	scanned int64
	buf     []byte
	carry   []byte
	found   []bool
	excerpt []string
}

// NewScan starts a scan of the file at path (slash-separated, relative to
// the site). It returns nil for a file that is never scanned (a known binary
// type); a nil *Scan must not be written to.
func (rs *RuleSet) NewScan(p string) *Scan {
	if binaryExt[ext(p)] {
		return nil
	}
	return &Scan{
		rs:      rs,
		path:    p,
		h:       sha256.New(),
		found:   make([]bool, len(rs.patterns)),
		excerpt: make([]string, len(rs.patterns)),
	}
}

// Write consumes the next bytes of the file.
func (s *Scan) Write(p []byte) (int, error) {
	n := len(p)
	if s.binary {
		return n, nil
	}
	if s.sniffed < sniffBytes {
		look := p
		if len(look) > sniffBytes-s.sniffed {
			look = look[:sniffBytes-s.sniffed]
		}
		s.sniffed += len(look)
		if bytes.IndexByte(look, 0) >= 0 {
			s.binary = true
			s.buf, s.carry = nil, nil
			return n, nil
		}
	}
	s.h.Write(p)
	// Past the cap only the hash moves.
	left := MaxScanBytes - s.scanned - int64(len(s.buf))
	if left <= 0 {
		return n, nil
	}
	if int64(len(p)) > left {
		p = p[:left]
	}
	for len(p) > 0 {
		take := min(chunkSize-len(s.buf), len(p))
		s.buf = append(s.buf, p[:take]...)
		p = p[take:]
		if len(s.buf) == chunkSize {
			s.flush()
		}
	}
	return n, nil
}

// flush matches the buffered chunk, with the tail of the previous one.
func (s *Scan) flush() {
	if len(s.buf) == 0 {
		return
	}
	s.scanned += int64(len(s.buf))
	text := append(s.carry, s.buf...)
	lower := bytes.ToLower(text)
	for i, p := range s.rs.patterns {
		if s.found[i] {
			continue
		}
		at, end := -1, -1
		if p.re != nil {
			if p.need != nil && !bytes.Contains(lower, p.need) {
				continue
			}
			if loc := p.re.FindIndex(lower); loc != nil {
				at, end = loc[0], loc[1]
			}
		} else if j := bytes.Index(lower, p.lit); j >= 0 {
			at, end = j, j+len(p.lit)
		}
		if at >= 0 {
			s.found[i] = true
			s.excerpt[i] = excerpt(lower, at, end)
		}
	}
	keep := min(overlap, len(text))
	s.carry = append([]byte(nil), text[len(text)-keep:]...)
	s.buf = s.buf[:0]
}

// Skipped reports whether the scan has decided the file is binary, after
// which nothing more it is given matters.
func (s *Scan) Skipped() bool { return s.binary }

// Result finishes the scan and evaluates the rules.
func (s *Scan) Result() Result {
	r := Result{Path: s.path, Active: IsActive(s.path)}
	if s.binary {
		return r
	}
	s.flush()
	r.SHA256 = hex.EncodeToString(s.h.Sum(nil))
	e := ext(s.path)
	for _, cr := range s.rs.rules {
		if cr.files != nil && !cr.files[e] {
			continue
		}
		ok := true
		for _, i := range cr.all {
			if !s.found[i] {
				ok = false
				break
			}
		}
		if !ok {
			continue
		}
		ex := ""
		if len(cr.any) > 0 {
			ok = false
			for _, i := range cr.any {
				if s.found[i] {
					ok, ex = true, s.excerpt[i]
					break
				}
			}
			if !ok {
				continue
			}
		} else {
			ex = s.excerpt[cr.all[0]]
		}
		r.Hits = append(r.Hits, Hit{Rule: cr.ID, Severity: cr.Severity, Excerpt: ex})
	}
	return r
}

// excerpt is the match with some context around it, as one clean line.
func excerpt(text []byte, at, end int) string {
	from := max(0, at-40)
	to := min(len(text), end+60)
	// don't cut a UTF-8 sequence in half at either end
	for from > 0 && !utf8.RuneStart(text[from]) {
		from--
	}
	for to < len(text) && !utf8.RuneStart(text[to]) {
		to++
	}
	return Clean(string(text[from:to]))
}

// Clean makes untrusted text safe to store and show on one line: valid
// UTF-8, no control characters, runs of space collapsed, at most excerptMax
// characters.
func Clean(s string) string {
	s = strings.ToValidUTF8(s, "?")
	s = strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) || unicode.IsControl(r) {
			return ' '
		}
		return r
	}, s)
	s = strings.Join(strings.Fields(s), " ")
	if utf8.RuneCountInString(s) > excerptMax {
		s = string([]rune(s)[:excerptMax])
	}
	return s
}

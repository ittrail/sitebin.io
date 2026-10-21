package abuse

import (
	"bytes"
	"html"
	"strings"
	"unicode/utf8"
)

// decodedMark prefixes the excerpt of a match found only in the decoded
// text: the operator should see that the file hid the signature.
const decodedMark = "decoded: "

// decode is the text as a browser acts on it, as far as a kit can hide a
// signature behind one character encoded: HTML character references (in
// attributes and text), JavaScript string escapes, and percent-encoding
// (which the URL parser decodes in hosts and servers in paths). It returns
// nil when there is nothing to decode.
//
// A character reference that would produce < or > stays as written: an
// entity-encoded angle bracket is how a page shows code, and the parser never
// turns it into markup. A JavaScript escape or a percent-encoded bracket can
// become markup, through document.write, innerHTML or unescape, so those are
// decoded.
//
// One round of each, in that order: an encoding that decodes into another
// encoding is not followed further.
func decode(text []byte) []byte {
	if bytes.IndexAny(text, `&\%`) < 0 {
		return nil
	}
	out := decodePercent(decodeJSEscapes(decodeReferences(text)))
	if bytes.Equal(out, text) {
		return nil
	}
	return out
}

func isRefByte(c byte) bool {
	return c == '#' || '0' <= c && c <= '9' || 'a' <= c && c <= 'z' || 'A' <= c && c <= 'Z'
}

// decodeReferences decodes HTML character references (&#58; &#x3a; &colon;
// and the legacy forms without a semicolon), except those that would produce
// < or >.
func decodeReferences(text []byte) []byte {
	if bytes.IndexByte(text, '&') < 0 {
		return text
	}
	out := make([]byte, 0, len(text))
	for i := 0; i < len(text); {
		if text[i] != '&' {
			out = append(out, text[i])
			i++
			continue
		}
		j := i + 1
		for j < len(text) && isRefByte(text[j]) {
			j++
		}
		if j < len(text) && text[j] == ';' {
			j++
		}
		ref := string(text[i:j])
		if u := html.UnescapeString(ref); u != ref && !strings.ContainsAny(u, "<>") {
			out = append(out, u...)
		} else {
			out = append(out, ref...)
		}
		i = j
	}
	return out
}

// decodeJSEscapes decodes \xHH, \uHHHH and \u{H…} as a JavaScript string
// literal does. An escaped backslash is a backslash, never the start of an
// escape.
func decodeJSEscapes(text []byte) []byte {
	if bytes.IndexByte(text, '\\') < 0 {
		return text
	}
	out := make([]byte, 0, len(text))
	for i := 0; i < len(text); i++ {
		c := text[i]
		if c != '\\' || i+1 == len(text) {
			out = append(out, c)
			continue
		}
		switch text[i+1] {
		case '\\':
			out = append(out, '\\', '\\')
			i++
			continue
		case 'x':
			if v, ok := hexValue(text[i+2:], 2); ok {
				out = utf8.AppendRune(out, rune(v))
				i += 3
				continue
			}
		case 'u':
			if i+2 < len(text) && text[i+2] == '{' {
				digits := text[i+3 : min(len(text), i+3+7)]
				if n := bytes.IndexByte(digits, '}'); n > 0 {
					if v, ok := hexValue(digits, n); ok && v <= utf8.MaxRune {
						out = utf8.AppendRune(out, rune(v))
						i += 3 + n
						continue
					}
				}
			} else if v, ok := hexValue(text[i+2:], 4); ok {
				out = utf8.AppendRune(out, rune(v))
				i += 5
				continue
			}
		}
		out = append(out, c)
	}
	return out
}

// decodePercent decodes %HH.
func decodePercent(text []byte) []byte {
	if bytes.IndexByte(text, '%') < 0 {
		return text
	}
	out := make([]byte, 0, len(text))
	for i := 0; i < len(text); i++ {
		if text[i] == '%' {
			if v, ok := hexValue(text[i+1:], 2); ok {
				out = append(out, byte(v))
				i += 2
				continue
			}
		}
		out = append(out, text[i])
	}
	return out
}

// hexValue reads exactly n hex digits from the start of b.
func hexValue(b []byte, n int) (int, bool) {
	if len(b) < n {
		return 0, false
	}
	v := 0
	for _, c := range b[:n] {
		switch {
		case '0' <= c && c <= '9':
			v = v<<4 | int(c-'0')
		case 'a' <= c && c <= 'f':
			v = v<<4 | int(c-'a'+10)
		case 'A' <= c && c <= 'F':
			v = v<<4 | int(c-'A'+10)
		default:
			return 0, false
		}
	}
	return v, true
}

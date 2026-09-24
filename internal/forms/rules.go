// Package forms is the logic of Sitebin's email forms, with no HTTP handlers
// and no store access: the rules for a form's settings, the recipient's link
// tokens, the ordered body parser, message building, SMTP delivery and the
// captcha. internal/httpapi wires it to requests and to the store.
//
// Design: docs/superpowers/specs/2026-09-24-site-forms-design.md.
package forms

import (
	"errors"
	"fmt"
	"net/mail"
	"strings"
	"unicode"
	"unicode/utf8"
)

// MaxNameRunes caps a form name, which is also the From display name.
const MaxNameRunes = 60

const maxRedirectLen = 512

var (
	ErrBadName      = errors.New("the form name must be 1 to 60 characters of plain text")
	ErrBadRecipient = errors.New("the recipient must be one email address, such as office@example.com")
	ErrBadRedirect  = errors.New("the thank-you page must be a path on this site, such as /thanks.html")
)

func hasControl(s string) bool { return strings.IndexFunc(s, unicode.IsControl) >= 0 }

// CleanName validates a form name. It becomes the From display name of every
// submission, so control characters, CR and LF above all, are refused rather
// than stripped: a stripped name would silently differ from what the owner
// typed.
func CleanName(s string) (string, error) {
	if hasControl(s) {
		return "", ErrBadName
	}
	s = strings.TrimSpace(s)
	if n := utf8.RuneCountInString(s); n == 0 || n > MaxNameRunes || !utf8.ValidString(s) {
		return "", ErrBadName
	}
	return s, nil
}

// CleanRecipient validates a recipient: exactly one bare address.
func CleanRecipient(s string) (string, error) {
	s = strings.TrimSpace(s)
	a, err := mail.ParseAddress(s)
	if err != nil || a.Name != "" || a.Address != s || hasControl(s) {
		return "", ErrBadRecipient
	}
	return s, nil
}

// CleanRedirect validates a thank-you path; empty means the default page. It
// must be a path on the same site, so the endpoint can never redirect a
// visitor anywhere else.
func CleanRedirect(s string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", nil
	}
	if len(s) > maxRedirectLen || !strings.HasPrefix(s, "/") || strings.HasPrefix(s, "//") ||
		strings.ContainsRune(s, '\\') || hasControl(s) {
		return "", ErrBadRedirect
	}
	return s, nil
}

// SnippetOptions describes the form whose HTML Snippet writes.
type SnippetOptions struct {
	Key     string
	Captcha bool
	Files   bool
	// SiteQuery is "?_site=<view id>" on a path-view instance, where the page
	// lives on the main domain and the endpoint cannot tell sites apart by
	// host. Empty everywhere else.
	SiteQuery string
}

// Snippet is the HTML a site owner pastes into a page. The edit page, the API
// and MCP all hand out this one string.
//
// The honeypot is moved off-screen rather than marked hidden: form bots skip
// hidden inputs and fill the visible-looking ones.
func Snippet(o SnippetOptions) string {
	action := "/_sitebin/forms/" + o.Key
	var b strings.Builder
	enctype := ""
	if o.Files {
		enctype = ` enctype="multipart/form-data"`
	}
	fmt.Fprintf(&b, "<form action=\"%s%s\" method=\"post\"%s>\n", action, o.SiteQuery, enctype)
	b.WriteString("  <label>Name <input name=\"name\" required></label>\n")
	b.WriteString("  <label>Email <input name=\"email\" type=\"email\" required></label>\n")
	b.WriteString("  <label>Message <textarea name=\"message\" required></textarea></label>\n")
	if o.Files {
		b.WriteString("  <label>Attachments <input name=\"attachments\" type=\"file\" multiple></label>\n")
	}
	b.WriteString("  <input name=\"_gotcha\" tabindex=\"-1\" autocomplete=\"off\" aria-hidden=\"true\" style=\"position:absolute;left:-9999px\">\n")
	if o.Captcha {
		fmt.Fprintf(&b, "  <altcha-widget challenge=\"%s/challenge%s\"></altcha-widget>\n", action, o.SiteQuery)
	}
	b.WriteString("  <button type=\"submit\">Send</button>\n</form>\n")
	if o.Captcha {
		b.WriteString("<script type=\"module\" src=\"/_sitebin/altcha.js\"></script>\n")
	}
	return b.String()
}

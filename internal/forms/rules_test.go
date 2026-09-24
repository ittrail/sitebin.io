package forms

import (
	"errors"
	"strings"
	"testing"
)

func TestCleanName(t *testing.T) {
	for _, ok := range []string{"Contact", "  Kontakt – Anfrage ✉  ", strings.Repeat("ä", 60)} {
		if got, err := CleanName(ok); err != nil || got != strings.TrimSpace(ok) {
			t.Errorf("CleanName(%q) = %q, %v", ok, got, err)
		}
	}
	for _, bad := range []string{"", "   ", strings.Repeat("a", 61), "Contact\r\nBcc: x@evil.example", "a\x00b", "tab\there", "Contact\r\n", "\tContact"} {
		if _, err := CleanName(bad); !errors.Is(err, ErrBadName) {
			t.Errorf("CleanName(%q) accepted", bad)
		}
	}
}

func TestCleanRecipient(t *testing.T) {
	if got, err := CleanRecipient("  office@example.com "); err != nil || got != "office@example.com" {
		t.Errorf("plain address = %q, %v", got, err)
	}
	for _, bad := range []string{"", "office", "Office <office@example.com>", "<office@example.com>",
		"a@example.com, b@example.com", "a@example.com\r\nBcc: x@evil.example"} {
		if _, err := CleanRecipient(bad); !errors.Is(err, ErrBadRecipient) {
			t.Errorf("CleanRecipient(%q) accepted", bad)
		}
	}
}

// Review Focus 5: a thank-you path with a query string or fragment is a
// perfectly ordinary thing to want.
func TestCleanRedirect(t *testing.T) {
	for _, ok := range []string{"", "/thanks.html", "/danke.html?sent=1#top", "/de/kontakt/danke/"} {
		if got, err := CleanRedirect(ok); err != nil || got != ok {
			t.Errorf("CleanRedirect(%q) = %q, %v", ok, got, err)
		}
	}
	for _, bad := range []string{"thanks.html", "//evil.example/x", "https://evil.example/", "/\\evil.example",
		"/a\nb", "/" + strings.Repeat("a", 512)} {
		if _, err := CleanRedirect(bad); !errors.Is(err, ErrBadRedirect) {
			t.Errorf("CleanRedirect(%q) accepted", bad)
		}
	}
}

func TestSnippetPlain(t *testing.T) {
	s := Snippet(SnippetOptions{Key: "k7f3m2q9xaw4npd6"})
	for _, want := range []string{`action="/_sitebin/forms/k7f3m2q9xaw4npd6"`, `method="post"`, `name="email"`, `name="_gotcha"`} {
		if !strings.Contains(s, want) {
			t.Errorf("snippet lacks %s:\n%s", want, s)
		}
	}
	for _, not := range []string{"enctype", "altcha", `type="file"`, " hidden"} {
		if strings.Contains(s, not) {
			t.Errorf("plain snippet contains %s:\n%s", not, s)
		}
	}
}

func TestSnippetWithEverything(t *testing.T) {
	s := Snippet(SnippetOptions{Key: "k1", Captcha: true, Files: true, SiteQuery: "?_site=v1"})
	for _, want := range []string{
		`action="/_sitebin/forms/k1?_site=v1"`,
		`enctype="multipart/form-data"`,
		`type="file" multiple`,
		`<altcha-widget challenge="/_sitebin/forms/k1/challenge?_site=v1"></altcha-widget>`,
		`<script type="module" src="/_sitebin/altcha.js"></script>`,
	} {
		if !strings.Contains(s, want) {
			t.Errorf("snippet lacks %s:\n%s", want, s)
		}
	}
}

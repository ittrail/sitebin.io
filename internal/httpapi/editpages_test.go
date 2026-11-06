package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ittrail/sitebin.io/internal/ext"
)

// editPagesFake is a provider that serves its own edit page (the enterprise
// workspace) when serve is set.
type editPagesFake struct {
	*fakeProvider
	serve bool
	asked int
}

func (f *editPagesFake) ServeEditPage(w http.ResponseWriter, r *http.Request) bool {
	f.asked++
	if !f.serve {
		return false
	}
	w.Write([]byte("the provider's page"))
	return true
}

func editIDOf(editURL string) string { return editURL[strings.LastIndex(editURL, "/")+1:] }

// Without a provider — the community build — /e/{editID} is edit.html, as
// it always was.
func TestEditPageWithoutAProviderIsEditHTML(t *testing.T) {
	e := newEnv(t, nil)
	c := e.createSite(t, nil, map[string]string{"index.html": "<p>hi</p>"})
	w := e.public(t, httptest.NewRequest("GET", "/e/"+editIDOf(c.EditURL), nil))
	if w.Code != 200 || !strings.Contains(w.Body.String(), "<html>edit</html>") {
		t.Fatalf("edit page = %d\n%s", w.Code, w.Body)
	}
}

// A provider that implements ext.EditPages is asked, only for a site that
// exists, and its answer decides which page is served.
func TestEditPageAsksTheProviderFirst(t *testing.T) {
	e := newEnv(t, nil)
	c := e.createSite(t, nil, map[string]string{"index.html": "<p>hi</p>"})
	f := &editPagesFake{fakeProvider: &fakeProvider{enabled: true}}
	ext.Register(f)
	t.Cleanup(ext.Reset)

	w := e.public(t, httptest.NewRequest("GET", "/e/"+editIDOf(c.EditURL), nil))
	if !strings.Contains(w.Body.String(), "<html>edit</html>") || f.asked != 1 {
		t.Fatalf("a provider that declines must leave edit.html (asked %d):\n%s", f.asked, w.Body)
	}
	f.serve = true
	w = e.public(t, httptest.NewRequest("GET", "/e/"+editIDOf(c.EditURL), nil))
	if w.Body.String() != "the provider's page" {
		t.Fatalf("the provider's page was not served:\n%s", w.Body)
	}
	// An unknown site is a 404 before the provider is asked.
	asked := f.asked
	w = e.public(t, httptest.NewRequest("GET", "/e/aaaaaaaaaaaaaaaaaaaaaaaaaa", nil))
	if w.Code != http.StatusNotFound || f.asked != asked {
		t.Errorf("unknown site = %d, provider asked %d more times", w.Code, f.asked-asked)
	}
}

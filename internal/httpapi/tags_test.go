package httpapi

import (
	"bytes"
	"encoding/json"
	"errors"
	"mime/multipart"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/ittrail/sitebin.io/internal/ext"
	"github.com/ittrail/sitebin.io/internal/store"
)

func payloadTags(t *testing.T, w *httptest.ResponseRecorder) []string {
	t.Helper()
	var m struct {
		Tags []string `json:"tags"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &m); err != nil {
		t.Fatalf("payload: %v (%s)", err, w.Body)
	}
	return m.Tags
}

func (e *env) storedTags(t *testing.T, edit string) []string {
	t.Helper()
	site, err := e.st.ByEditID(edit)
	if err != nil {
		t.Fatal(err)
	}
	return site.Meta.Tags
}

func TestPutSetsReplacesAndClearsTags(t *testing.T) {
	e := newEnv(t, nil)
	c := e.createSite(t, nil, map[string]string{"index.html": "x"})
	edit := editIDFrom(t, c.EditURL)
	for _, tc := range []struct {
		body string
		want []string
	}{
		{`{"tags":["b","A"]}`, []string{"A", "b"}},
		{`{"tags":["c"]}`, []string{"c"}},
		{`{"tags":[]}`, []string{}},
	} {
		w := e.putSettings(t, edit, c.EditPassword, tc.body)
		if w.Code != 200 {
			t.Fatalf("PUT %s: %d %s", tc.body, w.Code, w.Body)
		}
		if got := payloadTags(t, w); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("PUT %s: tags = %v, want %v", tc.body, got, tc.want)
		}
	}
}

func TestPutWithoutTagsLeavesThem(t *testing.T) {
	e := newEnv(t, nil)
	c := e.createSite(t, nil, map[string]string{"index.html": "x"})
	edit := editIDFrom(t, c.EditURL)
	if w := e.putSettings(t, edit, c.EditPassword, `{"tags":["x"]}`); w.Code != 200 {
		t.Fatalf("set: %d %s", w.Code, w.Body)
	}
	w := e.putSettings(t, edit, c.EditPassword, `{"name":"n"}`)
	if w.Code != 200 {
		t.Fatalf("name: %d %s", w.Code, w.Body)
	}
	if got := payloadTags(t, w); !reflect.DeepEqual(got, []string{"x"}) {
		t.Errorf("tags = %v, want [x]", got)
	}
}

func TestPutBadTagsIs400AndKeepsOld(t *testing.T) {
	e := newEnv(t, nil)
	c := e.createSite(t, nil, map[string]string{"index.html": "x"})
	edit := editIDFrom(t, c.EditURL)
	if w := e.putSettings(t, edit, c.EditPassword, `{"tags":["keep"]}`); w.Code != 200 {
		t.Fatalf("set: %d %s", w.Code, w.Body)
	}
	w := e.putSettings(t, edit, c.EditPassword, `{"tags":["a,b"]}`)
	if w.Code != 400 || !strings.Contains(w.Body.String(), "without commas") {
		t.Errorf("comma tag: %d %s", w.Code, w.Body)
	}
	if got := e.storedTags(t, edit); !reflect.DeepEqual(got, []string{"keep"}) {
		t.Errorf("after bad tag: %v", got)
	}
	var many []string
	for i := 0; i < 11; i++ {
		many = append(many, string(rune('a'+i)))
	}
	b, _ := json.Marshal(map[string]any{"tags": many})
	w = e.putSettings(t, edit, c.EditPassword, string(b))
	if w.Code != 400 || !strings.Contains(w.Body.String(), "at most 10") {
		t.Errorf("11 tags: %d %s", w.Code, w.Body)
	}
	if got := e.storedTags(t, edit); !reflect.DeepEqual(got, []string{"keep"}) {
		t.Errorf("after too many: %v", got)
	}
}

func TestPayloadReportsUntaggedAsEmptyList(t *testing.T) {
	e := newEnv(t, nil)
	c := e.createSite(t, nil, map[string]string{"index.html": "x"})
	w := e.public(t, authed(httptest.NewRequest("GET", "/api/sites/"+editIDFrom(t, c.EditURL), nil), c.EditPassword))
	if !strings.Contains(w.Body.String(), `"tags":[]`) {
		t.Errorf("payload lacks \"tags\":[]: %s", w.Body)
	}
}

func multipartCreate(t *testing.T, e *env, fields [][2]string) *httptest.ResponseRecorder {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	for _, f := range fields {
		mw.WriteField(f[0], f[1])
	}
	p, _ := mw.CreateFormFile("files", "index.html")
	p.Write([]byte("x"))
	mw.Close()
	req := httptest.NewRequest("POST", "/api/sites", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	return e.public(t, req)
}

func TestCreateWithTags(t *testing.T) {
	e := newEnv(t, nil)
	w := multipartCreate(t, e, [][2]string{{"tags", "a,b"}, {"tags", "c"}})
	if w.Code != 201 {
		t.Fatalf("multipart create: %d %s", w.Code, w.Body)
	}
	if got := payloadTags(t, w); !reflect.DeepEqual(got, []string{"a", "b", "c"}) {
		t.Errorf("multipart tags = %v", got)
	}

	req := httptest.NewRequest("POST", "/api/sites", strings.NewReader(`{"tags":["x"]}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	w = e.public(t, req)
	if w.Code != 201 {
		t.Fatalf("JSON create: %d %s", w.Code, w.Body)
	}
	if got := payloadTags(t, w); !reflect.DeepEqual(got, []string{"x"}) {
		t.Errorf("JSON tags = %v", got)
	}
}

func TestCreateWithBadTagsLeavesNothing(t *testing.T) {
	e := newEnv(t, nil)
	w := multipartCreate(t, e, [][2]string{{"tags", strings.Repeat("a", 31)}})
	if w.Code != 400 {
		t.Fatalf("bad tag at create: %d %s", w.Code, w.Body)
	}
	all, err := e.st.AllSites()
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 0 {
		t.Errorf("a refused creation left %d site(s) behind", len(all))
	}
}

// ---- the seam ----

func TestSiteServiceInfoCarriesTags(t *testing.T) {
	e := newEnv(t, nil)
	c := e.createSite(t, nil, map[string]string{"index.html": "x"})
	edit := editIDFrom(t, c.EditURL)
	if w := e.putSettings(t, edit, c.EditPassword, `{"tags":["b","A"]}`); w.Code != 200 {
		t.Fatalf("set: %d %s", w.Code, w.Body)
	}
	info, ok := e.api.SiteService().Info(c.ID)
	if !ok {
		t.Fatal("Info: site not found")
	}
	if !reflect.DeepEqual(info.Tags, []string{"A", "b"}) {
		t.Errorf("Info.Tags = %v", info.Tags)
	}
}

func TestSiteServiceUpdateTags(t *testing.T) {
	e := newEnv(t, nil)
	c := e.createSite(t, nil, map[string]string{"index.html": "x"})
	edit := editIDFrom(t, c.EditURL)
	ss := e.api.SiteService()
	if w := e.putSettings(t, edit, c.EditPassword, `{"tags":["a"]}`); w.Code != 200 {
		t.Fatalf("set: %d %s", w.Code, w.Body)
	}

	err := ss.UpdateTags(c.ID, func(cur []string) ([]string, error) {
		if !reflect.DeepEqual(cur, []string{"a"}) {
			t.Errorf("callback got %v, want [a]", cur)
		}
		return []string{"a", "b"}, nil
	})
	if err != nil {
		t.Fatalf("UpdateTags: %v", err)
	}
	if got := e.storedTags(t, edit); !reflect.DeepEqual(got, []string{"a", "b"}) {
		t.Errorf("stored = %v", got)
	}

	many := make([]string, 11)
	for i := range many {
		many[i] = "t" + string(rune('a'+i))
	}
	err = ss.UpdateTags(c.ID, func([]string) ([]string, error) { return many, nil })
	if !errors.Is(err, store.ErrTooManyTags) {
		t.Errorf("11 tags: %v, want ErrTooManyTags", err)
	}
	if got := e.storedTags(t, edit); !reflect.DeepEqual(got, []string{"a", "b"}) {
		t.Errorf("a refused list changed the site: %v", got)
	}

	boom := errors.New("boom")
	err = ss.UpdateTags(c.ID, func([]string) ([]string, error) { return []string{"zzz"}, boom })
	if err != boom {
		t.Errorf("callback error: %v, want it unchanged", err)
	}
	if got := e.storedTags(t, edit); !reflect.DeepEqual(got, []string{"a", "b"}) {
		t.Errorf("a callback error wrote: %v", got)
	}

	if err := ss.UpdateTags(c.ID, func([]string) ([]string, error) { return nil, nil }); err != nil {
		t.Fatalf("clear: %v", err)
	}
	if got := e.storedTags(t, edit); got != nil {
		t.Errorf("cleared tags stored as %#v, want nil", got)
	}

	err = ss.UpdateTags("nosuchsiteatallxxxxxxxxxxx", func([]string) ([]string, error) { return nil, nil })
	if !errors.Is(err, ext.ErrSiteGone) {
		t.Errorf("missing site: %v, want ErrSiteGone", err)
	}
}

func TestSiteServiceUpdateTagsSeesConcurrentWrite(t *testing.T) {
	e := newEnv(t, nil)
	c := e.createSite(t, nil, map[string]string{"index.html": "x"})
	edit := editIDFrom(t, c.EditURL)
	ss := e.api.SiteService()

	if err := ss.UpdateTags(c.ID, func([]string) ([]string, error) { return []string{"one"}, nil }); err != nil {
		t.Fatal(err)
	}
	if w := e.putSettings(t, edit, c.EditPassword, `{"tags":["one","api"]}`); w.Code != 200 {
		t.Fatalf("PUT: %d %s", w.Code, w.Body)
	}
	var seen []string
	err := ss.UpdateTags(c.ID, func(cur []string) ([]string, error) {
		seen = append([]string(nil), cur...)
		return append(cur, "two"), nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(seen, []string{"api", "one"}) {
		t.Errorf("callback saw %v, want [api one]", seen)
	}
	if got := e.storedTags(t, edit); !reflect.DeepEqual(got, []string{"api", "one", "two"}) {
		t.Errorf("stored = %v", got)
	}
}

package httpapi

import (
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
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

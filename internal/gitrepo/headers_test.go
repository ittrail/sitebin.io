package gitrepo

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
)

// Every smart HTTP answer is marked: never sniffed, never rendered.
func TestResponsesAreNeverRendered(t *testing.T) {
	NeedGit(t)
	r, dir := newBare(t)
	check := func(name string, rec *httptest.ResponseRecorder) {
		t.Helper()
		if got := rec.Header().Get("X-Content-Type-Options"); got != "nosniff" {
			t.Errorf("%s: X-Content-Type-Options = %q", name, got)
		}
		if got := rec.Header().Get("Content-Security-Policy"); got != "sandbox" {
			t.Errorf("%s: Content-Security-Policy = %q", name, got)
		}
	}
	rec := httptest.NewRecorder()
	if err := r.AdvertiseRefs(context.Background(), dir, UploadPack, "", rec); err != nil {
		t.Fatal(err)
	}
	check("advertisement", rec)
	rec = httptest.NewRecorder()
	if _, err := r.ServeRPC(context.Background(), dir, UploadPack, strings.NewReader("0000"), rec, RPCOptions{}); err != nil {
		t.Fatal(err)
	}
	check("result", rec)
	rec = httptest.NewRecorder()
	WriteRefusal(rec, UploadPack, "no")
	check("refusal", rec)
}

package analytics

import (
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"testing"
)

func TestKeepQuery(t *testing.T) {
	q, clicks := KeepQuery("utm_source=meta&utm_medium=paid-social&fbclid=ABC&lang=de&x=1", []string{"lang"})
	want := map[string]string{"utm_source": "meta", "utm_medium": "paid-social", "lang": "de"}
	if !reflect.DeepEqual(q, want) {
		t.Fatalf("q = %v", q)
	}
	if !reflect.DeepEqual(clicks, []string{"fbclid"}) {
		t.Fatalf("clicks = %v", clicks)
	}
	q, clicks = KeepQuery("twclid=1&gclid=2&Lang=de&utm_term="+strings.Repeat("é", 150), []string{"lang"})
	if _, ok := q["Lang"]; ok || q["lang"] != "" {
		t.Fatalf("extra names are exact: %v", q)
	}
	if len([]rune(q["utm_term"])) != 100 {
		t.Fatalf("value not cut at 100 runes")
	}
	if !reflect.DeepEqual(clicks, []string{"gclid", "twclid"}) {
		t.Fatalf("clicks order = %v", clicks)
	}
	if q, c := KeepQuery("gclid=X", []string{"gclid"}); len(q) != 0 || !reflect.DeepEqual(c, []string{"gclid"}) {
		t.Fatalf("click id listed as extra: q=%v clicks=%v", q, c)
	}
	if q, c := KeepQuery("", nil); q != nil || c != nil {
		t.Fatal("empty query should keep nothing")
	}
}

func TestCleanReferrer(t *testing.T) {
	own := []string{"www.morgenfertig.at", "shop.example.com/store", "blog.example.org"}
	cases := []struct{ ref, want string }{
		// The host is kept without a leading "www.", as Umami stores it.
		{"https://www.google.com/search?q=x", "google.com/search"},
		{"https://www.google.com/a#frag", "google.com/a"},
		{"https://WWW.Google.COM/Path", "google.com/Path"},
		{"http://www2.example.net/x", "www2.example.net/x"},
		{"https://wwwexample.net/x", "wwwexample.net/x"},
		{"https://www.morgenfertig.at/a", ""},
		{"https://WWW.MorgenFertig.at:8443/a", ""},
		// The own-address check ignores "www." on both sides.
		{"https://morgenfertig.at/a", ""},
		{"https://www.blog.example.org/a", ""},
		{"https://shop.example.com/other", ""},
		{"android-app://com.google.android.gm/", "android-app://com.google.android.gm"},
		// A scheme-relative referrer names no scheme: unparseable.
		{"//evil.example/x", ""},
		{"//www.google.com/search", ""},
		{"", ""},
		{"not a url", ""},
	}
	for _, c := range cases {
		if got := CleanReferrer(c.ref, own); got != c.want {
			t.Errorf("CleanReferrer(%q) = %q, want %q", c.ref, got, c.want)
		}
	}
	if got := CleanReferrer("android-app://com.google.android.gm/", nil); got != "android-app://com.google.android.gm" {
		t.Errorf("got %q", got)
	}
	if got := CleanReferrer("https://x.com/"+strings.Repeat("a", 500), nil); len(got) != 200 {
		t.Errorf("len = %d", len(got))
	}
	if got := CleanReferrer("https://a.com/x", []string{"a.com:8080"}); got != "" {
		t.Errorf("own with port: %q", got)
	}
}

func TestCleanPath(t *testing.T) {
	if got := CleanPath("/a/b?x=1#y"); got != "/a/b" {
		t.Errorf("got %q", got)
	}
	p := "/" + strings.Repeat("é", 200) // 401 bytes
	got := CleanPath(p)
	if len(got) > 300 || len(got) < 298 || !strings.HasPrefix(p, got) {
		t.Errorf("len = %d", len(got))
	}
	if strings.ToValidUTF8(got, "") != got {
		t.Error("cut inside a rune")
	}
}

func TestCleanEvent(t *testing.T) {
	data := map[string]any{}
	for i := 0; i < 21; i++ {
		data[fmt.Sprintf("k%02d", i)] = "v"
	}
	data["nested"] = map[string]any{"a": 1}
	data["list"] = []any{1}
	data["nil"] = nil
	data[strings.Repeat("k", 51)] = "x"
	name, out, ok := CleanEvent("signup", data)
	if !ok || name != "signup" {
		t.Fatal("expected ok")
	}
	if len(out) != 20 {
		t.Fatalf("kept %d", len(out))
	}
	var keys []string
	for k := range out {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	// Sorted survivors are k00..k20 (then list, nested, nil as dropped types): first 20 are k00..k19.
	if keys[0] != "k00" || keys[19] != "k19" {
		t.Fatalf("keys = %v", keys)
	}
	_, out, _ = CleanEvent("e", map[string]any{
		"s": strings.Repeat("é", 300), "n": 3.5, "i": 4, "j": json.Number("7"), "b": true,
		"m": map[string]any{}, "z": nil,
	})
	if len([]rune(out["s"].(string))) != 200 {
		t.Error("string not cut at 200")
	}
	if len(out) != 5 {
		t.Errorf("out = %v", out)
	}
	if _, _, ok := CleanEvent("", nil); ok {
		t.Error("empty name")
	}
	if _, _, ok := CleanEvent(strings.Repeat("n", 51), nil); ok {
		t.Error("51-char name")
	}
	if _, _, ok := CleanEvent(strings.Repeat("n", 50), nil); !ok {
		t.Error("50-char name")
	}
}

func TestSimpleCleaners(t *testing.T) {
	if got := CleanTitle(strings.Repeat("ü", 250)); len([]rune(got)) != 200 {
		t.Error("title")
	}
	if got := CleanTag(strings.Repeat("t", 60)); len(got) != 50 {
		t.Error("tag")
	}
	for in, want := range map[string]string{"de-AT": "de-AT", "en": "en", "zh-Hant-TW": "zh-Hant-TW", "": "", "de_AT": "", "*": "", "en;q=0.9": "", "<x>": ""} {
		if got := CleanLanguage(in); got != want {
			t.Errorf("CleanLanguage(%q) = %q", in, got)
		}
	}
	if got := CleanLanguage(strings.Repeat("a", 40)); len(got) != 35 {
		t.Errorf("language cut: %d", len(got))
	}
	for in, want := range map[string]string{"1920x1080": "1920x1080", "99999x10": "99999x10", "1x1": "", "123456x10": "", "axb": "", "1920X1080": "", "": ""} {
		if got := CleanScreen(in); got != want {
			t.Errorf("CleanScreen(%q) = %q", in, got)
		}
	}
}

func TestRecordKeys(t *testing.T) {
	full := Record{T: 1, K: "p", S: "srv", A: "a", P: "/", Ti: "t", Q: map[string]string{"a": "b"}, Ck: []string{"gclid"},
		R: "r", N: "n", D: map[string]any{"a": 1}, V: "v", Vi: "vi", B: "b", O: "o", Dv: "dv", Sc: "1x1", L: "l",
		C: "c", Rg: "rg", Ct: "ct", Tg: "tg", Vr: "vr"}
	b, err := json.Marshal(full)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	var got []string
	for k := range m {
		got = append(got, k)
	}
	sort.Strings(got)
	want := strings.Fields("t k s a p ti q ck r n d v vi b o dv sc l c rg ct tg vr")
	sort.Strings(want)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("keys = %v", got)
	}
	b, _ = json.Marshal(Record{})
	if string(b) != "{}" {
		t.Fatalf("empty record = %s", b)
	}
}

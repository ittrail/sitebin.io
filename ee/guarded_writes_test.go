//go:build ee

package ee

import (
	"net/http"
	"net/url"
	"slices"
	"strings"
	"testing"
)

// Final review C1: the dashboard's rotate, rename and tag writes pass their
// owner check, then a transfer commits before the core writes. The core's
// owner-guarded writers refuse, and nothing reaches the recipient's site --
// above all no edit password reaches the sender.
func TestDashboardWritesRefuseATransferThatLandsMidRequest(t *testing.T) {
	e := newShareEnv(t)
	const recipient = "acct-recipient"
	reset := func() {
		info := e.fs.infos[shareSite]
		info.Owner, info.Name, info.Tags = e.acc.ID, "Before", []string{"keep"}
		e.fs.infos[shareSite] = info
		e.fs.setOwner(shareSite, e.acc.ID)
		e.fs.rotated = nil
	}
	// The transfer lands between the handler's check and the write.
	transferNow := func(id string) {
		info := e.fs.infos[id]
		info.Owner, info.Tags, info.Admins = recipient, nil, nil
		e.fs.infos[id] = info
		e.fs.setOwner(id, recipient)
	}

	cases := []struct {
		path string
		form url.Values
	}{
		{"/rotate", nil},
		{"/name", url.Values{"name": {"Sender's label"}}},
		{"/tags/add", url.Values{"tag": {"sender-private"}}},
		{"/tags/remove", url.Values{"tag": {"keep"}}},
	}
	for _, c := range cases {
		reset()
		e.fs.onGuarded = transferNow
		w := e.postTo(e.cookie, e.acc, "/account/sites/"+shareSite+c.path, c.form)
		e.fs.onGuarded = nil
		if w.Code != http.StatusForbidden {
			t.Errorf("%s across a transfer = %d, want 403", c.path, w.Code)
		}
		if len(e.fs.rotated) != 0 || strings.Contains(w.Body.String(), "freshEditPw") {
			t.Errorf("%s: the sender was handed a password to the recipient's site", c.path)
		}
		info := e.fs.infos[shareSite]
		if info.Owner != recipient || info.Name != "Before" || len(info.Tags) != 0 {
			t.Errorf("%s wrote to the recipient's site: owner %q, name %q, tags %v", c.path, info.Owner, info.Name, info.Tags)
		}
	}

	// Without a transfer the same requests go through, so the refusals
	// above are the guard's.
	reset()
	if w := e.postTo(e.cookie, e.acc, "/account/sites/"+shareSite+"/rotate", nil); w.Code != http.StatusOK || !slices.Equal(e.fs.rotated, []string{shareSite}) {
		t.Errorf("the owner's rotate = %d, rotated %v", w.Code, e.fs.rotated)
	}
	if w := e.postTo(e.cookie, e.acc, "/account/sites/"+shareSite+"/name", url.Values{"name": {"After"}}); w.Code != http.StatusSeeOther || e.fs.infos[shareSite].Name != "After" {
		t.Errorf("the owner's rename = %d, name %q", w.Code, e.fs.infos[shareSite].Name)
	}
	if w := e.postTo(e.cookie, e.acc, "/account/sites/"+shareSite+"/tags/add", url.Values{"tag": {"new"}}); w.Code != http.StatusSeeOther || !slices.Equal(e.fs.infos[shareSite].Tags, []string{"keep", "new"}) {
		t.Errorf("the owner's tag = %d, tags %v", w.Code, e.fs.infos[shareSite].Tags)
	}
}

// An admin's rename is decided against the owner who shared the site: after
// a transfer (which ends every share) it is refused too.
func TestDashboardAdminRenameRefusesATransferThatLandsMidRequest(t *testing.T) {
	e := newShareEnv(t)
	e.shared(nil)
	info := e.fs.infos[shareSite]
	info.Name = "Before"
	e.fs.infos[shareSite] = info
	e.fs.onGuarded = func(id string) {
		info := e.fs.infos[id]
		info.Owner, info.Admins = "acct-recipient", nil
		e.fs.infos[id] = info
		e.fs.setOwner(id, "acct-recipient")
	}
	w := e.postTo(e.toCookie, e.to, "/account/sites/"+shareSite+"/name", url.Values{"name": {"Admin's label"}})
	if w.Code != http.StatusForbidden {
		t.Errorf("the admin's rename across a transfer = %d, want 403", w.Code)
	}
	if got := e.fs.infos[shareSite].Name; got != "Before" {
		t.Errorf("the admin renamed the recipient's site to %q", got)
	}
}

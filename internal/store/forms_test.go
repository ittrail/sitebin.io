package store

import (
	"errors"
	"testing"
)

func formSite(t *testing.T) (*Store, *Site) {
	t.Helper()
	s := newTestStore(t)
	site, _, err := s.Create()
	if err != nil {
		t.Fatal(err)
	}
	return s, site
}

func addForm(t *testing.T, s *Store, site *Site, name, to string) Form {
	t.Helper()
	f, err := s.AddForm(site, Form{Name: name, Recipient: to}, 10)
	if err != nil {
		t.Fatalf("AddForm: %v", err)
	}
	return f
}

func TestAddFormIsPendingWithAKey(t *testing.T) {
	s, site := formSite(t)
	f, err := s.AddForm(site, Form{Name: "Contact", Recipient: "a@example.com", Captcha: true, Redirect: "/thanks.html",
		// fields AddForm must ignore:
		Key: "chosen", Status: FormActive, Seq: 9}, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(f.Key) != FormKeyLen || f.Key == "chosen" {
		t.Errorf("key %q: want a fresh %d-character key", f.Key, FormKeyLen)
	}
	if f.Status != FormPending || f.Seq != 0 || f.CreatedAt.IsZero() || f.ConfirmedAt != nil {
		t.Errorf("new form = %+v, want pending, seq 0, created now", f)
	}
	if !f.Captcha || f.Redirect != "/thanks.html" || f.Name != "Contact" {
		t.Errorf("settings not kept: %+v", f)
	}
	again, _ := s.ByViewID(site.ViewID)
	if len(again.Meta.Forms) != 1 || again.Meta.Forms[0].Key != f.Key {
		t.Errorf("not persisted: %+v", again.Meta.Forms)
	}
}

func TestAddFormRefusesAtTheLimit(t *testing.T) {
	s, site := formSite(t)
	if _, err := s.AddForm(site, Form{Name: "A", Recipient: "a@example.com"}, 1); err != nil {
		t.Fatal(err)
	}
	_, err := s.AddForm(site, Form{Name: "B", Recipient: "b@example.com"}, 1)
	if !errors.Is(err, ErrTooManyForms) {
		t.Fatalf("second form at limit 1: err = %v, want ErrTooManyForms", err)
	}
	if _, err := s.AddForm(site, Form{Name: "C", Recipient: "c@example.com"}, 0); !errors.Is(err, ErrTooManyForms) {
		t.Errorf("limit 0 must refuse: %v", err)
	}
}

func TestFormKeysAreUniqueWithinASite(t *testing.T) {
	s, site := formSite(t)
	seen := map[string]bool{}
	for i := 0; i < 10; i++ {
		f := addForm(t, s, site, "F", "a@example.com")
		if seen[f.Key] {
			t.Fatalf("duplicate key %q", f.Key)
		}
		seen[f.Key] = true
	}
}

func TestUpdateFormRecipientResetsConsent(t *testing.T) {
	s, site := formSite(t)
	f := addForm(t, s, site, "Contact", "a@example.com")
	if _, err := s.ConfirmForm(site, f.Key, "a@example.com", 0); err != nil {
		t.Fatal(err)
	}
	to := "b@example.com"
	g, changed, err := s.UpdateForm(site, f.Key, FormPatch{Recipient: &to})
	if err != nil || !changed {
		t.Fatalf("UpdateForm: changed=%v err=%v", changed, err)
	}
	if g.Status != FormPending || g.Seq != 1 || g.ConfirmedAt != nil || g.Recipient != to {
		t.Errorf("after recipient change = %+v, want pending, seq 1, unconfirmed", g)
	}
}

func TestUpdateFormOtherFieldsKeepConsent(t *testing.T) {
	s, site := formSite(t)
	f := addForm(t, s, site, "Contact", "a@example.com")
	s.ConfirmForm(site, f.Key, "a@example.com", 0)
	name, same, yes := "Kontakt", "a@example.com", true
	g, changed, err := s.UpdateForm(site, f.Key, FormPatch{Name: &name, Recipient: &same, Files: &yes})
	if err != nil || changed {
		t.Fatalf("changed=%v err=%v, want no recipient change", changed, err)
	}
	if g.Status != FormActive || g.Seq != 0 || g.Name != "Kontakt" || !g.Files {
		t.Errorf("after settings change = %+v", g)
	}
}

func TestDeleteForm(t *testing.T) {
	s, site := formSite(t)
	a := addForm(t, s, site, "A", "a@example.com")
	b := addForm(t, s, site, "B", "b@example.com")
	if err := s.DeleteForm(site, a.Key); err != nil {
		t.Fatal(err)
	}
	if len(site.Meta.Forms) != 1 || site.Meta.Forms[0].Key != b.Key {
		t.Errorf("forms after delete = %+v", site.Meta.Forms)
	}
	if err := s.DeleteForm(site, a.Key); !errors.Is(err, ErrFormNotFound) {
		t.Errorf("deleting twice: %v, want ErrFormNotFound", err)
	}
}

func TestConfirmForm(t *testing.T) {
	s, site := formSite(t)
	f := addForm(t, s, site, "Contact", "a@example.com")
	if _, err := s.ConfirmForm(site, f.Key, "a@example.com", 1); !errors.Is(err, ErrFormStale) {
		t.Errorf("wrong seq: %v, want ErrFormStale", err)
	}
	if _, err := s.ConfirmForm(site, f.Key, "x@example.com", 0); !errors.Is(err, ErrFormStale) {
		t.Errorf("wrong recipient: %v, want ErrFormStale", err)
	}
	g, err := s.ConfirmForm(site, f.Key, "a@example.com", 0)
	if err != nil || g.Status != FormActive || g.ConfirmedAt == nil {
		t.Fatalf("confirm = %+v, %v", g, err)
	}
	// A second click on the same link is a success, not an error.
	if h, err := s.ConfirmForm(site, f.Key, "a@example.com", 0); err != nil || h.Status != FormActive {
		t.Errorf("second confirm = %+v, %v", h, err)
	}
	if _, err := s.ConfirmForm(site, "nosuchkey", "a@example.com", 0); !errors.Is(err, ErrFormNotFound) {
		t.Errorf("unknown key: %v", err)
	}
}

func TestStopFormAndReconfirm(t *testing.T) {
	s, site := formSite(t)
	f := addForm(t, s, site, "Contact", "a@example.com")
	s.ConfirmForm(site, f.Key, "a@example.com", 0)

	if g, changed, err := s.StopForm(site, f.Key, "someone-else@example.com"); err != nil || changed || g.Status != FormActive {
		t.Errorf("stop by a former address = %+v changed=%v err=%v, want no change", g, changed, err)
	}
	g, changed, err := s.StopForm(site, f.Key, "a@example.com")
	if err != nil || !changed || g.Status != FormStopped || g.StoppedAt == nil || g.Seq != 1 {
		t.Fatalf("stop = %+v changed=%v err=%v", g, changed, err)
	}
	if _, changed, _ := s.StopForm(site, f.Key, "a@example.com"); changed {
		t.Error("stopping twice reported a change")
	}
	// The pre-stop confirmation link (seq 0) can never re-activate the form.
	if _, err := s.ConfirmForm(site, f.Key, "a@example.com", 0); !errors.Is(err, ErrFormStale) {
		t.Errorf("old link after stop: %v, want ErrFormStale", err)
	}
	// The form asks again, and the stop stands until the recipient confirms:
	// an owner's account must not skip a recipient who said stop
	// (2026-11-17-form-recipients-known-to-the-account-design.md).
	h, err := s.RequestConfirmation(site, f.Key)
	if err != nil || h.Status != FormPending || h.Seq != 1 || h.StoppedAt == nil {
		t.Fatalf("request after stop = %+v, %v", h, err)
	}
	if k, err := s.ConfirmForm(site, f.Key, "a@example.com", 1); err != nil || k.Status != FormActive || k.StoppedAt != nil {
		t.Errorf("confirm with the new link = %+v, %v", k, err)
	}
	if _, err := s.RequestConfirmation(site, f.Key); !errors.Is(err, ErrFormActive) {
		t.Errorf("request on an active form: %v, want ErrFormActive", err)
	}
}

// ownedFormSite is formSite owned by acct-1.
func ownedFormSite(t *testing.T) (*Store, *Site) {
	t.Helper()
	s, site := formSite(t)
	if err := s.Update(site, func(m *Meta) error { m.OwnerAccountID = "acct-1"; return nil }); err != nil {
		t.Fatal(err)
	}
	return s, site
}

func TestAddFormKnownIsActiveAtOnce(t *testing.T) {
	s, site := ownedFormSite(t)
	f, err := s.AddFormKnown(site, Form{Name: "Contact", Recipient: "me@example.com"}, 10,
		&FormShortcut{Owner: "acct-1", Via: FormViaSignIn})
	if err != nil {
		t.Fatal(err)
	}
	if f.Status != FormActive || f.ConfirmedAt == nil || f.ConfirmedVia != FormViaSignIn || f.Seq != 0 {
		t.Fatalf("known recipient = %+v, want active, confirmed now via sign_in", f)
	}
	again, _ := s.ByViewID(site.ViewID)
	if g := again.Meta.Forms[0]; g.Status != FormActive || g.ConfirmedVia != FormViaSignIn {
		t.Errorf("not persisted: %+v", g)
	}
	// Without a shortcut nothing changes.
	if g := addForm(t, s, site, "B", "me@example.com"); g.Status != FormPending || g.ConfirmedVia != "" {
		t.Errorf("plain add = %+v, want pending", g)
	}
}

// The shortcut is decided before the write, across several sites; the store
// re-checks the owner under the site lock, so a transfer in between leaves
// the form pending.
func TestFormShortcutNeedsTheOwnerInMeta(t *testing.T) {
	s, site := ownedFormSite(t)
	f, err := s.AddFormKnown(site, Form{Name: "A", Recipient: "me@example.com"}, 10,
		&FormShortcut{Owner: "acct-2", Via: FormViaAccount})
	if err != nil || f.Status != FormPending || f.ConfirmedAt != nil || f.ConfirmedVia != "" {
		t.Errorf("another owner's shortcut = %+v, %v; want pending", f, err)
	}

	s2, anon := formSite(t)
	g, err := s2.AddFormKnown(anon, Form{Name: "A", Recipient: "me@example.com"}, 10,
		&FormShortcut{Owner: "", Via: FormViaAccount})
	if err != nil || g.Status != FormPending {
		t.Errorf("an anonymous site's shortcut = %+v, %v; want pending", g, err)
	}
}

func TestUpdateFormRecipientWithAShortcut(t *testing.T) {
	s, site := ownedFormSite(t)
	f := addForm(t, s, site, "Contact", "a@example.com")
	if _, err := s.ConfirmForm(site, f.Key, "a@example.com", 0); err != nil {
		t.Fatal(err)
	}
	to := "me@example.com"
	sc := &FormShortcut{Owner: "acct-1", Via: FormViaAccount}
	g, changed, err := s.UpdateForm(site, f.Key, FormPatch{Recipient: &to, Shortcut: sc})
	if err != nil || !changed {
		t.Fatalf("UpdateForm: changed=%v err=%v", changed, err)
	}
	if g.Status != FormActive || g.Seq != 1 || g.ConfirmedAt == nil || g.ConfirmedVia != FormViaAccount {
		t.Errorf("known new recipient = %+v, want active, seq 1, via account", g)
	}

	// A change to an address nobody vouched for asks the recipient, and the
	// form no longer says it was confirmed through the account.
	other := "b@example.com"
	h, _, err := s.UpdateForm(site, f.Key, FormPatch{Recipient: &other})
	if err != nil || h.Status != FormPending || h.ConfirmedAt != nil || h.ConfirmedVia != "" || h.Seq != 2 {
		t.Errorf("unknown new recipient = %+v, %v", h, err)
	}

	// A shortcut without a recipient change changes nothing about the state.
	name := "Kontakt"
	k, changed, err := s.UpdateForm(site, f.Key, FormPatch{Name: &name, Shortcut: sc})
	if err != nil || changed || k.Status != FormPending || k.Name != "Kontakt" {
		t.Errorf("rename with a shortcut = %+v changed=%v err=%v, want still pending", k, changed, err)
	}
}

func TestRequestConfirmationKnown(t *testing.T) {
	s, site := ownedFormSite(t)
	sc := &FormShortcut{Owner: "acct-1", Via: FormViaAccount}

	f := addForm(t, s, site, "Pending", "me@example.com")
	g, err := s.RequestConfirmationKnown(site, f.Key, sc)
	if err != nil || g.Status != FormActive || g.ConfirmedVia != FormViaAccount || g.ConfirmedAt == nil {
		t.Fatalf("resend of a pending form with a shortcut = %+v, %v; want active", g, err)
	}
	if _, err := s.RequestConfirmationKnown(site, f.Key, sc); !errors.Is(err, ErrFormActive) {
		t.Errorf("resend on an active form: %v, want ErrFormActive", err)
	}

	// A stopped form is the recipient's no: it never skips them, not on the
	// first resend and not on the next, until they confirm again.
	h := addForm(t, s, site, "Stopped", "you@example.com")
	s.ConfirmForm(site, h.Key, "you@example.com", 0)
	s.StopForm(site, h.Key, "you@example.com")
	for i := 0; i < 2; i++ {
		k, err := s.RequestConfirmationKnown(site, h.Key, sc)
		if err != nil || k.Status != FormPending || k.StoppedAt == nil || k.ConfirmedVia != "" {
			t.Fatalf("resend %d of a stopped form = %+v, %v; want pending with its stop", i+1, k, err)
		}
	}
	k, err := s.ConfirmForm(site, h.Key, "you@example.com", 1)
	if err != nil || k.Status != FormActive || k.StoppedAt != nil || k.ConfirmedVia != "" {
		t.Errorf("the recipient's confirmation = %+v, %v; want active, stop cleared", k, err)
	}
}

func TestApplyQuotaStampsForms(t *testing.T) {
	s, site := formSite(t)
	three := 3
	if err := s.ApplyQuota(site, Quota{Forms: &three}, 0); err != nil {
		t.Fatal(err)
	}
	if site.Meta.QuotaForms == nil || *site.Meta.QuotaForms != 3 {
		t.Fatalf("QuotaForms = %v, want 3", site.Meta.QuotaForms)
	}
	// ApplyQuota writes every field it is given — which is exactly why every
	// caller has to pass Forms (see the cleanup and siteservice tests).
	if err := s.ApplyQuota(site, Quota{}, 0); err != nil {
		t.Fatal(err)
	}
	if site.Meta.QuotaForms != nil {
		t.Errorf("QuotaForms = %d after a grant without it, want nil", *site.Meta.QuotaForms)
	}
}

func TestFindFormAndPaused(t *testing.T) {
	s, site := formSite(t)
	a := addForm(t, s, site, "A", "a@example.com")
	b := addForm(t, s, site, "B", "b@example.com")
	if _, i, ok := FindForm(site.Meta, b.Key); !ok || i != 1 {
		t.Errorf("FindForm(b) = %d %v", i, ok)
	}
	if _, _, ok := FindForm(site.Meta, "nope"); ok {
		t.Error("found a missing key")
	}
	_, ia, _ := FindForm(site.Meta, a.Key)
	if FormPaused(ia, 1) || !FormPaused(1, 1) || !FormPaused(0, 0) {
		t.Error("FormPaused: the first N forms run, the rest pause")
	}
}

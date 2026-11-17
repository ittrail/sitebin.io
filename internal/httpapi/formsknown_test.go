package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/ittrail/sitebin.io/internal/ext"
	"github.com/ittrail/sitebin.io/internal/store"
)

// ---- recipients the owner's account already knows ----
//
// An owner skips the confirmation mail for an address their account already
// holds: its verified sign-in address, or one that confirmed another form on
// a site the account owns. Never an admin, never the edit password, never
// past a stop. See
// docs/superpowers/specs/2026-11-17-form-recipients-known-to-the-account-design.md.

// formAccountsFake is sharingFake (acct-1 owns, acct-2 is the admin) with
// ext.FormAccounts. OwnedSiteIDs reads the store's meta.json, as the
// extension's ownedSiteIDs does.
type formAccountsFake struct {
	*sharingFake
	st          *store.Store
	verified    map[string]string
	verifiedErr error
	ownedErr    error
}

var _ ext.FormAccounts = (*formAccountsFake)(nil)

func (f *formAccountsFake) VerifiedEmail(id string) (string, error) {
	if f.verifiedErr != nil {
		return "", f.verifiedErr
	}
	return f.verified[id], nil
}

func (f *formAccountsFake) OwnedSiteIDs(id string) ([]string, error) {
	if f.ownedErr != nil {
		return nil, f.ownedErr
	}
	sites, err := f.st.AllSites()
	if err != nil {
		return nil, err
	}
	var out []string
	for _, s := range sites {
		if s.Meta.OwnerAccountID == id {
			out = append(out, s.ViewID)
		}
	}
	return out, nil
}

// knownEnv is a forms instance whose sites belong to acct-1, are trusted and
// allow ten forms; acct-1 signs in as owner@example.com, verified.
func knownEnv(t *testing.T) (*env, *recSender, *formAccountsFake) {
	t.Helper()
	e, rs := formsEnv(t, nil)
	sf := registerSharing(t, ext.CreateGrant{MaxForms: intp(10), Trusted: true})
	ext.Reset()
	f := &formAccountsFake{sharingFake: sf, st: e.st, verified: map[string]string{"acct-1": "owner@example.com"}}
	registerProvider(t, f)
	return e, rs, f
}

type knownForm struct {
	Key          string `json:"key"`
	Recipient    string `json:"recipient"`
	Status       string `json:"status"`
	ConfirmedVia string `json:"confirmed_via"`
}

type knownResp struct {
	Forms    []knownForm `json:"forms"`
	Warnings []string    `json:"warnings"`
}

// form returns the listed form with key, or the newest one for "".
func (r knownResp) form(t *testing.T, key string) knownForm {
	t.Helper()
	if len(r.Forms) == 0 {
		t.Fatal("no forms in the answer")
	}
	if key == "" {
		return r.Forms[len(r.Forms)-1]
	}
	for _, f := range r.Forms {
		if f.Key == key {
			return f
		}
	}
	t.Fatalf("form %s not in the answer", key)
	return knownForm{}
}

// knownAs authenticates a request one way: an account token (sbp_…), an edit page
// session (a cookie value ending in -cookie), or the edit password.
type knownAs func(*http.Request) *http.Request

func byToken(tok string) knownAs {
	return func(r *http.Request) *http.Request { r.Header.Set("Authorization", "Bearer "+tok); return r }
}
func bySession(cookie string) knownAs {
	return func(r *http.Request) *http.Request { return editPage(r, cookie, "") }
}
func byPassword(pw string) knownAs {
	return func(r *http.Request) *http.Request { return editPage(r, "", pw) }
}

func (e *env) formsBy(t *testing.T, who knownAs, method, edit, path string, body any) (*httptest.ResponseRecorder, knownResp) {
	t.Helper()
	w := e.public(t, who(bodyReq(method, "/api/sites/"+edit+"/forms"+path, body)))
	var out knownResp
	json.Unmarshal(w.Body.Bytes(), &out)
	return w, out
}

// knownSite creates a site of acct-1 and returns its edit id, password and view id.
func knownSite(t *testing.T, e *env) (edit, pw, viewID string) {
	t.Helper()
	c := e.createSite(t, nil, map[string]string{"index.html": "<h1>hi</h1>"})
	site, err := e.st.ByViewID(c.ID)
	if err != nil || site.Meta.OwnerAccountID != "acct-1" {
		t.Fatalf("fixture: owner %q (%v), want acct-1", site.Meta.OwnerAccountID, err)
	}
	return editIDFrom(t, c.EditURL), c.EditPassword, c.ID
}

// vouchingForm adds a form to viewID through the store and confirms it as
// its recipient would.
func vouchingForm(t *testing.T, e *env, viewID, to string) store.Form {
	t.Helper()
	site, _ := e.st.ByViewID(viewID)
	f, err := e.st.AddForm(site, store.Form{Name: "Earlier", Recipient: to}, 10)
	if err != nil {
		t.Fatal(err)
	}
	if f, err = e.st.ConfirmForm(site, f.Key, to, f.Seq); err != nil {
		t.Fatal(err)
	}
	return f
}

func addBy(t *testing.T, e *env, who knownAs, edit, to string) (*httptest.ResponseRecorder, knownForm) {
	t.Helper()
	w, out := e.formsBy(t, who, "POST", edit, "", map[string]any{"name": "Contact", "recipient": to})
	if w.Code != 201 {
		t.Fatalf("add %s = %d %s", to, w.Code, w.Body)
	}
	return w, out.form(t, "")
}

func TestKnownRecipientSignInAddress(t *testing.T) {
	e, rs, _ := knownEnv(t)
	edit, _, _ := knownSite(t, e)
	for _, to := range []string{"owner@example.com", "Owner@Example.COM"} {
		if _, f := addBy(t, e, byToken("sbp_owner"), edit, to); f.Status != "active" || f.ConfirmedVia != store.FormViaSignIn {
			t.Errorf("owner adds %s = %+v, want active via sign_in", to, f)
		}
	}
	if rs.count() != 0 {
		t.Errorf("%d confirmation mails for the owner's own address", rs.count())
	}
	// A +tag is another address.
	if _, f := addBy(t, e, byToken("sbp_owner"), edit, "owner+forms@example.com"); f.Status != "pending" || rs.count() != 1 {
		t.Errorf("owner adds a +tag address = %+v, mails %d; want pending and a mail", f, rs.count())
	}
}

func TestKnownRecipientFromAnotherOwnSite(t *testing.T) {
	e, rs, _ := knownEnv(t)
	_, _, first := knownSite(t, e)
	vouchingForm(t, e, first, "office@example.com")
	edit, _, _ := knownSite(t, e)

	_, f := addBy(t, e, bySession("owner-cookie"), edit, "office@example.com")
	if f.Status != "active" || f.ConfirmedVia != store.FormViaAccount || rs.count() != 0 {
		t.Fatalf("owner adds an address confirmed on another site = %+v, mails %d", f, rs.count())
	}
	// That form vouches in turn, on the same site too.
	_, g := addBy(t, e, byToken("sbp_owner"), edit, "office@example.com")
	if g.Status != "active" || rs.count() != 0 {
		t.Errorf("a second form on the same site = %+v, mails %d", g, rs.count())
	}
	// A form that is only pending vouches for nothing.
	_, h := addBy(t, e, byToken("sbp_owner"), edit, "new@example.com")
	_, k := addBy(t, e, byToken("sbp_owner"), edit, "new@example.com")
	if h.Status != "pending" || k.Status != "pending" || rs.count() != 2 {
		t.Errorf("an unconfirmed address = %+v / %+v, mails %d; want both pending, two mails", h, k, rs.count())
	}
}

// Only the owner, through their account. An admin of a shared site and the
// holder of the edit password ask the recipient as before.
func TestKnownRecipientOnlyForTheOwnersAccount(t *testing.T) {
	e, rs, f := knownEnv(t)
	_, _, first := knownSite(t, e)
	vouchingForm(t, e, first, "office@example.com")
	edit, pw, _ := sharedSite(t, e, f.sharingFake)

	for name, who := range map[string]knownAs{
		"admin token":   byToken("sbp_admin"),
		"admin session": bySession("admin-cookie"),
		"edit password": byPassword(pw),
	} {
		for _, to := range []string{"owner@example.com", "office@example.com"} {
			before := rs.count()
			if _, got := addBy(t, e, who, edit, to); got.Status != "pending" || got.ConfirmedVia != "" || rs.count() != before+1 {
				t.Errorf("%s adds %s = %+v, mails %d; want pending and a mail", name, to, got, rs.count()-before)
			}
		}
	}
	// The admin's own sign-in address is not the owner's.
	f.verified["acct-2"] = "admin@example.com"
	if _, got := addBy(t, e, byToken("sbp_admin"), edit, "admin@example.com"); got.Status != "pending" {
		t.Errorf("the admin adds their own address on the owner's site = %+v, want pending", got)
	}
	// The owner on the same shared site skips it.
	if _, got := addBy(t, e, byToken("sbp_owner"), edit, "office@example.com"); got.Status != "active" {
		t.Errorf("the owner on their shared site = %+v, want active", got)
	}
}

// Another account's sites never vouch, whoever confirmed there.
func TestKnownRecipientNeverFromAnotherAccount(t *testing.T) {
	e, rs, _ := knownEnv(t)
	_, _, foreign := knownSite(t, e)
	vouchingForm(t, e, foreign, "office@example.com")
	site, _ := e.st.ByViewID(foreign)
	e.st.Update(site, func(m *store.Meta) error { m.OwnerAccountID = "acct-3"; return nil })

	edit, _, _ := knownSite(t, e)
	if _, f := addBy(t, e, byToken("sbp_owner"), edit, "office@example.com"); f.Status != "pending" || rs.count() != 1 {
		t.Errorf("an address confirmed on acct-3's site = %+v, mails %d; want pending", f, rs.count())
	}
}

// A stop on any of the account's forms blocks the shortcut for that address,
// the sign-in address included.
func TestKnownRecipientStopBlocks(t *testing.T) {
	e, rs, _ := knownEnv(t)
	_, _, first := knownSite(t, e)
	site, _ := e.st.ByViewID(first)
	for _, to := range []string{"office@example.com", "owner@example.com"} {
		f := vouchingForm(t, e, first, to)
		if _, _, err := e.st.StopForm(site, f.Key, to); err != nil {
			t.Fatal(err)
		}
	}
	vouchingForm(t, e, first, "office@example.com") // a second form still active: the stop wins

	edit, _, _ := knownSite(t, e)
	for _, to := range []string{"office@example.com", "owner@example.com"} {
		before := rs.count()
		if _, f := addBy(t, e, byToken("sbp_owner"), edit, to); f.Status != "pending" || rs.count() != before+1 {
			t.Errorf("owner adds %s past a stop = %+v, want pending and a mail", to, f)
		}
	}
}

// A form confirmed before the site came to its owner holds the previous
// owner's consent, not this one's.
func TestKnownRecipientTransferredSiteVouchesOnlyAfterTheMove(t *testing.T) {
	e, rs, _ := knownEnv(t)
	_, _, moved := knownSite(t, e)
	vouchingForm(t, e, moved, "office@example.com")
	site, _ := e.st.ByViewID(moved)
	since := time.Now().Add(time.Minute)
	e.st.Update(site, func(m *store.Meta) error { m.OwnerSince = &since; return nil })

	edit, _, _ := knownSite(t, e)
	if _, f := addBy(t, e, byToken("sbp_owner"), edit, "office@example.com"); f.Status != "pending" || rs.count() != 1 {
		t.Errorf("an address confirmed before the transfer = %+v, mails %d; want pending", f, rs.count())
	}

	before := time.Now().Add(-time.Hour)
	e.st.Update(site, func(m *store.Meta) error { m.OwnerSince = &before; return nil })
	if _, f := addBy(t, e, byToken("sbp_owner"), edit, "office@example.com"); f.Status != "active" {
		t.Errorf("an address confirmed after the transfer = %+v, want active", f)
	}
}

func TestKnownRecipientLockedSiteDoesNotVouch(t *testing.T) {
	e, rs, _ := knownEnv(t)
	_, _, locked := knownSite(t, e)
	vouchingForm(t, e, locked, "office@example.com")
	site, _ := e.st.ByViewID(locked)
	if _, err := e.st.SetLock(site, &store.SiteLock{At: time.Now(), By: "operator", Reason: "test"}); err != nil {
		t.Fatal(err)
	}
	edit, _, _ := knownSite(t, e)
	if _, f := addBy(t, e, byToken("sbp_owner"), edit, "office@example.com"); f.Status != "pending" || rs.count() != 1 {
		t.Errorf("an address confirmed on a locked site = %+v, mails %d; want pending", f, rs.count())
	}
}

// Unknown means mail: an error from the extension never skips anyone.
func TestKnownRecipientUnknownMeansMail(t *testing.T) {
	e, rs, f := knownEnv(t)
	edit, _, _ := knownSite(t, e)

	f.verifiedErr = errors.New("account store unreachable")
	if _, got := addBy(t, e, byToken("sbp_owner"), edit, "owner@example.com"); got.Status != "pending" {
		t.Errorf("sign-in address while the account is unknown = %+v, want pending", got)
	}
	f.verifiedErr = nil
	f.ownedErr = errors.New("a site's owner could not be read")
	if _, got := addBy(t, e, byToken("sbp_owner"), edit, "owner@example.com"); got.Status != "pending" {
		t.Errorf("sign-in address while the site list is incomplete = %+v, want pending", got)
	}
	f.ownedErr = nil
	delete(f.verified, "acct-1") // an unverified sign-in address
	if _, got := addBy(t, e, byToken("sbp_owner"), edit, "owner@example.com"); got.Status != "pending" {
		t.Errorf("an unverified sign-in address = %+v, want pending", got)
	}
	if rs.count() != 3 {
		t.Errorf("mails = %d, want 3", rs.count())
	}
}

// A provider without ext.FormAccounts changes nothing.
func TestKnownRecipientNeedsTheExtension(t *testing.T) {
	e, rs := formsEnv(t, nil)
	registerSharing(t, ext.CreateGrant{MaxForms: intp(10), Trusted: true})
	_, _, first := knownSite(t, e)
	vouchingForm(t, e, first, "office@example.com")
	edit, _, _ := knownSite(t, e)
	if _, f := addBy(t, e, byToken("sbp_owner"), edit, "office@example.com"); f.Status != "pending" || rs.count() != 1 {
		t.Errorf("without FormAccounts = %+v, mails %d; want pending", f, rs.count())
	}
}

// No mail goes out, so no confirmation throttle is spent or consulted.
func TestKnownRecipientSpendsNoThrottle(t *testing.T) {
	e, rs, _ := knownEnv(t)
	edit, _, viewID := knownSite(t, e)
	for e.api.forms.confirmAddr.Allow(confirmAddrKey("owner@example.com")) {
	}
	for e.api.forms.confirmSite.Allow(viewID) {
	}
	if _, f := addBy(t, e, byToken("sbp_owner"), edit, "owner@example.com"); f.Status != "active" || rs.count() != 0 {
		t.Errorf("a known recipient with the throttles spent = %+v, mails %d; want active", f, rs.count())
	}
}

func TestKnownRecipientOnARecipientChange(t *testing.T) {
	e, rs, _ := knownEnv(t)
	edit, _, _ := knownSite(t, e)
	_, f := addBy(t, e, byToken("sbp_owner"), edit, "someone@example.com")
	w, out := e.formsBy(t, byToken("sbp_owner"), "PUT", edit, "/"+f.Key, map[string]any{"recipient": "owner@example.com"})
	if w.Code != 200 {
		t.Fatalf("PUT = %d %s", w.Code, w.Body)
	}
	if g := out.form(t, f.Key); g.Status != "active" || g.ConfirmedVia != store.FormViaSignIn || rs.count() != 1 {
		t.Errorf("recipient changed to the sign-in address = %+v, mails %d; want active, only the first mail", g, rs.count())
	}
	// And back to an unknown address: pending, mailed, no longer "via".
	_, out = e.formsBy(t, byToken("sbp_owner"), "PUT", edit, "/"+f.Key, map[string]any{"recipient": "someone@example.com"})
	if g := out.form(t, f.Key); g.Status != "pending" || g.ConfirmedVia != "" || rs.count() != 2 {
		t.Errorf("recipient changed back = %+v, mails %d", g, rs.count())
	}
}

func TestKnownRecipientOnAResend(t *testing.T) {
	e, rs, _ := knownEnv(t)
	edit, _, viewID := knownSite(t, e)
	site, _ := e.st.ByViewID(viewID)
	// Pending from before: the owner's next resend activates it.
	f, err := e.st.AddForm(site, store.Form{Name: "Old", Recipient: "owner@example.com"}, 10)
	if err != nil {
		t.Fatal(err)
	}
	w, out := e.formsBy(t, byToken("sbp_owner"), "POST", edit, "/"+f.Key+"/confirmation", nil)
	if w.Code != 202 {
		t.Fatalf("resend = %d %s", w.Code, w.Body)
	}
	if g := out.form(t, f.Key); g.Status != "active" || g.ConfirmedVia != store.FormViaSignIn || rs.count() != 0 {
		t.Errorf("resend of a pending known form = %+v, mails %d; want active, no mail", g, rs.count())
	}

	// A stopped one is mailed, now and on the next resend.
	s := vouchingForm(t, e, viewID, "office@example.com")
	site, _ = e.st.ByViewID(viewID)
	e.st.StopForm(site, s.Key, "office@example.com")
	vouchingForm(t, e, viewID, "office@example.com")
	for i := 1; i <= 2; i++ {
		_, out = e.formsBy(t, byToken("sbp_owner"), "POST", edit, "/"+s.Key+"/confirmation", nil)
		if g := out.form(t, s.Key); g.Status != "pending" || rs.count() != i {
			t.Errorf("resend %d of a stopped form = %+v, mails %d; want pending and a mail", i, g, rs.count())
		}
	}
}

// MCP: the owner's token skips the mail too, and the tool's output gains no
// field (clients that cached a closed output schema would reject one).
func TestKnownRecipientOverMCP(t *testing.T) {
	e, rs, _ := knownEnv(t)
	edit, _, _ := knownSite(t, e)
	cs := mcpClient(t, e, http.Header{"Authorization": {"Bearer sbp_owner"}})
	res := mcpCall(t, cs, "add_form", map[string]any{"edit_id": edit, "form": map[string]any{"name": "Contact", "recipient": "owner@example.com"}})
	if res.IsError {
		t.Fatalf("add_form = %s", mcpText(res))
	}
	list := res.StructuredContent.(map[string]any)["forms"].([]any)
	f := list[0].(map[string]any)
	if f["status"] != "active" || rs.count() != 0 {
		t.Errorf("add_form for the owner's address = %v, mails %d; want active, no mail", f, rs.count())
	}
	if _, ok := f["confirmed_via"]; ok {
		t.Error("the MCP output gained confirmed_via")
	}
}

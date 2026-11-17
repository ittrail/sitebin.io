package store

import (
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/ittrail/sitebin.io/internal/ids"
)

// Form states. The state a person also sees, "paused", is computed from the
// site's cap and never stored (FormPaused).
// Design: docs/superpowers/specs/2026-09-24-site-forms-design.md.
const (
	FormPending = "pending" // waiting for the recipient to confirm
	FormActive  = "active"  // the recipient confirmed; submissions are mailed
	FormStopped = "stopped" // the recipient used the stop link
)

// How an active form was confirmed without a mail (Form.ConfirmedVia); "" is
// the recipient's own click. Design:
// docs/superpowers/specs/2026-11-17-form-recipients-known-to-the-account-design.md.
const (
	FormViaSignIn  = "sign_in" // the recipient is the owner account's verified sign-in address
	FormViaAccount = "account" // the recipient confirmed another form of the same owner
)

// FormShortcut activates a form without a confirmation mail, because the
// owner's account already holds its recipient's consent (decided by the
// caller, httpapi.knownRecipient). It applies only while meta.json still
// names Owner: the decision is made before the write, across several sites,
// and a transfer may commit in between.
type FormShortcut struct {
	Owner string
	Via   string // FormViaSignIn or FormViaAccount
}

// applies reports whether sc may activate a form on the site m describes.
func (sc *FormShortcut) applies(m *Meta) bool {
	return sc != nil && sc.Owner != "" && sc.Via != "" && m.OwnerAccountID == sc.Owner
}

// activate makes g active through sc, confirmed now.
func (sc *FormShortcut) activate(g *Form) {
	now := time.Now().UTC()
	g.Status, g.ConfirmedAt, g.ConfirmedVia = FormActive, &now, sc.Via
}

// FormKeyLen is the length of a form key, drawn from the id alphabet.
const FormKeyLen = 16

// Form is one form a site's pages post to. Its key is public (it sits in the
// page's HTML), so nothing here is a secret.
type Form struct {
	Key       string `json:"key"`
	Name      string `json:"name"`
	Recipient string `json:"recipient"`
	Captcha   bool   `json:"captcha,omitempty"`
	Files     bool   `json:"files,omitempty"`
	Redirect  string `json:"redirect,omitempty"`
	Status    string `json:"status"`
	// Seq moves when the recipient changes and when the form is stopped. A
	// confirmation link carries the seq it was minted for, so both kill every
	// earlier link. Confirming does not move it: a second click on the same
	// link is still a success.
	Seq         int        `json:"seq,omitempty"`
	CreatedAt   time.Time  `json:"created_at"`
	ConfirmedAt *time.Time `json:"confirmed_at,omitempty"`
	// ConfirmedVia says how an active form was confirmed without a mail
	// (FormViaSignIn, FormViaAccount); "" is the recipient's own click.
	ConfirmedVia string `json:"confirmed_via,omitempty"`
	// StoppedAt is the recipient's stop. It stands until they confirm the
	// form again: a resend moves the form back to pending and keeps it, so an
	// owner's account never skips a recipient who said stop.
	StoppedAt *time.Time `json:"stopped_at,omitempty"`
}

// FormPatch changes a form's settings. A nil field is left alone.
type FormPatch struct {
	Name      *string
	Recipient *string
	Captcha   *bool
	Files     *bool
	Redirect  *string
	// Shortcut activates the form at once when its recipient changes to an
	// address the owner's account already holds. Nothing else uses it.
	Shortcut *FormShortcut
}

var (
	ErrFormNotFound = errors.New("no form with that key on this site")
	ErrTooManyForms = errors.New("form limit reached")
	// ErrFormStale refuses a confirmation link minted before the form's
	// recipient changed or the form was stopped.
	ErrFormStale = errors.New("this link is no longer valid for this form")
	// ErrFormActive refuses a new confirmation for a form whose recipient
	// already confirmed.
	ErrFormActive = errors.New("this form's recipient has already confirmed")

	errFormUnchanged = errors.New("unchanged") // internal: skip the write
)

// FindForm returns the form with key and its position in the site's list.
func FindForm(m Meta, key string) (Form, int, bool) {
	for i, f := range m.Forms {
		if f.Key == key {
			return f, i, true
		}
	}
	return Form{}, -1, false
}

// FormPaused reports whether the form at index is beyond the site's cap. A
// smaller plan pauses the newest forms; nothing is ever deleted over a cap.
func FormPaused(index, limit int) bool { return index >= limit }

// AddForm appends a new pending form. Only the settings are taken from spec:
// the key, state and timestamps are the store's. limit is the site's forms
// cap, resolved by the caller, and it is compared under the site lock, so
// two concurrent adds cannot both take the last slot.
func (s *Store) AddForm(site *Site, spec Form, limit int) (Form, error) {
	return s.AddFormKnown(site, spec, limit, nil)
}

// AddFormKnown is AddForm for a recipient the owner's account already holds:
// with a shortcut that applies, the form is active at once.
func (s *Store) AddFormKnown(site *Site, spec Form, limit int, sc *FormShortcut) (Form, error) {
	var out Form
	err := s.Update(site, func(m *Meta) error {
		if len(m.Forms) >= limit {
			return fmt.Errorf("%w: this site's plan allows %d form(s)", ErrTooManyForms, limit)
		}
		out = Form{
			Key:       newFormKey(m.Forms),
			Name:      spec.Name,
			Recipient: spec.Recipient,
			Captcha:   spec.Captcha,
			Files:     spec.Files,
			Redirect:  spec.Redirect,
			Status:    FormPending,
			CreatedAt: time.Now().UTC(),
		}
		if sc.applies(m) {
			sc.activate(&out)
		}
		m.Forms = append(m.Forms, out)
		return nil
	})
	return out, err
}

func newFormKey(existing []Form) string {
	for {
		k := ids.New()[:FormKeyLen]
		if !slices.ContainsFunc(existing, func(f Form) bool { return f.Key == k }) {
			return k
		}
	}
}

// UpdateForm applies p. A new recipient puts the form back to pending and
// moves its seq, so every earlier confirmation link dies; recipientChanged
// tells the caller to send a fresh one — unless p.Shortcut applies, which
// makes the form active at once (the seq moves all the same).
func (s *Store) UpdateForm(site *Site, key string, p FormPatch) (f Form, recipientChanged bool, err error) {
	err = s.Update(site, func(m *Meta) error {
		_, i, ok := FindForm(*m, key)
		if !ok {
			return ErrFormNotFound
		}
		g := &m.Forms[i]
		if p.Name != nil {
			g.Name = *p.Name
		}
		if p.Captcha != nil {
			g.Captcha = *p.Captcha
		}
		if p.Files != nil {
			g.Files = *p.Files
		}
		if p.Redirect != nil {
			g.Redirect = *p.Redirect
		}
		if p.Recipient != nil && *p.Recipient != g.Recipient {
			g.Recipient = *p.Recipient
			g.Status = FormPending
			g.Seq++
			g.ConfirmedAt, g.StoppedAt, g.ConfirmedVia = nil, nil, ""
			if p.Shortcut.applies(m) {
				p.Shortcut.activate(g)
			}
			recipientChanged = true
		}
		f = *g
		return nil
	})
	return f, recipientChanged, err
}

// DeleteForm removes a form. Its outstanding links find nothing afterwards.
func (s *Store) DeleteForm(site *Site, key string) error {
	return s.Update(site, func(m *Meta) error {
		_, i, ok := FindForm(*m, key)
		if !ok {
			return ErrFormNotFound
		}
		m.Forms = slices.Delete(m.Forms, i, i+1)
		return nil
	})
}

// ConfirmForm records the recipient's consent. The link carries the recipient
// and seq it was minted for; if either differs, the link predates a recipient
// change or a stop and is refused. Confirming an active form again is not an
// error. A confirmation ends a stop that stood on the form.
func (s *Store) ConfirmForm(site *Site, key, recipient string, seq int) (Form, error) {
	var out Form
	err := s.Update(site, func(m *Meta) error {
		_, i, ok := FindForm(*m, key)
		if !ok {
			return ErrFormNotFound
		}
		g := &m.Forms[i]
		if g.Recipient != recipient || g.Seq != seq || g.Status == FormStopped {
			return ErrFormStale
		}
		if g.Status == FormActive {
			out = *g
			return errFormUnchanged
		}
		now := time.Now().UTC()
		g.Status, g.ConfirmedAt, g.ConfirmedVia, g.StoppedAt = FormActive, &now, "", nil
		out = *g
		return nil
	})
	if errors.Is(err, errFormUnchanged) {
		err = nil
	}
	return out, err
}

// StopForm is the recipient withdrawing consent. A stop link carries no seq,
// because it must keep working from an old mail, so it is checked against the
// address alone. A link for an address that is no longer the recipient
// changes nothing. changed is false then, and when the form was already
// stopped.
func (s *Store) StopForm(site *Site, key, recipient string) (f Form, changed bool, err error) {
	err = s.Update(site, func(m *Meta) error {
		_, i, ok := FindForm(*m, key)
		if !ok {
			return ErrFormNotFound
		}
		g := &m.Forms[i]
		if g.Recipient != recipient || g.Status == FormStopped {
			f = *g
			return errFormUnchanged
		}
		now := time.Now().UTC()
		g.Status, g.StoppedAt = FormStopped, &now
		g.Seq++
		f, changed = *g, true
		return nil
	})
	if errors.Is(err, errFormUnchanged) {
		err = nil
	}
	return f, changed, err
}

// RequestConfirmation readies a form for a new confirmation mail. A stopped
// form goes back to pending (its seq already moved at the stop) and keeps its
// StoppedAt, a pending one stays as it is, and an active one is refused.
func (s *Store) RequestConfirmation(site *Site, key string) (Form, error) {
	return s.RequestConfirmationKnown(site, key, nil)
}

// RequestConfirmationKnown is RequestConfirmation for a recipient the owner's
// account already holds: a pending form with no stop standing is active at
// once when sc applies. A stopped form never is, on this resend or a later
// one, until its recipient confirms.
func (s *Store) RequestConfirmationKnown(site *Site, key string, sc *FormShortcut) (Form, error) {
	var out Form
	err := s.Update(site, func(m *Meta) error {
		_, i, ok := FindForm(*m, key)
		if !ok {
			return ErrFormNotFound
		}
		g := &m.Forms[i]
		switch g.Status {
		case FormActive:
			return ErrFormActive
		case FormStopped:
			g.Status = FormPending
		case FormPending:
			if g.StoppedAt == nil && sc.applies(m) {
				sc.activate(g)
			}
		}
		out = *g
		return nil
	})
	return out, err
}

//go:build ee

package ee

import (
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/ittrail/sitebin.io/ee/ownership"
)

// Mail for transfers and shares. Plain text (no HTML part, no tracking), sent
// best effort in the background, and only ever to an account whose address is
// VERIFIED: an address nobody proved is a stranger's, and the instance must
// not become a way to send mail to strangers.

// mailVerified mails the account that owns this address, when there is one,
// its address is verified and a mailer is configured. The lookup is a file
// read, so a caller that must not wait (a request that answers the same for a
// known and an unknown address) calls it in a goroutine of its own, counted in
// mailWG; the send itself is a goroutine too. A failure is logged without the
// address.
func (p *provider) mailVerified(email, subject, body string) {
	if p.mailer == nil || p.accounts == nil {
		return
	}
	acc, err := p.accounts.ByEmail(email)
	if err != nil || acc == nil || !acc.EmailVerified {
		return
	}
	to := acc.Email
	mailer := p.mailer
	p.mailWG.Add(1)
	go func() {
		defer p.mailWG.Done()
		if err := mailer.Send(to, subject, body); err != nil {
			slog.Warn("could not send an ownership mail", "account", acc.ID, "err", err)
		}
	}()
}

// mailInBackground runs mailVerified off the request, counted in mailWG.
func (p *provider) mailInBackground(email, subject, body string) {
	p.mailWG.Add(1)
	go func() {
		defer p.mailWG.Done()
		p.mailVerified(email, subject, body)
	}()
}

var errNothingToMail = errors.New("nothing to mail")

// notifyOffer tells the recipient of an open offer that it exists, once: it
// marks NotifiedAt first (so a second caller, the background loop, finds it
// done) and then mails the recipient if they are a verified account. The
// sender is never told whether anyone was mailed. An offer that is not open,
// or already notified, is left alone.
func (p *provider) notifyOffer(o ownership.Offer) {
	if p.owner == nil {
		return
	}
	cur, err := p.owner.UpdateOffer(o.ID, func(x *ownership.Offer) error {
		if x.NotifiedAt != nil || x.State != ownership.StateOpen {
			return errNothingToMail
		}
		now := time.Now().UTC()
		x.NotifiedAt = &now
		return nil
	})
	if err != nil {
		if !errors.Is(err, errNothingToMail) {
			slog.Error("could not mark an offer as notified", "offer", o.ID, "err", err)
		}
		return
	}
	senderEmail := "an account on this instance"
	if p.accounts != nil {
		if s, err := p.accounts.ByID(cur.From); err == nil && s.Email != "" {
			senderEmail = s.Email
		}
	}
	subject, body := p.offerMail(cur, senderEmail)
	p.mailInBackground(cur.ToEmail, subject, body)
}

// offerMail is "a transfer is offered to you". It names the sender's email,
// the sites (name and address), the accept-by date and the account link, and
// never a password: a transferred site gets a fresh one on acceptance.
func (p *provider) offerMail(o ownership.Offer, senderEmail string) (subject, body string) {
	var b strings.Builder
	fmt.Fprintf(&b, "%s wants to hand over %s on Sitebin to your account:\n\n", senderEmail, sitesWord(len(o.Sites)))
	for _, id := range o.Sites {
		label := id
		if info, ok := p.host.Sites().Info(id); ok {
			label = siteLabel(info)
		}
		fmt.Fprintf(&b, "  - %s\n", label)
	}
	fmt.Fprintf(&b, "\nYou can accept until %s (UTC). After that the offer lapses on its own.\n\n", dateText(o.AcceptBy))
	fmt.Fprintf(&b, "To review it, sign in to your Sitebin account:\n\n%s/account\n\n", p.baseURL())
	b.WriteString("If you did not expect this, you can ignore this email: nothing changes unless you accept.\n")
	return "A Sitebin transfer is offered to you", b.String()
}

func sitesWord(n int) string {
	if n == 1 {
		return "1 site"
	}
	return fmt.Sprintf("%d sites", n)
}

// dateText is a date as the pages and mails write it.
func dateText(t time.Time) string { return t.UTC().Format("2006-01-02") }

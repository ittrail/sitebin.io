//go:build ee

package ee

import (
	"errors"
	"fmt"
	"log/slog"
	"net"
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
			// err's text can carry the recipient (an SMTP reply often
			// quotes it), so only a coarse class is logged.
			slog.Warn("could not send an ownership mail", "account", acc.ID, "class", mailErrClass(err))
		}
	}()
}

// mailErrClass is the kind of a send failure without its text.
func mailErrClass(err error) string {
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		return "timeout"
	}
	return fmt.Sprintf("%T", err)
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

// writeSites lists sites in a mail, one per line, by their labels.
func (p *provider) writeSites(b *strings.Builder, ids []string) {
	for _, id := range ids {
		fmt.Fprintf(b, "  - %s\n", p.labelOf(id))
	}
}

// mailAccepted tells both sides how an accepted offer ended: the sites that
// moved (o.Done) and, when the rest could not move, the ones that stay with
// the sender and why. The sender is named by their account's address, the
// recipient by the address the offer was made to.
func (p *provider) mailAccepted(o ownership.Offer, stayed []string, reason string) {
	senderEmail := p.emailOf(o.From)
	recipientEmail := p.emailOf(o.AcceptedBy)
	if recipientEmail == "" {
		recipientEmail = o.ToEmail
	}
	other := senderEmail
	if other == "" {
		other = "the sender"
	}
	subject, body := p.acceptedMail(o, stayed, reason, o.ToEmail, true)
	p.mailVerified(senderEmail, subject, body)
	subject, body = p.acceptedMail(o, stayed, reason, other, false)
	p.mailVerified(recipientEmail, subject, body)
}

// endedForRecipient is all the recipient is told about why sites could not
// move: the reasons are the sender's business (their plan, their account,
// their other sites).
const endedForRecipient = "the offer could not be completed and has ended"

// acceptedMail is "a transfer was accepted", for one side (toSender), naming
// the other party's address (other), the sites that moved and, when some
// could not, the ones that stay with the sender: why for the sender, a
// generic sentence for the recipient. Never a password: the recipient saw
// the new ones once, on the result page.
func (p *provider) acceptedMail(o ownership.Offer, stayed []string, reason, other string, toSender bool) (subject, body string) {
	var b strings.Builder
	holder := "you"
	if !toSender {
		holder = other
		reason = endedForRecipient
	}
	switch {
	case len(o.Done) == 0:
		subject = "A Sitebin transfer could not be completed"
		if toSender {
			fmt.Fprintf(&b, "%s accepted your transfer, but none of its sites could be moved (%s). They stay with you:\n\n", other, reason)
		} else {
			fmt.Fprintf(&b, "You accepted the transfer from %s, but none of its sites could be moved (%s). They stay with %s:\n\n", other, reason, other)
		}
		p.writeSites(&b, stayed)
		stayed = nil
	case toSender:
		subject = "Your Sitebin transfer was accepted"
		fmt.Fprintf(&b, "%s accepted your transfer. These sites belong to their account now:\n\n", other)
		p.writeSites(&b, o.Done)
		b.WriteString("\nYou no longer manage them: each has a new edit password, and your tags, admins and upload links on them have ended.\n")
	default:
		subject = "You took over Sitebin sites"
		fmt.Fprintf(&b, "You accepted the transfer from %s. These sites belong to your account now:\n\n", other)
		p.writeSites(&b, o.Done)
		b.WriteString("\nEach has a new edit password. Those moved while you accepted were shown to you once, then; for any other, rotate the password on your dashboard to get a new one.\n")
	}
	if len(stayed) > 0 {
		fmt.Fprintf(&b, "\nThese sites could not be moved (%s) and stay with %s:\n\n", reason, holder)
		p.writeSites(&b, stayed)
	}
	fmt.Fprintf(&b, "\nYour account: %s/account\n", p.baseURL())
	return subject, b.String()
}

// declinedMail is "your transfer was declined", to the sender, naming the
// address the offer was made to and its sites.
func (p *provider) declinedMail(o ownership.Offer) (subject, body string) {
	var b strings.Builder
	fmt.Fprintf(&b, "%s declined your transfer of %s:\n\n", o.ToEmail, sitesWord(len(o.Sites)))
	p.writeSites(&b, o.Sites)
	fmt.Fprintf(&b, "\nNothing has moved: the sites stay with you.\n\nYour transfers: %s%s\n", p.baseURL(), transferPath)
	return "Your Sitebin transfer was declined", b.String()
}

// pausedMail is "your transfer is paused", to the sender: why (reason, a
// sentence offerPause or acceptCheck gave, safe for the sender), what to do
// about it (advice, may be empty), and that the accept-by date still runs.
func (p *provider) pausedMail(o ownership.Offer, reason, advice string) (subject, body string) {
	var b strings.Builder
	fmt.Fprintf(&b, "Your transfer to %s is paused, so it cannot be accepted right now: %s.\n\n", o.ToEmail, reason)
	p.writeSites(&b, o.Sites)
	b.WriteString("\nNothing has moved.")
	if advice != "" {
		b.WriteString(" " + advice)
	}
	fmt.Fprintf(&b, " The accept-by date (%s, UTC) still runs while the offer is paused.\n\nYour transfers: %s%s\n", dateText(o.AcceptBy), p.baseURL(), transferPath)
	return "Your Sitebin transfer is paused", b.String()
}

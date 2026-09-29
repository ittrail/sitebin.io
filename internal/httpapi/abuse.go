package httpapi

import (
	"context"
	"fmt"
	"log/slog"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/ittrail/sitebin.io/internal/config"
	"github.com/ittrail/sitebin.io/internal/ext"
	"github.com/ittrail/sitebin.io/internal/forms"
	"github.com/ittrail/sitebin.io/internal/store"
)

// Abuse detection, the HTTP half: the guard's decisions become lock side
// effects and operator mail, CSP reports become the tripwire, abuse reports
// become mail. The rules and the policy are internal/abuse and
// internal/store/guard.go. See docs/superpowers/specs/2026-09-29-abuse-detection.md.

// msgHeldCreate answers a creation the abuse guard held. The site exists,
// locked, as evidence; its caller gets neither its address nor its password.
const msgHeldCreate = "this upload matched the instance's abuse rules: the site was created locked and is held for review by the operator, and it is not served"

// onScan is the store's scan hook: every guard decision, with the site lock
// held. A hold ends what the site's lock ends — cached passwords, upload
// tokens, a running container — and every decision but a reviewed one is
// mailed to the operator (aggregated, asynchronously).
func (a *API) onScan(ev store.ScanEvent) {
	if ev.Held() {
		if site, err := a.st.ByViewID(ev.ViewID); err == nil {
			a.lockChanged(site)
		}
	}
	if ev.Decision == store.DecisionReviewed {
		return
	}
	al := a.scanAlert(ev)
	switch {
	case ev.Held():
		// A hold is rare (a site is held once) and is the alert the rest
		// exist for: no window or cap may delay it.
		al.priority = true
	case ev.Decision == store.DecisionUnverified:
		// Anyone can send a report naming any site; one that the site's
		// files do not bear out goes to the digest and spends no one's
		// budget, so forged reports cannot push real alerts back an hour.
		al.digestOnly = true
	}
	a.alerts.notify(al)
}

// scanAlert turns a guard decision into an alert.
func (a *API) scanAlert(ev store.ScanEvent) alertEvent {
	var rules []string
	for _, f := range ev.Findings {
		if !containsStr(rules, f.Rule) {
			rules = append(rules, f.Rule)
		}
	}
	what := strings.Join(rules, ", ")
	var subject, lede string
	switch {
	case ev.Source == store.FindingCSP && ev.Decision == store.DecisionUnverified:
		subject = "CSP report, not verified: " + ev.ViewID
		lede = "A CSP report says this site tried to reach a known exfiltration destination, but none of its files reference it. Nothing was recorded on the site; a forged report looks like this."
	case ev.Source == store.FindingCSP && ev.Held():
		subject = "Site held by the CSP tripwire: " + ev.ViewID + " (" + what + ")"
		lede = "A CSP report named a known exfiltration destination, the site's own files reference it, and the site is now locked and held for review."
	case ev.Source == store.FindingCSP:
		subject = "CSP tripwire: " + ev.ViewID + " (" + what + ")"
		lede = "A CSP report named a known exfiltration destination and the site's own files reference it. The site was not locked (" + ev.Decision + ")."
	case ev.Held():
		subject = "Site held: " + ev.ViewID + " (" + what + ")"
		lede = "An upload matched a blocking abuse rule. The site was locked before the file became visible and is held for review."
	default:
		subject = "Flagged: " + ev.ViewID + " (" + what + ")"
		lede = "Content matched an abuse rule. The site was not locked (" + ev.Decision + ")."
	}
	var b strings.Builder
	b.WriteString(lede + "\n\n")
	a.siteLines(&b, ev.ViewID)
	fmt.Fprintf(&b, "Decision:  %s\n", ev.Decision)
	fmt.Fprintf(&b, "Source:    %s\n", map[string]string{store.FindingUpload: "an upload", store.FindingCSP: "a CSP report", store.FindingScan: "sitebin scan"}[ev.Source])
	if ev.Blocked != "" {
		fmt.Fprintf(&b, "Blocked:   %s\n", defang(ev.Blocked))
	}
	if len(ev.Findings) > 0 {
		b.WriteString("\nFindings:\n")
		for _, f := range ev.Findings {
			fmt.Fprintf(&b, "  - %s (%s) in %s\n", f.Rule, f.Severity, defang(f.Path))
			if f.Excerpt != "" {
				fmt.Fprintf(&b, "      %s\n", defang(f.Excerpt))
			}
		}
	} else if len(ev.Paths) > 0 {
		fmt.Fprintf(&b, "Files:     %s\n", defang(strings.Join(ev.Paths, ", ")))
	}
	a.registerLines(&b, ev.ViewID)
	if ev.Held() {
		b.WriteString("\nA lock keeps the site as evidence. Unlocking it (register or `sitebin unlock`) marks this content as reviewed: the same bytes are not held again.\n")
	}
	line := fmt.Sprintf("%s  %s  %s  %s", ev.ViewID, ev.Decision, what, defang(strings.Join(ev.Paths, ",")))
	if ev.Blocked != "" {
		line += "  blocked " + defang(ev.Blocked)
	}
	return alertEvent{key: ev.ViewID, subject: subject, body: b.String(), line: line}
}

// reportAlert turns a stored abuse report into an alert.
func (a *API) reportAlert(rep store.Report) alertEvent {
	var b strings.Builder
	via := "the API"
	if rep.Via == store.ReportViaPage {
		via = "the report page"
	}
	fmt.Fprintf(&b, "An abuse report was filed through %s.\n\n", via)
	// Everything the reporter typed is defanged: the report may name a
	// phishing URL, and an API reason is free text.
	reason := defang(rep.Reason)
	fmt.Fprintf(&b, "Target:    %s\n", defang(rep.Target))
	fmt.Fprintf(&b, "Reason:    %s\n", reason)
	contact := rep.Contact
	if contact == "" {
		contact = "none given"
	}
	fmt.Fprintf(&b, "Contact:   %s\n", contact) // validated as an address: the operator's way back
	fmt.Fprintf(&b, "From:      %s\n", rep.Source)
	if strings.TrimSpace(rep.Details) != "" {
		b.WriteString("Details:\n")
		for _, l := range strings.Split(strings.ReplaceAll(rep.Details, "\r\n", "\n"), "\n") {
			fmt.Fprintf(&b, "    %s\n", defang(l))
		}
	}
	b.WriteString("\n")
	key := "report:" + strings.ToLower(rep.Target)
	subject := "Report: " + reason
	if rep.ViewID != "" {
		a.siteLines(&b, rep.ViewID)
		key = rep.ViewID
		subject += " - " + rep.ViewID
	} else {
		b.WriteString("Site:      not resolved to a site on this instance\n")
	}
	a.registerLines(&b, rep.ViewID)
	if a.registerURL() != "" {
		fmt.Fprintf(&b, "Reports:   %s/account/admin/reports\n", a.baseURL())
	}
	line := fmt.Sprintf("report  %s  %s  %s", reason, defang(rep.Target), rep.ViewID)
	return alertEvent{key: key, subject: subject, body: b.String(), line: line}
}

// siteLines writes what an operator needs to know about a site to act on
// an alert: its address, owner and lock.
func (a *API) siteLines(b *strings.Builder, viewID string) {
	site, err := a.st.ByViewID(viewID)
	if err != nil {
		fmt.Fprintf(b, "Site:      %s (no longer exists)\n", viewID)
		return
	}
	fmt.Fprintf(b, "Site:      %s\n", viewID)
	fmt.Fprintf(b, "Address:   %s\n", defang(a.cfg.ViewURL(viewID)))
	if len(site.Meta.CustomDomains) > 0 {
		fmt.Fprintf(b, "Domains:   %s\n", defang(strings.Join(site.Meta.CustomDomains, ", ")))
	}
	fmt.Fprintf(b, "Owner:     %s\n", a.ownerLabel(site.Meta.OwnerAccountID))
	trust := "untrusted tier"
	if a.st.Trusted(site) {
		trust = "trusted tier"
	}
	fmt.Fprintf(b, "Tier:      %s, created %s\n", trust, site.Meta.CreatedAt.UTC().Format("2006-01-02 15:04 UTC"))
	if l := site.Meta.Locked; l != nil {
		fmt.Fprintf(b, "State:     LOCKED by %s since %s", l.By, l.At.UTC().Format("2006-01-02 15:04 UTC"))
		if l.Reason != "" {
			fmt.Fprintf(b, " — %s", defang(l.Reason))
		}
		b.WriteString("\n")
	} else {
		b.WriteString("State:     served (not locked)\n")
	}
}

// ownerLabel names an account for the operator: its email where the
// extension can say, its id always.
func (a *API) ownerLabel(id string) string {
	if id == "" {
		return "anonymous"
	}
	if p, ok := ext.Get(); ok {
		if dir, ok := p.(ext.AccountDirectory); ok {
			if email, ok := dir.AccountEmail(id); ok && email != "" {
				return email + " (" + id + ")"
			}
		}
	}
	return id
}

// registerURL is the admin console's address, where there is one: an
// extension with accounts.
func (a *API) registerURL() string {
	if p, ok := ext.Get(); ok && p.AccountsEnabled() {
		return a.baseURL() + "/account/admin"
	}
	return ""
}

func (a *API) registerLines(b *strings.Builder, viewID string) {
	reg := a.registerURL()
	if reg == "" {
		if viewID != "" {
			fmt.Fprintf(b, "\nLock it:   sitebin lock %s <reason>\n", viewID)
		}
		return
	}
	if viewID != "" {
		fmt.Fprintf(b, "\nRegister:  %s?q=%s\n", reg, url.QueryEscape(viewID))
	}
}

// defangRe finds the dots of anything shaped like a host name.
var defangRe = regexp.MustCompile(`([a-zA-Z0-9-])\.([a-zA-Z0-9-])`)

// defang makes URLs and host names in untrusted text unclickable
// (hxxps://api[.]telegram[.]org/…), so an alert that quotes a phishing kit
// does not itself read as phishing to a mail filter.
func defang(s string) string {
	s = strings.NewReplacer("https://", "hxxps://", "http://", "hxxp://", "HTTPS://", "hxxps://", "HTTP://", "hxxp://").Replace(s)
	// twice: "a.b.c" overlaps, and one pass leaves every other dot
	s = defangRe.ReplaceAllString(s, "$1[.]$2")
	return defangRe.ReplaceAllString(s, "$1[.]$2")
}

func containsStr(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// ---- the alerter: aggregation and delivery ----

const (
	// alertPerKey is how long a site (or a report target) waits after one
	// immediate mail before the next one; everything in between goes into
	// the digest.
	alertPerKey = time.Hour
	// alertBurst is how many immediate mails go out per hour in all. A flood
	// across many sites becomes one digest, not a mail bomb.
	alertBurst = 10
	// digestDelay is how long after its first event a digest is sent.
	digestDelay    = time.Hour
	digestMaxLines = 200
	maxQueued      = 1000
	alertTimeout   = 30 * time.Second
)

// alertEvent is one thing the operator is told.
type alertEvent struct {
	// key aggregates: a site's view id, or "report:<target>".
	key     string
	subject string // without the [sitebin abuse] prefix
	body    string
	// line is the event in one line, for the digest.
	line string
	// priority is mailed at once, outside the per-key window and the hourly
	// cap, and spends neither: a hold. digestOnly never is: an event anyone
	// can cause for free.
	priority, digestOnly bool
}

// alerter mails the operator: at most one mail per key per hour and
// alertBurst in all, the rest in an hourly digest. Nothing runs while
// nothing happens: the digest is a timer armed by the first event it holds.
type alerter struct {
	send  forms.Sender // nil: alerts are logged, not mailed
	from  string
	to    []string
	log   *slog.Logger
	now   func() time.Time
	after func(time.Duration, func()) // time.AfterFunc, swappable in tests

	mu      sync.Mutex
	last    map[string]time.Time
	sent    []time.Time
	queue   []alertEvent
	dropped int
	armed   bool
	wg      sync.WaitGroup
}

func newAlerter(cfg config.Config, send forms.Sender, log *slog.Logger) *alerter {
	al := &alerter{
		send: send, to: cfg.AbuseAlertsTo, log: log, now: time.Now,
		after: func(d time.Duration, f func()) { time.AfterFunc(d, f) },
		last:  map[string]time.Time{},
	}
	if cfg.FormsSMTP != nil {
		al.from = cfg.FormsSMTP.From
	} else {
		al.send = nil
	}
	return al
}

// notify mails ev now, or holds it for the digest.
func (al *alerter) notify(ev alertEvent) {
	al.mu.Lock()
	defer al.mu.Unlock()
	now := al.now()
	for k, t := range al.last {
		if now.Sub(t) >= alertPerKey {
			delete(al.last, k)
		}
	}
	keep := al.sent[:0]
	for _, t := range al.sent {
		if now.Sub(t) < time.Hour {
			keep = append(keep, t)
		}
	}
	al.sent = keep
	al.log.Info("abuse alert", "subject", ev.subject)
	if ev.priority {
		al.deliver(ev.subject, ev.body)
		return
	}
	if _, recent := al.last[ev.key]; recent || len(al.sent) >= alertBurst || ev.digestOnly {
		if len(al.queue) < maxQueued {
			al.queue = append(al.queue, ev)
		} else {
			al.dropped++
		}
		if !al.armed {
			al.armed = true
			al.after(digestDelay, al.flush)
		}
		return
	}
	al.last[ev.key] = now
	al.sent = append(al.sent, now)
	al.deliver(ev.subject, ev.body)
}

// flush sends the digest of everything held back.
func (al *alerter) flush() {
	al.mu.Lock()
	q, dropped := al.queue, al.dropped
	al.queue, al.dropped, al.armed = nil, 0, false
	al.mu.Unlock()
	if len(q) == 0 {
		return
	}
	total := len(q) + dropped
	keys := map[string]bool{}
	for _, e := range q {
		keys[e.key] = true
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%d abuse event(s) on %d site(s) or target(s) were held back from immediate mail (one mail per site per hour, %d per hour in all).\n\n", total, len(keys), alertBurst)
	lines := make([]string, 0, len(q))
	for _, e := range q {
		lines = append(lines, e.line)
	}
	sort.Strings(lines)
	for i, l := range lines {
		if i == digestMaxLines {
			fmt.Fprintf(&b, "… and %d more\n", len(lines)-digestMaxLines)
			break
		}
		b.WriteString("  " + l + "\n")
	}
	if dropped > 0 {
		fmt.Fprintf(&b, "\n%d more event(s) arrived after the digest was full and are only in the log.\n", dropped)
	}
	al.deliver(fmt.Sprintf("Digest: %d event(s)", total), b.String())
}

// deliver sends one mail to every recipient, in the background.
func (al *alerter) deliver(subject, body string) {
	subject = "[sitebin abuse] " + subject
	if al.send == nil || len(al.to) == 0 {
		al.log.Warn("abuse alert not mailed: set SITEBIN_ABUSE_ALERTS_TO and the SITEBIN_FORMS_SMTP_* mailer", "subject", subject)
		return
	}
	at := al.now()
	al.wg.Add(1)
	go func() {
		defer al.wg.Done()
		for _, to := range al.to {
			m, err := forms.BuildNotice(forms.Notice{From: al.from, To: to, Subject: subject, Text: body, At: at})
			if err == nil {
				ctx, cancel := context.WithTimeout(context.Background(), alertTimeout)
				err = al.send.Send(ctx, m)
				cancel()
			}
			if err != nil {
				al.log.Error("abuse alert: send failed", "to", to, "subject", subject, "err", err)
			}
		}
	}()
}

// ---- the CSP tripwire ----

const (
	// A site is verified at most tripPerHour times an hour, whatever the
	// destinations its reports name, and at most tripSlots verifications run
	// at once: a report is unauthenticated, and each verification reads the
	// site's files (within store's own byte budget).
	tripPerHour = 6
	tripBurst   = 2
	tripSlots   = 2
)

// tripwire acts on one CSP report: a blocked URL naming a known exfiltration
// destination sends the site's own files to verification (store.CheckExfil),
// which holds the site only when they reference it. doc is the report's
// document URL; when there is one it must be a page of this same site.
func (a *API) tripwire(site *store.Site, blocked, doc string) {
	if blocked == "" {
		return
	}
	rs := a.st.AbuseRules()
	if rs == nil {
		return
	}
	d, ok := rs.Destination(blocked)
	if !ok {
		return
	}
	if doc != "" {
		if s := a.siteByURL(doc); s == nil || s.ViewID != site.ViewID {
			a.log.Info("csp tripwire: report's document is not this site's; ignored", "site", site.ViewID, "destination", d.ID)
			return
		}
	}
	if !a.tripLimiter.Allow(site.ViewID) {
		return
	}
	select {
	case a.tripSlots <- struct{}{}:
		defer func() { <-a.tripSlots }()
	default:
		a.log.Warn("csp tripwire: busy, report not verified", "site", site.ViewID, "destination", d.ID)
		return
	}
	if _, err := a.st.CheckExfil(site, d, blocked); err != nil {
		a.log.Error("csp tripwire: verification failed", "site", site.ViewID, "destination", d.ID, "err", err)
	}
}

// siteByURL resolves a page URL — subdomain view, custom domain, or /v/<id>/
// path view on the main domain — to its site, or nil.
func (a *API) siteByURL(raw string) *store.Site {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" {
		return nil
	}
	if strings.EqualFold(hostWithoutPort(u.Host), a.cfg.BaseDomain) {
		if rest, ok := strings.CutPrefix(u.Path, "/v/"); ok {
			id, _, _ := strings.Cut(rest, "/")
			if site, err := a.st.ByViewID(strings.ToLower(id)); err == nil {
				return site
			}
		}
		return nil
	}
	if site, err := a.siteByHost(u.Host); err == nil {
		return site
	}
	return nil
}

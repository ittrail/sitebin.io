package httpapi

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"html/template"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/ittrail/sitebin.io/internal/store"
)

// The public report page, GET/POST https://<base>/report: how someone who
// found a phishing page on a Sitebin site tells a person about it. It is
// server-rendered and needs no JavaScript — its CSP allows none — so its spam
// protection is the site forms' honeypot plus a signed form ticket with a
// minimum age, not the forms' ALTCHA (a JavaScript proof of work). Behind
// both are the same limits as POST /api/report, which it shares.

const (
	// A ticket must be at least ticketMinAge old (a person reads the page
	// and types) and at most ticketMaxAge (a page left open overnight gets a
	// fresh one, with what was typed kept).
	ticketMinAge = 3 * time.Second
	ticketMaxAge = 2 * time.Hour
)

// reportReasons are the page's choices, stored by label.
var reportReasons = []struct{ Key, Label string }{
	{"phishing", "Phishing or credential theft"},
	{"malware", "Malware or a malicious download"},
	{"fraud", "Fraud or a payment scam"},
	{"spam", "Spam"},
	{"illegal", "Illegal content"},
	{"copyright", "Copyright or trademark infringement"},
	{"other", "Something else"},
}

func reportTicketKey(secret []byte) []byte {
	m := hmac.New(sha256.New, secret)
	m.Write([]byte("report-page:ticket"))
	return m.Sum(nil)
}

// ticket signs the time a form was rendered.
func (a *API) ticket(now time.Time) string {
	ts := strconv.FormatInt(now.Unix(), 10)
	m := hmac.New(sha256.New, a.reportKey)
	m.Write([]byte(ts))
	return ts + "." + hex.EncodeToString(m.Sum(nil))[:32]
}

// ticketAge verifies a ticket and returns how old it is.
func (a *API) ticketAge(t string, now time.Time) (time.Duration, bool) {
	ts, sig, ok := strings.Cut(t, ".")
	if !ok {
		return 0, false
	}
	m := hmac.New(sha256.New, a.reportKey)
	m.Write([]byte(ts))
	if !hmac.Equal([]byte(sig), []byte(hex.EncodeToString(m.Sum(nil))[:32])) {
		return 0, false
	}
	n, err := strconv.ParseInt(ts, 10, 64)
	if err != nil {
		return 0, false
	}
	return now.Sub(time.Unix(n, 0)), true
}

type reportView struct {
	Base    string
	Ticket  string
	Target  string
	Reason  string
	Details string
	Contact string
	Error   string
	Done    bool
	Reasons []struct{ Key, Label string }
}

func (a *API) reportPage(w http.ResponseWriter, r *http.Request) {
	if hostWithoutPort(r.Host) != a.cfg.BaseDomain {
		a.notFoundPage(w, r)
		return
	}
	target := r.URL.Query().Get("url")
	if target == "" {
		target = r.URL.Query().Get("target")
	}
	a.renderReport(w, 200, reportView{Target: strings.TrimSpace(target)})
}

func (a *API) reportSubmit(w http.ResponseWriter, r *http.Request) {
	if hostWithoutPort(r.Host) != a.cfg.BaseDomain {
		a.notFoundPage(w, r)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	if err := r.ParseForm(); err != nil {
		a.renderReport(w, 400, reportView{Error: "The form could not be read. Please try again."})
		return
	}
	v := reportView{
		Target:  strings.TrimSpace(r.PostFormValue("target")),
		Reason:  r.PostFormValue("reason"),
		Details: r.PostFormValue("details"),
		Contact: strings.TrimSpace(r.PostFormValue("contact")),
	}
	// A filled honeypot is a bot: it is thanked and nothing is stored,
	// exactly like a site form's.
	if strings.TrimSpace(r.PostFormValue("_gotcha")) != "" {
		a.renderReport(w, 200, reportView{Done: true})
		return
	}
	age, ok := a.ticketAge(r.PostFormValue("t"), time.Now())
	switch {
	case !ok:
		v.Error = "The form expired. Please send it again."
		a.renderReport(w, 400, v)
		return
	case age < ticketMinAge:
		v.Error = "That was quick — please check the form and send it again."
		a.renderReport(w, 400, v)
		return
	case age > ticketMaxAge:
		v.Error = "The form was open for a long time. Please send it again."
		a.renderReport(w, 400, v)
		return
	}
	label := ""
	for _, rr := range reportReasons {
		if rr.Key == v.Reason {
			label = rr.Label
		}
	}
	if label == "" {
		v.Error = "Please choose a reason."
		a.renderReport(w, 400, v)
		return
	}
	if err := a.fileReport(r, v.Target, label, v.Details, v.Contact, store.ReportViaPage); err != nil {
		code, msg := 500, "The report could not be recorded. Please try again later."
		var ae *apiError
		if asAPIError(err, &ae) {
			code, msg = ae.code, ae.msg
		}
		v.Error = strings.ToUpper(msg[:1]) + msg[1:] + "."
		a.renderReport(w, code, v)
		return
	}
	a.renderReport(w, 200, reportView{Done: true})
}

func (a *API) renderReport(w http.ResponseWriter, status int, v reportView) {
	v.Base = a.cfg.BaseDomain
	v.Ticket = a.ticket(time.Now())
	v.Reasons = reportReasons
	h := w.Header()
	// No script at all, so the page works — and can only work — without it.
	h.Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; img-src data:; form-action 'self'; frame-ancestors 'none'; base-uri 'none'")
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("X-Frame-Options", "DENY")
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	reportTmpl.Execute(w, v)
}

var reportTmpl = template.Must(template.New("report").Parse(`<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Report abuse — Sitebin</title>
<style>
  :root { color-scheme: dark; }
  * { box-sizing: border-box; margin: 0; }
  body {
    min-height: 100dvh; display: grid; place-items: center;
    background:
      radial-gradient(900px 620px at 18% -12%, rgba(91,140,255,.15), transparent 60%),
      radial-gradient(820px 620px at 112% 112%, rgba(245,184,77,.09), transparent 55%),
      #0a0e18;
    color: #eceff7; font: 16px/1.6 ui-sans-serif, system-ui, "Segoe UI", Roboto, sans-serif;
    padding: 24px 16px;
  }
  .card {
    width: min(560px, 100%);
    background: linear-gradient(180deg, rgba(255,255,255,.055), rgba(255,255,255,.018));
    border: 1px solid rgba(255,255,255,.1); border-radius: 18px;
    padding: 34px 30px; box-shadow: 0 30px 80px rgba(0,0,0,.55);
  }
  .badge { font-size: 12.5px; letter-spacing: .12em; text-transform: uppercase; color: #8fa3c8; margin-bottom: 18px; display: block; }
  h1 { font-size: 24px; margin-bottom: 8px; letter-spacing: -.02em; }
  p { color: #9aa5bd; font-size: 15px; }
  p + p { margin-top: 8px; }
  form { margin-top: 22px; display: grid; gap: 6px; }
  label { font-size: 13px; color: #b7c1d8; margin-top: 10px; }
  label .opt { color: #6f7a93; }
  input, select, textarea {
    width: 100%; padding: 11px 13px; border-radius: 10px;
    border: 1px solid rgba(255,255,255,.13); background: rgba(5,8,15,.55);
    color: #eceff7; font: inherit; font-size: 15px;
  }
  textarea { min-height: 120px; resize: vertical; }
  input:focus, select:focus, textarea:focus { outline: none; border-color: #5b8cff; box-shadow: 0 0 0 3px rgba(91,140,255,.22); }
  .trap { position: absolute; left: -9999px; width: 1px; height: 1px; overflow: hidden; }
  button {
    margin-top: 18px; padding: 12px 14px; border: 0; border-radius: 10px; cursor: pointer;
    background: linear-gradient(135deg, #ffd47c, #f5b84d 45%, #d99a26); color: #221902;
    font-size: 15px; font-weight: 650;
  }
  .err { color: #ff9d9d; font-size: 14px; margin-top: 16px; }
  .fine { margin-top: 18px; font-size: 13px; color: #6f7a93; }
  .ok { margin-top: 18px; color: #7fdca4; }
</style>
</head>
<body>
<main class="card">
  <span class="badge">Sitebin · {{.Base}}</span>
  {{if .Done}}
  <h1>Thank you</h1>
  <p class="ok">Your report has been received and will be read by a person.</p>
  <p>If the site is abusive it will be taken offline and kept as evidence. You will hear back only if you left an address and we need to ask something.</p>
  {{else}}
  <h1>Report abuse</h1>
  <p>Tell the operator of {{.Base}} about a site hosted here that phishes for passwords, spreads malware, defrauds people or otherwise breaks the terms of use. Every report is read by a person.</p>
  <form method="post" action="/report">
    <input type="hidden" name="t" value="{{.Ticket}}">
    <label for="target">Address of the site</label>
    <input id="target" name="target" type="text" inputmode="url" required maxlength="400" value="{{.Target}}" placeholder="https://….sitebin.app/">
    <label for="reason">What is wrong with it</label>
    <select id="reason" name="reason" required>
      <option value="">Choose a reason</option>
      {{range .Reasons}}<option value="{{.Key}}"{{if eq .Key $.Reason}} selected{{end}}>{{.Label}}</option>
      {{end}}
    </select>
    <label for="details">Details <span class="opt">(optional)</span></label>
    <textarea id="details" name="details" maxlength="4000" placeholder="What did you see? Where did the link reach you?">{{.Details}}</textarea>
    <label for="contact">Your email <span class="opt">(optional, if you want an answer)</span></label>
    <input id="contact" name="contact" type="email" maxlength="254" value="{{.Contact}}" autocomplete="email">
    <div class="trap" aria-hidden="true"><label for="gotcha">Leave this empty</label><input id="gotcha" name="_gotcha" tabindex="-1" autocomplete="off"></div>
    {{if .Error}}<p class="err" role="alert">{{.Error}}</p>{{end}}
    <button type="submit">Send report</button>
  </form>
  <p class="fine">The report, your email if you give one and the first part of your IP address (the /24 network) are kept for 14 days.</p>
  {{end}}
</main>
</body>
</html>
`))

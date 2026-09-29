// Package abuse recognises phishing and fraud kits in uploaded content and
// the exfiltration destinations such kits call out to. It is pure logic: it
// reads bytes and URLs and says what it found. Where the verdict is applied —
// before an upload becomes visible, when a CSP report arrives, from the CLI —
// is the store's business (internal/store/guard.go). See
// docs/superpowers/specs/2026-09-29-abuse-detection.md.
//
// The rules are data. Built-in defaults are compiled in; an instance adds to
// or overrides them with a JSON file (Loader), so a new kit is one line in a
// file rather than a deploy.
package abuse

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"regexp/syntax"
	"strings"
)

// Severity is what a rule's hit does.
type Severity string

const (
	// Block holds an untrusted site for review: it is locked before the file
	// that matched becomes visible.
	Block Severity = "block"
	// Flag records the hit and alerts the operator, nothing more.
	Flag Severity = "flag"
	// Off disables a rule (in a rules file, to switch off a built-in one).
	Off Severity = "off"
)

// Destination actions: what a CSP report naming the destination does.
const (
	ActionLock  = "lock"
	ActionAlert = "alert"
	ActionOff   = "off"
)

// Rule is one kit signature. A file matches when every All pattern occurs in
// it and, if Any is not empty, at least one Any pattern does — which is how
// "a password field AND a cross-origin exfiltration reference" is said
// without tripping on every legitimate login form.
//
// A pattern is a case-insensitive substring, or a regular expression (RE2)
// when it starts with "re:". Content is lowercased before it is matched, so a
// regular expression is written in lowercase too (escapes such as \S keep
// their meaning). Files, when set, limits the rule to those extensions (no
// dot).
type Rule struct {
	ID          string   `json:"id"`
	Description string   `json:"description,omitempty"`
	Severity    Severity `json:"severity"`
	All         []string `json:"all,omitempty"`
	Any         []string `json:"any,omitempty"`
	Files       []string `json:"files,omitempty"`
}

// Destination is an exfiltration endpoint the CSP-report tripwire watches
// for. URL is a host with an optional path prefix ("api.telegram.org/bot").
type Destination struct {
	ID     string `json:"id"`
	URL    string `json:"url"`
	Action string `json:"action,omitempty"`

	host, path string
}

// Needle is the text a site's content must contain to be proven to reference
// the destination: its host and path prefix as written.
func (d Destination) Needle() string { return d.host + d.path }

// pw is a password input, written as markup or built from script.
const pw = `re:type\s*[=:]\s*["'` + "`" + `]?password\b`

var markup = []string{"html", "htm", "xhtml", "shtml", "svg"}

// defaultRules come from the two kits of 2026-09-25…28 (checked against the
// quarantined copies) and the shapes such kits usually take. The design doc
// has the table and the reasoning behind each severity.
var defaultRules = []Rule{
	{ID: "telegram-bot-api", Severity: Block, Any: []string{"api.telegram.org/bot"},
		Description: "calls the Telegram bot API, how credential harvesters deliver what they collect"},
	{ID: "telegram-sender", Severity: Block, All: []string{"sendtotelegram"},
		Description: "a sendToTelegram function, the harvester kits' own name for it"},
	{ID: "chat-webhook", Severity: Block,
		Any:         []string{"discord.com/api/webhooks", "discordapp.com/api/webhooks", "hooks.slack.com/services", "hooks.slack.com/workflows"},
		Description: "posts to a Discord or Slack webhook"},
	{ID: "botcheck-password", Severity: Block, All: []string{`re:id\s*=\s*["']?botcheck\b`, pw},
		Description: "the harvester kit's honeypot field next to a password input"},
	{ID: "clearbit-logo-password", Severity: Block, All: []string{"logo.clearbit.com", pw},
		Description: "fetches the victim company's logo next to a password input"},
	{ID: "dns-lookup-password", Severity: Block, Any: []string{"dns.google/resolve", "cloudflare-dns.com/dns-query"}, All: []string{pw},
		Description: "looks up the victim's mail servers next to a password input"},
	{ID: "ip-lookup-password", Severity: Block,
		Any:         []string{"api.ipify.org", "api64.ipify.org", "ip-api.com", "ipapi.co", "ipinfo.io", "api.db-ip.com"},
		All:         []string{pw},
		Description: "looks up the visitor's IP address next to a password input"},
	{ID: "hash-email-password", Severity: Block, All: []string{"atob(", "location.hash", pw}, Files: markup,
		Description: "decodes the victim's address from the URL fragment next to a password input"},
	{ID: "brand-login-title", Severity: Block,
		All: []string{
			`re:<title[^>]*>[^<]{0,200}(microsoft|office\s*365|outlook|onedrive|sharepoint|docusign|adobe|wetransfer|account verification|verify your account)`,
			pw,
		},
		Description: "a page titled after a brand's or an account sign-in, with a password input"},
	{ID: "brand-login-text", Severity: Block,
		All: []string{`re:\b(sign|log)\s*-?\s*in\b`, pw},
		Any: []string{"office 365", "office365", "onedrive", "sharepoint", "docusign", "wetransfer",
			"outlook web", "microsoft account", "adobe document cloud", "adobe sign", "adobe pdf"},
		Description: "a sign-in form next to a brand whose login it imitates"},
	{ID: "lure-login-text", Severity: Block,
		Any: []string{"sign in to your microsoft account", "sign in with your email to view", "login with your email to view",
			"log in with your email to view", "to view the shared document", "to view the document", "to access the shared file",
			"verify your email account", "confirm your email password", "your mailbox is full", "mailbox storage limit"},
		All:         []string{pw},
		Description: "a lure sentence next to a password input"},
	{ID: "wallet-deeplink", Severity: Block, Any: []string{"tez://upi", "phonepe://", "paytmmp://", "gpay://"},
		Description: "opens a payment app on a hard-coded payee, the UPI scam page's mechanism"},
	{ID: "upi-deeplink", Severity: Flag, Any: []string{"upi://pay"},
		Description: "a UPI payment link (legitimate merchants use these too)"},
	{ID: "ipfs-script", Severity: Flag, Any: []string{`re:<script[^>]+src\s*=\s*["']?[^"'>\s]*(ipfs\.io|cloudflare-ipfs\.com|dweb\.link)/`},
		Description: "loads a script from an IPFS gateway"},
	{ID: "obfuscation-eval-atob", Severity: Flag, Any: []string{`re:eval\s*\(\s*(window\.)?atob\s*\(`},
		Description: "evaluates base64-decoded code"},
	{ID: "obfuscation-charcode", Severity: Flag, Any: []string{`re:fromcharcode\s*\(\s*(\d+\s*,\s*){40,}`},
		Description: "a long String.fromCharCode chain"},
	{ID: "obfuscation-unescape", Severity: Flag, Any: []string{`re:unescape\s*\(\s*["'](%[0-9a-f]{2}){60,}`},
		Description: "a long percent-encoded unescape() blob"},
}

// defaultDestinations are where kits send what they collect, or look up who
// their visitor is. The untrusted-tier CSP (connect-src 'self', form-action
// 'none') blocks them; the report it produces is the tripwire.
var defaultDestinations = []Destination{
	{ID: "telegram-bot", URL: "api.telegram.org/bot"},
	{ID: "discord-webhook", URL: "discord.com/api/webhooks"},
	{ID: "discordapp-webhook", URL: "discordapp.com/api/webhooks"},
	{ID: "slack-hook", URL: "hooks.slack.com"},
	{ID: "ipify", URL: "api.ipify.org"},
	{ID: "ipapi-co", URL: "ipapi.co"},
	{ID: "ip-api", URL: "ip-api.com"},
	{ID: "ipinfo", URL: "ipinfo.io"},
	{ID: "db-ip", URL: "api.db-ip.com"},
	{ID: "google-script", URL: "script.google.com/macros"},
	{ID: "formspree", URL: "formspree.io"},
	{ID: "getform", URL: "getform.io"},
	{ID: "formsubmit", URL: "formsubmit.co"},
	{ID: "submit-form", URL: "submit-form.com"},
	{ID: "emailjs", URL: "api.emailjs.com"},
	{ID: "webhook-site", URL: "webhook.site"},
	{ID: "pipedream", URL: "pipedream.net"},
}

// pattern is one compiled pattern: a lowercase literal or a regexp. need is
// a literal every match of the regexp contains, when it has one: a chunk
// without it is skipped without running the regexp, which is most of the
// scan's speed.
type pattern struct {
	src  string
	lit  []byte
	re   *regexp.Regexp
	need []byte
}

type compiledRule struct {
	Rule
	all, any []int // indexes into RuleSet.patterns
	files    map[string]bool
}

// RuleSet is a compiled, immutable set of rules and destinations. It is safe
// for concurrent use.
type RuleSet struct {
	rules    []compiledRule
	patterns []pattern
	dests    []Destination
}

// Rules and Destinations report what the set holds, for logs and the CLI.
func (rs *RuleSet) Rules() int        { return len(rs.rules) }
func (rs *RuleSet) Destinations() int { return len(rs.dests) }

var defaultSet = mustCompile(defaultRules, defaultDestinations)

// Defaults returns the compiled-in rule set.
func Defaults() *RuleSet { return defaultSet }

func mustCompile(rules []Rule, dests []Destination) *RuleSet {
	rs, err := compile(rules, dests)
	if err != nil {
		panic("abuse: built-in rules: " + err.Error())
	}
	return rs
}

var idRe = regexp.MustCompile(`^[a-z0-9][a-z0-9:._-]{0,63}$`)

const (
	maxRules      = 500
	maxPatternLen = 1000
)

// compile validates and compiles rules and destinations. Rules with severity
// Off and destinations with action off are dropped here, after validation.
func compile(rules []Rule, dests []Destination) (*RuleSet, error) {
	if len(rules) > maxRules || len(dests) > maxRules {
		return nil, fmt.Errorf("at most %d rules and %d destinations", maxRules, maxRules)
	}
	rs := &RuleSet{}
	index := map[string]int{}
	add := func(src string) (int, error) {
		if i, ok := index[src]; ok {
			return i, nil
		}
		if strings.TrimSpace(src) == "" || len(src) > maxPatternLen {
			return 0, fmt.Errorf("pattern %q is empty or longer than %d characters", src, maxPatternLen)
		}
		p := pattern{src: src}
		if expr, ok := strings.CutPrefix(src, "re:"); ok {
			re, err := regexp.Compile(expr)
			if err != nil {
				return 0, fmt.Errorf("pattern %q: %v", src, err)
			}
			p.re, p.need = re, requiredLiteral(expr)
		} else {
			p.lit = []byte(strings.ToLower(src))
		}
		rs.patterns = append(rs.patterns, p)
		index[src] = len(rs.patterns) - 1
		return index[src], nil
	}
	seen := map[string]bool{}
	for _, r := range rules {
		if !idRe.MatchString(r.ID) {
			return nil, fmt.Errorf("rule id %q: use lowercase letters, digits and - . : _", r.ID)
		}
		if seen[r.ID] {
			return nil, fmt.Errorf("rule %q appears twice", r.ID)
		}
		seen[r.ID] = true
		switch r.Severity {
		case Block, Flag:
		case Off:
			continue
		default:
			return nil, fmt.Errorf("rule %q: severity %q (want block, flag or off)", r.ID, r.Severity)
		}
		if len(r.All)+len(r.Any) == 0 {
			return nil, fmt.Errorf("rule %q has no patterns", r.ID)
		}
		cr := compiledRule{Rule: r}
		for _, src := range r.All {
			i, err := add(src)
			if err != nil {
				return nil, fmt.Errorf("rule %q: %w", r.ID, err)
			}
			cr.all = append(cr.all, i)
		}
		for _, src := range r.Any {
			i, err := add(src)
			if err != nil {
				return nil, fmt.Errorf("rule %q: %w", r.ID, err)
			}
			cr.any = append(cr.any, i)
		}
		if len(r.Files) > 0 {
			cr.files = map[string]bool{}
			for _, ext := range r.Files {
				cr.files[strings.ToLower(strings.TrimPrefix(ext, "."))] = true
			}
		}
		rs.rules = append(rs.rules, cr)
	}
	seen = map[string]bool{}
	for _, d := range dests {
		if !idRe.MatchString(d.ID) {
			return nil, fmt.Errorf("destination id %q: use lowercase letters, digits and - . : _", d.ID)
		}
		if seen[d.ID] {
			return nil, fmt.Errorf("destination %q appears twice", d.ID)
		}
		seen[d.ID] = true
		switch d.Action {
		case "":
			d.Action = ActionLock
		case ActionLock, ActionAlert:
		case ActionOff:
			continue
		default:
			return nil, fmt.Errorf("destination %q: action %q (want lock, alert or off)", d.ID, d.Action)
		}
		host, path, err := splitDestination(d.URL)
		if err != nil {
			return nil, fmt.Errorf("destination %q: %w", d.ID, err)
		}
		d.host, d.path = host, path
		rs.dests = append(rs.dests, d)
	}
	return rs, nil
}

// requiredLiteral finds a literal that every match of expr contains: the
// expression itself when it is one, else the longest literal directly inside
// its top-level concatenation. nil when there is none (an alternation at the
// top, say), and the regexp then runs on every chunk.
func requiredLiteral(expr string) []byte {
	re, err := syntax.Parse(expr, syntax.Perl)
	if err != nil {
		return nil
	}
	re = re.Simplify()
	for re.Op == syntax.OpCapture && len(re.Sub) == 1 {
		re = re.Sub[0]
	}
	lit := func(r *syntax.Regexp) []byte {
		if r.Op != syntax.OpLiteral || r.Flags&syntax.FoldCase != 0 {
			return nil
		}
		return []byte(string(r.Rune))
	}
	if l := lit(re); l != nil {
		return l
	}
	var best []byte
	if re.Op == syntax.OpConcat {
		for _, sub := range re.Sub {
			if l := lit(sub); len(l) > len(best) {
				best = l
			}
		}
	}
	return best
}

// splitDestination turns "host[/path]" (a scheme is tolerated) into its
// lowercase host and path prefix.
func splitDestination(raw string) (host, path string, err error) {
	raw = strings.ToLower(strings.TrimSpace(raw))
	raw = strings.TrimPrefix(strings.TrimPrefix(raw, "https://"), "http://")
	host, path, _ = strings.Cut(raw, "/")
	if host == "" || strings.ContainsAny(host, " :@?#") {
		return "", "", fmt.Errorf("url %q must be a host with an optional path, such as api.example.com/hook", raw)
	}
	if path != "" {
		path = "/" + strings.TrimSuffix(path, "/")
	}
	return host, path, nil
}

// Destination reports which destination, if any, a CSP report's blocked URL
// names. A report that carries only the origin (some browsers strip the path
// of a cross-origin URL) matches a destination by host alone.
func (rs *RuleSet) Destination(blocked string) (Destination, bool) {
	blocked = strings.TrimSpace(blocked)
	if blocked == "" {
		return Destination{}, false
	}
	if !strings.Contains(blocked, "://") {
		blocked = "https://" + blocked
	}
	u, err := url.Parse(blocked)
	if err != nil || u.Hostname() == "" {
		return Destination{}, false
	}
	host := strings.ToLower(u.Hostname())
	p := strings.ToLower(u.EscapedPath())
	for _, d := range rs.dests {
		if host != d.host && !strings.HasSuffix(host, "."+d.host) {
			continue
		}
		if d.path == "" || p == "" || p == "/" || strings.HasPrefix(p, d.path) {
			return d, true
		}
	}
	return Destination{}, false
}

// DestinationRules is a rule set with the one rule a tripwire verification
// needs: the destination's needle, as rule "csp:<id>", blocking when the
// destination's action is lock.
func DestinationRules(d Destination) *RuleSet {
	sev := Flag
	if d.Action == ActionLock {
		sev = Block
	}
	rs, err := compile([]Rule{{ID: "csp:" + d.ID, Severity: sev, All: []string{d.Needle()}}}, nil)
	if err != nil {
		// A destination is validated when its set is compiled, so its needle
		// always compiles; an empty set is the safe answer regardless.
		return &RuleSet{}
	}
	return rs
}

// fileFormat is the rules file on the instance. See the design doc.
type fileFormat struct {
	// Defaults is "extend" (the default: merge by id) or "replace" (drop
	// every built-in rule and destination).
	Defaults string        `json:"defaults"`
	Rules    []Rule        `json:"rules"`
	Exfil    []Destination `json:"exfil"`
}

// Parse reads a rules file and merges it with the built-in defaults. An
// entry whose id names a built-in replaces it; one with no patterns (or no
// url) keeps the built-in's and changes only its severity (or action).
func Parse(b []byte) (*RuleSet, error) {
	var f fileFormat
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&f); err != nil {
		return nil, fmt.Errorf("parse: %w", err)
	}
	var rules []Rule
	var dests []Destination
	switch f.Defaults {
	case "", "extend":
		rules = append(rules, defaultRules...)
		dests = append(dests, defaultDestinations...)
	case "replace":
	default:
		return nil, fmt.Errorf(`defaults %q (want "extend" or "replace")`, f.Defaults)
	}
	ruleAt := map[string]int{}
	for i, r := range rules {
		ruleAt[r.ID] = i
	}
	fileIDs := map[string]bool{}
	for _, r := range f.Rules {
		if fileIDs[r.ID] {
			return nil, fmt.Errorf("rule %q appears twice", r.ID)
		}
		fileIDs[r.ID] = true
		i, builtin := ruleAt[r.ID]
		if len(r.All)+len(r.Any) == 0 {
			if !builtin {
				if r.Severity == Off {
					return nil, fmt.Errorf("rule %q: there is no built-in rule of that id to switch off", r.ID)
				}
				return nil, fmt.Errorf("rule %q has no patterns", r.ID)
			}
			base := rules[i]
			base.Severity = r.Severity
			if r.Description != "" {
				base.Description = r.Description
			}
			if r.Files != nil {
				base.Files = r.Files
			}
			r = base
		}
		if builtin {
			rules[i] = r
		} else {
			ruleAt[r.ID] = len(rules)
			rules = append(rules, r)
		}
	}
	destAt := map[string]int{}
	for i, d := range dests {
		destAt[d.ID] = i
	}
	fileIDs = map[string]bool{}
	for _, d := range f.Exfil {
		if fileIDs[d.ID] {
			return nil, fmt.Errorf("destination %q appears twice", d.ID)
		}
		fileIDs[d.ID] = true
		i, builtin := destAt[d.ID]
		if strings.TrimSpace(d.URL) == "" {
			if !builtin {
				return nil, fmt.Errorf("destination %q has no url", d.ID)
			}
			base := dests[i]
			base.Action = d.Action
			d = base
		}
		if builtin {
			dests[i] = d
		} else {
			destAt[d.ID] = len(dests)
			dests = append(dests, d)
		}
	}
	return compile(rules, dests)
}

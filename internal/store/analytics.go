package store

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	_ "time/tzdata" // LoadLocation works without system zoneinfo (Alpine, Windows)

	"github.com/ittrail/sitebin.io/internal/ids"
)

// Limits on a site's analytics settings.
const (
	maxAnalyticsExclude = 50
	maxAnalyticsQuery   = 20
	maxAnalyticsFunnels = 20
	maxAnalyticsGoals   = 30
	minFunnelSteps      = 2
	maxFunnelSteps      = 8
	maxFunnelWindow     = 1440
	maxGlobLen          = 300
	maxEventName        = 50
)

// AnalyticsStep is one step of a funnel: a page path (a glob) or a named event.
type AnalyticsStep struct {
	Type  string `json:"type"` // "path" | "event"
	Value string `json:"value"`
}

// AnalyticsFunnel is an ordered series of steps a visit completes within
// WindowMinutes.
type AnalyticsFunnel struct {
	ID            string          `json:"id"`
	Name          string          `json:"name"`
	Steps         []AnalyticsStep `json:"steps"`
	WindowMinutes int             `json:"window_minutes"`
}

// AnalyticsGoal counts visits that reach a path (a glob) or fire an event.
type AnalyticsGoal struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Type  string `json:"type"` // "path" | "event"
	Value string `json:"value"`
}

// Analytics is a site's analytics settings. A site without them (nil in Meta)
// collects nothing.
type Analytics struct {
	Enabled   bool              `json:"enabled"`
	Mode      string            `json:"mode"`     // "server" | "script"; "" = server
	Timezone  string            `json:"timezone"` // "" = instance default
	Addresses []string          `json:"addresses,omitempty"`
	Exclude   []string          `json:"exclude,omitempty"`
	Query     []string          `json:"query,omitempty"`
	Funnels   []AnalyticsFunnel `json:"funnels,omitempty"`
	Goals     []AnalyticsGoal   `json:"goals,omitempty"`
}

var queryNameRE = regexp.MustCompile(`^[A-Za-z0-9_.\-\[\]]{1,50}$`)

// Validate checks the settings against siteAddresses (the site's own
// addresses) and returns the offending field with the reason, or ("", nil).
func (a Analytics) Validate(siteAddresses []string) (string, error) {
	if a.Mode != "" && a.Mode != "server" && a.Mode != "script" {
		return "mode", errors.New(`mode must be "server" or "script"`)
	}
	if a.Timezone != "" {
		if _, err := time.LoadLocation(a.Timezone); err != nil {
			return "timezone", fmt.Errorf("unknown time zone %q", a.Timezone)
		}
	}
	own := make(map[string]bool, len(siteAddresses))
	for _, s := range siteAddresses {
		own[s] = true
	}
	for _, ad := range a.Addresses {
		if !own[ad] {
			return "addresses", fmt.Errorf("%q is not an address of this site", ad)
		}
	}
	if len(a.Exclude) > maxAnalyticsExclude {
		return "exclude", fmt.Errorf("at most %d excluded paths", maxAnalyticsExclude)
	}
	for _, p := range a.Exclude {
		if !GlobOK(p) {
			return "exclude", fmt.Errorf("excluded path %q must start with / and be at most %d characters", p, maxGlobLen)
		}
	}
	if len(a.Query) > maxAnalyticsQuery {
		return "query", fmt.Errorf("at most %d query parameters", maxAnalyticsQuery)
	}
	for _, q := range a.Query {
		if !queryNameRE.MatchString(q) {
			return "query", fmt.Errorf("query parameter name %q is not allowed", q)
		}
	}
	if len(a.Funnels) > maxAnalyticsFunnels {
		return "funnels", fmt.Errorf("at most %d funnels", maxAnalyticsFunnels)
	}
	seen := map[string]bool{}
	for _, f := range a.Funnels {
		if f.ID == "" || f.Name == "" {
			return "funnels", errors.New("a funnel needs an id and a name")
		}
		if seen[f.ID] {
			return "funnels", fmt.Errorf("duplicate funnel id %q", f.ID)
		}
		seen[f.ID] = true
		if len(f.Steps) < minFunnelSteps || len(f.Steps) > maxFunnelSteps {
			return "funnels", fmt.Errorf("funnel %q needs %d to %d steps", f.ID, minFunnelSteps, maxFunnelSteps)
		}
		if f.WindowMinutes < 1 || f.WindowMinutes > maxFunnelWindow {
			return "funnels", fmt.Errorf("funnel %q window must be 1 to %d minutes", f.ID, maxFunnelWindow)
		}
		for _, st := range f.Steps {
			if err := analyticsTarget(st.Type, st.Value); err != nil {
				return "funnels", fmt.Errorf("funnel %q: %w", f.ID, err)
			}
		}
	}
	if len(a.Goals) > maxAnalyticsGoals {
		return "goals", fmt.Errorf("at most %d goals", maxAnalyticsGoals)
	}
	seen = map[string]bool{}
	for _, g := range a.Goals {
		if g.ID == "" || g.Name == "" {
			return "goals", errors.New("a goal needs an id and a name")
		}
		if seen[g.ID] {
			return "goals", fmt.Errorf("duplicate goal id %q", g.ID)
		}
		seen[g.ID] = true
		if err := analyticsTarget(g.Type, g.Value); err != nil {
			return "goals", fmt.Errorf("goal %q: %w", g.ID, err)
		}
	}
	return "", nil
}

// analyticsTarget validates a step or goal: a path glob or a non-empty event name.
func analyticsTarget(typ, value string) error {
	switch typ {
	case "path":
		if !GlobOK(value) {
			return fmt.Errorf("path %q must start with / and be at most %d characters", value, maxGlobLen)
		}
	case "event":
		if value == "" || len(value) > maxEventName {
			return fmt.Errorf("an event name is 1 to %d characters", maxEventName)
		}
	default:
		return fmt.Errorf(`type must be "path" or "event", not %q`, typ)
	}
	return nil
}

// compileGlob turns a path glob into an anchored regexp: "*" is [^/]*, "**"
// is .*, everything else is literal.
func compileGlob(pattern string) (*regexp.Regexp, bool) {
	if !GlobOK(pattern) {
		return nil, false
	}
	var b strings.Builder
	b.WriteString("^")
	for i := 0; i < len(pattern); i++ {
		switch c := pattern[i]; {
		case c == '*' && i+1 < len(pattern) && pattern[i+1] == '*':
			b.WriteString(".*")
			i++
		case c == '*':
			b.WriteString("[^/]*")
		default:
			b.WriteString(regexp.QuoteMeta(string(c)))
		}
	}
	b.WriteString("$")
	re, err := regexp.Compile(b.String())
	return re, err == nil
}

// GlobOK reports whether pattern is a usable path glob: it starts with "/"
// and is at most 300 bytes. "*" stands for characters within one path
// segment, "**" for anything.
func GlobOK(pattern string) bool {
	return pattern != "" && pattern[0] == '/' && len(pattern) <= maxGlobLen
}

// GlobMatch reports whether path matches pattern in full. An invalid pattern
// matches nothing.
func GlobMatch(pattern, path string) bool {
	re, ok := compileGlob(pattern)
	return ok && re.MatchString(path)
}

// SetAnalytics replaces the site's analytics settings; nil clears them.
func (s *Store) SetAnalytics(site *Site, a *Analytics) error {
	return s.Update(site, func(m *Meta) error {
		if a == nil {
			m.Analytics = nil
		} else {
			c := *a
			m.Analytics = &c
		}
		return nil
	})
}

// SetQuotaAnalytics writes the site's analytics stamp and nothing else, like
// SetQuotaGit.
func (s *Store) SetQuotaAnalytics(site *Site, v *bool) error {
	return s.Update(site, func(m *Meta) error {
		if v == nil {
			m.QuotaAnalytics = nil
		} else {
			b := *v
			m.QuotaAnalytics = &b
		}
		return nil
	})
}

// WithStatsLock runs fn holding the site's stats lock, with the site folder
// as argument. A missing folder returns (false, nil) without calling fn, so a
// writer racing a delete cannot recreate the folder (Delete takes the site
// lock, then this one).
func (s *Store) WithStatsLock(viewID string, fn func(siteDir string) error) (bool, error) {
	if !ids.ValidID(viewID) {
		return false, nil
	}
	l := s.lockStats(viewID)
	l.Lock()
	defer l.Unlock()
	dir := filepath.Join(s.sitesDir(), viewID)
	if _, err := os.Stat(metaPath(dir)); err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	return true, fn(dir)
}

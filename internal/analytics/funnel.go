package analytics

import (
	"cmp"
	"slices"
	"time"

	"github.com/ittrail/sitebin.io/internal/store"
)

// StepResult is one step of a funnel: the visitors who reached it, how many
// of the step before's did not (0 for the first step), and the share of the
// first step's visitors who reached it (0 when the first step has none).
type StepResult struct {
	Visitors   int     `json:"visitors"`
	DropOff    int     `json:"drop_off"`
	Conversion float64 `json:"conversion"`
}

// FunnelResult is a saved funnel's answer, one result per step.
type FunnelResult struct {
	ID    string       `json:"id"`
	Name  string       `json:"name"`
	Steps []StepResult `json:"steps"`
}

// GoalResult is a saved goal's answer: the kept records matching it, the
// visitors with at least one, and their share of all visitors.
type GoalResult struct {
	ID          string  `json:"id"`
	Name        string  `json:"name"`
	Completions int     `json:"completions"`
	Visitors    int     `json:"visitors"`
	Rate        float64 `json:"rate"`
}

// Funnels answers saved funnels over q as Umami counts them, per visitor
// and day. A visitor-day reaches step k when kept records at t1 < t2 < … <
// tk match the steps in order, each within the funnel's window after the
// one before (t ≤ previous + window), and any such chain counts, not only
// one from the first step-1 record. A visitor id lives one day, so a chain
// never crosses midnight, and a step's visitors are summed over days, like
// visitors everywhere.
//
// Records are kept as every query keeps them: in range and under q's
// filters. Funnels always count the raw records, never a day summary.
func (e *Engine) Funnels(siteDir string, q Query, fs []store.AnalyticsFunnel) ([]FunnelResult, error) {
	c, err := compile(q.From, q.To, q.Loc, q.Filters)
	if err != nil {
		return nil, err
	}
	accs := make([]*funnelAcc, len(fs))
	for i := range fs {
		accs[i] = newFunnelAcc(&fs[i])
	}
	if len(fs) > 0 {
		var g visitorRuns
		err = e.scan(siteDir, c, func(s *dayScan) {
			g.group(s)
			for _, a := range accs {
				a.add(s, &g)
			}
		}, nil)
		if err != nil {
			return nil, err
		}
	}
	out := make([]FunnelResult, len(fs))
	for i, a := range accs {
		out[i] = a.result(&fs[i])
	}
	return out, nil
}

// Goals answers saved goals over q: a goal's completions are the kept
// records matching it, its visitors the visitors with at least one, summed
// over days, and its rate their share of all the visitors q keeps (the
// overview's visitors under the same filters). Goals always count the raw
// records, never a day summary.
func (e *Engine) Goals(siteDir string, q Query, gs []store.AnalyticsGoal) ([]GoalResult, error) {
	c, err := compile(q.From, q.To, q.Loc, q.Filters)
	if err != nil {
		return nil, err
	}
	out := make([]GoalResult, len(gs))
	targets := make([]target, len(gs))
	for i, g := range gs {
		out[i] = GoalResult{ID: g.ID, Name: g.Name}
		targets[i] = newTarget(g.Type, g.Value)
	}
	if len(gs) == 0 {
		return out, nil
	}
	all := 0
	var g visitorRuns
	err = e.scan(siteDir, c, func(s *dayScan) {
		g.group(s)
		all += g.len()
		for v := range g.len() {
			run := g.run(v)
			for j := range targets {
				n := 0
				for _, i := range run {
					if targets[j].matches(&s.d.recs[i]) {
						n++
					}
				}
				if n > 0 {
					out[j].Completions += n
					out[j].Visitors++
				}
			}
		}
	}, nil)
	if err != nil {
		return nil, err
	}
	if all > 0 {
		for i := range out {
			out[i].Rate = float64(out[i].Visitors) / float64(all)
		}
	}
	return out, nil
}

// target is what a funnel step or a goal matches: page views whose path
// matches a glob, or events of one name. One that does not compile (an
// unknown type, an invalid glob) matches nothing.
type target struct {
	kind  string      // the record kind it matches, "p" or "e"; "" matches nothing
	glob  *store.Glob // for "p"
	event string      // for "e"
}

func newTarget(typ, value string) target {
	switch typ {
	case "path":
		if g, ok := store.CompileGlob(value); ok {
			return target{kind: "p", glob: g}
		}
	case "event":
		return target{kind: "e", event: value}
	}
	return target{}
}

func (t *target) matches(r *Record) bool {
	switch {
	case t.kind == "" || r.K != t.kind:
		return false
	case t.kind == "p":
		return t.glob.Match(r.P)
	}
	return r.N == t.event
}

// visitorRuns is a day's kept records grouped by visitor: the records'
// indexes, each visitor's in a run in time order, and where each run
// starts, with the end of the last.
type visitorRuns struct {
	idx    []int32
	starts []int32
}

func (g *visitorRuns) group(s *dayScan) {
	g.idx = g.idx[:0]
	for i := s.lo; i < s.hi; i++ {
		if s.selected(i) {
			g.idx = append(g.idx, int32(i))
		}
	}
	visitor := s.d.visitor
	// Stable: records come in time order and stay so within a run.
	slices.SortStableFunc(g.idx, func(a, b int32) int { return cmp.Compare(visitor[a], visitor[b]) })
	g.starts = g.starts[:0]
	for j, i := range g.idx {
		if j == 0 || visitor[i] != visitor[g.idx[j-1]] {
			g.starts = append(g.starts, int32(j))
		}
	}
	g.starts = append(g.starts, int32(len(g.idx)))
}

// len is the number of visitors with a kept record.
func (g *visitorRuns) len() int { return len(g.starts) - 1 }

// run is the v-th visitor's kept records.
func (g *visitorRuns) run(v int) []int32 { return g.idx[g.starts[v]:g.starts[v+1]] }

// funnelAcc counts the visitor-days reaching each step of one funnel.
type funnelAcc struct {
	steps    []target
	window   int64     // ms
	visitors []int     // per step
	times    [][]int64 // per step, scratch: one visitor's matching times
}

func newFunnelAcc(f *store.AnalyticsFunnel) *funnelAcc {
	a := &funnelAcc{
		steps:    make([]target, len(f.Steps)),
		window:   int64(f.WindowMinutes) * time.Minute.Milliseconds(),
		visitors: make([]int, len(f.Steps)),
		times:    make([][]int64, len(f.Steps)),
	}
	for i, st := range f.Steps {
		a.steps[i] = newTarget(st.Type, st.Value)
	}
	return a
}

func (a *funnelAcc) add(s *dayScan, g *visitorRuns) {
	for v := range g.len() {
		for k := range a.times {
			a.times[k] = a.times[k][:0]
		}
		for _, i := range g.run(v) {
			r := &s.d.recs[i]
			for k := range a.steps {
				if a.steps[k].matches(r) {
					a.times[k] = append(a.times[k], r.T)
				}
			}
		}
		for k := range a.reached() {
			a.visitors[k]++
		}
	}
}

// reached is the number of steps one visitor-day reaches, from its
// matching times per step. Step 1's times are reachable; a later step's
// time is reachable when it comes after a reachable time of the step
// before by at most the window. The latest reachable time before t is the
// one to try, so with both lists in time order each step is one merge. A
// step's times are cut down to its reachable ones in place.
func (a *funnelAcc) reached() int {
	if len(a.times) == 0 || len(a.times[0]) == 0 {
		return 0
	}
	for k := 1; k < len(a.times); k++ {
		prev, cur := a.times[k-1], a.times[k]
		n, j := 0, 0
		for _, t := range cur {
			for j < len(prev) && prev[j] < t {
				j++
			}
			if j > 0 && t-prev[j-1] <= a.window {
				cur[n] = t
				n++
			}
		}
		if n == 0 {
			return k
		}
		a.times[k] = cur[:n]
	}
	return len(a.times)
}

func (a *funnelAcc) result(f *store.AnalyticsFunnel) FunnelResult {
	r := FunnelResult{ID: f.ID, Name: f.Name, Steps: make([]StepResult, len(a.visitors))}
	for k, n := range a.visitors {
		st := StepResult{Visitors: n}
		if k > 0 {
			st.DropOff = a.visitors[k-1] - n
		}
		if first := a.visitors[0]; first > 0 {
			st.Conversion = float64(n) / float64(first)
		}
		r.Steps[k] = st
	}
	return r
}

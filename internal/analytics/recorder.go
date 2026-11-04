package analytics

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"sync"
	"sync/atomic"
	"time"
)

const (
	recorderBuffer  = 8192            // hits queued for the writer
	flushEvery      = time.Second     // a hit waits at most this long
	flushRecords    = 256             // ...or until this many records wait
	drainTimeout    = 2 * time.Second // the writer's last drain on shutdown
	dropLogEvery    = time.Minute     // drops are logged at most this often
	defaultDailyCap = 50000           // records per site and local day
)

// Hit is what a request hands the recorder: raw request facts, never
// sanitized and never stored as they are. The writer turns it into a Record.
type Hit struct {
	Time                 time.Time // zero: the recorder's Now
	ViewID, Kind, Source string    // Kind "p"|"e"; Source "srv"|"js"

	Address, RawPath, RawQuery, Referrer   string
	IP, UA, CHMobile, CHPlatform, Language string // Language: first Accept-Language tag or script value

	Title, Screen, Tag, Name string
	Data                     map[string]any

	Own        []string       // the site's addresses: a referrer from one is not kept
	ExtraQuery []string       // the site's own query names to keep
	TZ         *time.Location // the site's zone; nil is UTC

	PageCheck func() bool // nil = no check; false = drop (404)
}

// RecorderConfig wires a Recorder. WithSite is store.WithStatsLock: it runs
// fn under the site's stats lock and reports found=false, without calling
// fn, when the site is gone. WithSite and Salts are required; Geo may be
// nil.
type RecorderConfig struct {
	DailyCap int // <= 0: 50000
	Salts    *Salts
	Geo      *Geo
	WithSite func(viewID string, fn func(siteDir string) error) (bool, error)
	Now      func() time.Time // nil: time.Now
	Log      *slog.Logger     // nil: slog.Default()
}

// Recorder turns hits into stored records. Add only queues; one writer
// (Run) parses, hashes, looks up and sanitizes, and appends per site and
// day under the site's stats lock.
type Recorder struct {
	c        RecorderConfig
	ch       chan Hit
	flushReq chan chan struct{}
	dropped  atomic.Int64

	runMu   sync.Mutex
	runDone chan struct{} // non-nil while Run is active, closed when it returns

	// The writer's state. wmu keeps it to one goroutine at a time: Run's
	// loop, or Flush while Run is not active.
	wmu         sync.Mutex
	batches     map[batchKey][][]byte // encoded record lines
	pending     int
	days        map[string]dayCount // viewID → the day whose lines are counted
	prunedDay   string              // the UTC date salts were last pruned on
	loggedDrops int64
	lastDropLog time.Time
}

type batchKey struct{ viewID, date string }

// dayCount is the number of lines in a site's day file, kept so the cap
// check does not read the file on every flush. size is the file's size
// after the last count or append: a file of another size was rewritten
// (an import) and is counted again. cut is a file ending inside a line.
type dayCount struct {
	date  string
	lines int
	size  int64
	cut   bool
}

// NewRecorder returns a recorder; it records nothing until Run is started
// (or Flush is called).
func NewRecorder(c RecorderConfig) *Recorder {
	if c.DailyCap <= 0 {
		c.DailyCap = defaultDailyCap
	}
	if c.Now == nil {
		c.Now = time.Now
	}
	if c.Log == nil {
		c.Log = slog.Default()
	}
	return &Recorder{
		c:        c,
		ch:       make(chan Hit, recorderBuffer),
		flushReq: make(chan chan struct{}),
		batches:  map[batchKey][][]byte{},
		days:     map[string]dayCount{},
	}
}

// Add queues a hit without ever blocking. It returns false, and counts the
// hit as dropped, when the queue is full.
func (r *Recorder) Add(h Hit) bool {
	select {
	case r.ch <- h:
		return true
	default:
		r.dropped.Add(1)
		return false
	}
}

// Dropped is the number of hits Add refused because the queue was full.
func (r *Recorder) Dropped() int64 { return r.dropped.Load() }

// Run is the writer loop. It writes every second or every 256 records, and
// when ctx ends it drains the queue for at most 2 seconds, writes, and
// returns.
func (r *Recorder) Run(ctx context.Context) {
	done := make(chan struct{})
	r.runMu.Lock()
	r.runDone = done
	r.runMu.Unlock()
	defer func() {
		r.runMu.Lock()
		r.runDone = nil
		r.runMu.Unlock()
		close(done)
	}()

	tick := time.NewTicker(flushEvery)
	defer tick.Stop()
	r.wmu.Lock()
	r.housekeeping()
	r.wmu.Unlock()
	for {
		select {
		case h := <-r.ch:
			r.wmu.Lock()
			r.take(h)
			r.wmu.Unlock()
		case <-tick.C:
			r.wmu.Lock()
			r.writeAll()
			r.housekeeping()
			r.wmu.Unlock()
		case ack := <-r.flushReq:
			r.wmu.Lock()
			r.drain(len(r.ch), time.Time{})
			r.writeAll()
			r.wmu.Unlock()
			close(ack)
		case <-ctx.Done():
			r.wmu.Lock()
			r.drain(cap(r.ch), time.Now().Add(drainTimeout))
			r.writeAll()
			r.wmu.Unlock()
			return
		}
	}
}

// Flush is a test hook: it processes every hit queued when it is called,
// writes every batch, and returns once they are on disk. While Run is
// active the writer loop does it, on a request, so hits still pass through
// the one writer in order (including one Run had already taken off the
// queue); otherwise Flush drains the queue itself under the writer's lock.
func (r *Recorder) Flush() {
	r.runMu.Lock()
	done := r.runDone
	r.runMu.Unlock()
	if done != nil {
		ack := make(chan struct{})
		select {
		case r.flushReq <- ack:
			<-ack
			return
		case <-done: // Run ended meanwhile: drain here
		}
	}
	r.wmu.Lock()
	defer r.wmu.Unlock()
	r.drain(len(r.ch), time.Time{})
	r.writeAll()
}

// drain takes at most limit queued hits, stopping early when the queue is
// empty or a non-zero deadline has passed.
func (r *Recorder) drain(limit int, deadline time.Time) {
	for ; limit > 0; limit-- {
		if !deadline.IsZero() && !time.Now().Before(deadline) {
			return
		}
		select {
		case h := <-r.ch:
			r.take(h)
		default:
			return
		}
	}
}

// take turns a hit into a record line in its (site, day) batch, and writes
// every batch once 256 records wait.
func (r *Recorder) take(h Hit) {
	date, line, ok := r.record(h)
	if !ok {
		return
	}
	k := batchKey{h.ViewID, date}
	r.batches[k] = append(r.batches[k], line)
	r.pending++
	if r.pending >= flushRecords {
		r.writeAll()
	}
}

// record is the whole way from request facts to a stored line: the page
// check, the site's local date, sanitizing, parsing, the location and the
// visitor id. The IP and the user agent are used here and go no further.
func (r *Recorder) record(h Hit) (date string, line []byte, ok bool) {
	if h.Kind != "p" && h.Kind != "e" {
		return "", nil, false
	}
	if h.PageCheck != nil && !h.PageCheck() {
		return "", nil, false
	}
	if h.Time.IsZero() {
		h.Time = r.c.Now()
	}
	date = LocalDate(h.Time, h.TZ)
	salt, err := r.c.Salts.For(date)
	if err != nil {
		r.c.Log.Warn("analytics: no salt, hit dropped", "date", date, "err", err)
		return "", nil, false
	}
	rec := Record{
		T:  h.Time.UnixMilli(),
		K:  h.Kind,
		S:  h.Source,
		A:  h.Address,
		P:  CleanPath(h.RawPath),
		R:  CleanReferrer(h.Referrer, h.Own),
		Ti: CleanTitle(h.Title),
		Tg: CleanTag(h.Tag),
		L:  CleanLanguage(h.Language),
		Sc: CleanScreen(h.Screen),
	}
	rec.Q, rec.Ck = KeepQuery(h.RawQuery, h.ExtraQuery)
	if h.Kind == "e" {
		if rec.N, rec.D, ok = CleanEvent(h.Name, h.Data); !ok {
			return "", nil, false
		}
	}
	rec.B, rec.O, rec.Dv = ParseUA(h.UA, h.CHMobile, h.CHPlatform)
	if rec.Sc != "" {
		if dv := DeviceFromScreen(rec.O, rec.Sc); dv != "" {
			rec.Dv = dv
		}
	}
	rec.C, rec.Rg, rec.Ct = r.c.Geo.Lookup(h.IP)
	rec.V = VisitorID(salt, h.ViewID, h.IP, h.UA)
	// rec.Vr stays "": no variants in this version.
	line, err = json.Marshal(rec)
	if err != nil {
		r.c.Log.Warn("analytics: record not encodable, hit dropped", "site", h.ViewID, "err", err)
		return "", nil, false
	}
	return date, line, true
}

// writeAll writes every batch and empties them.
func (r *Recorder) writeAll() {
	for k, lines := range r.batches {
		found, err := r.c.WithSite(k.viewID, func(siteDir string) error {
			return r.appendDay(k.viewID, siteDir, k.date, lines)
		})
		if err != nil {
			r.c.Log.Warn("analytics: writing records", "site", k.viewID, "date", k.date, "records", len(lines), "err", err)
		}
		if !found { // the site is gone: drop its batch, create nothing
			delete(r.days, k.viewID)
		}
	}
	clear(r.batches)
	r.pending = 0
}

// appendDay runs under the site's stats lock, so the site folder exists
// and no delete can run. It appends the lines the daily cap still has room
// for and counts the rest in the day's .over file.
func (r *Recorder) appendDay(viewID, siteDir, date string, lines [][]byte) error {
	if err := os.MkdirAll(eventsDir(siteDir), 0o755); err != nil {
		return err
	}
	path := DayFile(siteDir, date)
	var size int64
	if info, err := os.Stat(path); err == nil {
		size = info.Size()
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	dc, ok := r.days[viewID]
	if !ok || dc.date != date || dc.size != size {
		n, cut, err := countLines(path)
		if err != nil {
			delete(r.days, viewID)
			return err
		}
		dc = dayCount{date: date, lines: n, size: size, cut: cut}
	}
	keep := min(max(r.c.DailyCap-dc.lines, 0), len(lines))
	if keep > 0 {
		var buf bytes.Buffer
		if dc.cut { // never glue a record onto a line a crash cut short
			buf.WriteByte('\n')
		}
		for _, l := range lines[:keep] {
			buf.Write(l)
			buf.WriteByte('\n')
		}
		f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
		if err != nil {
			delete(r.days, viewID)
			return err
		}
		_, werr := f.Write(buf.Bytes())
		cerr := f.Close()
		if err := errors.Join(werr, cerr); err != nil {
			delete(r.days, viewID) // count the file again next time
			return err
		}
		dc.lines += keep
		dc.size += int64(buf.Len())
		dc.cut = false
	}
	r.days[viewID] = dc
	if over := len(lines) - keep; over > 0 {
		if err := addOver(siteDir, date, over); err != nil {
			return fmt.Errorf("counting %d records over the daily cap: %w", over, err)
		}
	}
	return nil
}

// housekeeping runs on the writer's tick: it deletes the salts of days that
// are over everywhere (once per UTC day; the sweep does it too) and logs
// drops at most once a minute.
func (r *Recorder) housekeeping() {
	now := r.c.Now()
	if day := now.UTC().Format("2006-01-02"); day != r.prunedDay {
		r.prunedDay = day
		if err := r.c.Salts.Prune(now); err != nil {
			r.c.Log.Warn("analytics: pruning salts", "err", err)
		}
	}
	if d := r.dropped.Load(); d != r.loggedDrops && time.Since(r.lastDropLog) >= dropLogEvery {
		r.c.Log.Warn("analytics: queue full, hits dropped", "dropped", d-r.loggedDrops, "total", d)
		r.loggedDrops, r.lastDropLog = d, time.Now()
	}
}

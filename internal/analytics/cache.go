package analytics

import (
	"cmp"
	"container/list"
	"errors"
	"io/fs"
	"os"
	"reflect"
	"slices"
	"sync"
	"time"
)

// Engine answers analytics queries from a site's day files. It keeps parsed
// days in an LRU bounded by an estimate of their size in memory. A day is
// keyed by its file's path, size and mtime, so an appended record (today's
// file grows every second) or a rewritten file is never answered from an
// older parse. Closed days also have a summary on disk, keyed the same way
// (summary.go). It is safe for concurrent use.
type Engine struct {
	max int64
	now func() time.Time // says which days are closed

	mu      sync.Mutex
	used    int64
	lru     list.List                // of *cacheEntry, most recent first
	items   map[string]*list.Element // by path
	loading map[string]*dayLoad      // parses in flight, by path
}

// NewEngine returns an engine whose parsed-day cache holds at most
// maxBytes (estimated). A day bigger than that is parsed for each query.
func NewEngine(maxBytes int64) *Engine {
	return &Engine{max: maxBytes, now: time.Now, items: map[string]*list.Element{}, loading: map[string]*dayLoad{}}
}

// Invalidate drops a day's parse and deletes its summary, for a writer that
// rewrites a day file (an import): a rewrite could keep the size and land in
// the same mtime tick, and both are keyed by them. Deleting is best effort;
// a summary left behind is still rebuilt once the file's size or mtime
// differs.
func (e *Engine) Invalidate(siteDir, date string) {
	e.drop(DayFile(siteDir, date))
	os.Remove(summaryFile(siteDir, date))
}

// parsedDay is a day file ready to query: its records sorted by time, and
// for each record its visitor and its visit, both numbered within the day.
// Every query reading the day shares it; nothing changes it once built.
type parsedDay struct {
	recs      []Record
	visitor   []int32
	visit     []int32
	nVisitors int
	nVisits   int
	size      int64 // estimated bytes in memory
}

// recordBytes is what a parsed record costs beyond its share of the file's
// bytes (which stand for its strings and maps): the struct and its two
// index entries.
var recordBytes = int64(reflect.TypeFor[Record]().Size()) + 8

type fileKey struct{ size, mtime int64 }

type cacheEntry struct {
	path string
	key  fileKey
	day  *parsedDay
}

// dayLoad is a parse in flight: queries asking for the same version of the
// same day wait for it rather than parsing the file again.
type dayLoad struct {
	key  fileKey
	done chan struct{}
	day  *parsedDay
	err  error
}

// day returns the parsed day file of date, nil when there is none, and the
// key it was read under.
//
// The key is taken before the file is read, so a parse is never older than
// its key: a file that changes in between is parsed again by the next query.
func (e *Engine) day(siteDir, date string) (*parsedDay, fileKey, error) {
	path := DayFile(siteDir, date)
	fi, err := os.Stat(path)
	if errors.Is(err, fs.ErrNotExist) {
		e.drop(path)
		return nil, fileKey{}, nil
	}
	if err != nil {
		return nil, fileKey{}, err
	}
	key := fileKey{fi.Size(), fi.ModTime().UnixNano()}

	e.mu.Lock()
	if el, ok := e.items[path]; ok {
		if ent := el.Value.(*cacheEntry); ent.key == key {
			e.lru.MoveToFront(el)
			e.mu.Unlock()
			return ent.day, key, nil
		}
		e.removeLocked(el)
	}
	if l, ok := e.loading[path]; ok && l.key == key {
		e.mu.Unlock()
		<-l.done
		return l.day, key, l.err
	}
	l := &dayLoad{key: key, done: make(chan struct{})}
	e.loading[path] = l
	e.mu.Unlock()

	l.day, l.err = parseDay(path, key.size)
	e.mu.Lock()
	if e.loading[path] == l { // not invalidated or overtaken meanwhile
		delete(e.loading, path)
		if l.err == nil && l.day != nil {
			e.putLocked(path, key, l.day)
		}
	}
	e.mu.Unlock()
	close(l.done)
	return l.day, key, l.err
}

func (e *Engine) putLocked(path string, key fileKey, d *parsedDay) {
	if el, ok := e.items[path]; ok {
		e.removeLocked(el)
	}
	if d.size > e.max {
		return
	}
	e.items[path] = e.lru.PushFront(&cacheEntry{path: path, key: key, day: d})
	e.used += d.size
	for e.used > e.max {
		e.removeLocked(e.lru.Back())
	}
}

func (e *Engine) removeLocked(el *list.Element) {
	ent := e.lru.Remove(el).(*cacheEntry)
	delete(e.items, ent.path)
	e.used -= ent.day.size
}

// drop forgets a day: its parse, and any parse in flight, which then is
// not cached.
func (e *Engine) drop(path string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if el, ok := e.items[path]; ok {
		e.removeLocked(el)
	}
	delete(e.loading, path)
}

// parseDay reads and indexes a day file of fileSize bytes; a file gone
// meanwhile is nil.
func parseDay(path string, fileSize int64) (*parsedDay, error) {
	f, err := os.Open(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	recs, err := readRecords(f)
	if err != nil {
		return nil, err
	}
	d := indexDay(recs)
	d.size = fileSize + int64(len(recs))*recordBytes
	return d, nil
}

// indexDay sorts a day's records by time and numbers its visitors and
// visits: a visitor's records in a row with no gap over visitGap are one
// visit, and an imported record joins the other records of its visitor
// carrying the same Umami visit id.
func indexDay(recs []Record) *parsedDay {
	byT := func(a, b Record) int { return cmp.Compare(a.T, b.T) }
	if !slices.IsSortedFunc(recs, byT) {
		slices.SortStableFunc(recs, byT)
	}
	d := &parsedDay{recs: recs, visitor: make([]int32, len(recs)), visit: make([]int32, len(recs))}
	type umamiVisit struct{ v, vi string }
	visitors := map[string]int32{}
	imported := map[umamiVisit]int32{}
	var lastT []int64 // per visitor: the time of their last record outside an imported visit
	var open []int32  // per visitor: their current visit, -1 before the first
	gap := visitGap.Milliseconds()
	for i := range recs {
		r := &recs[i]
		w, ok := visitors[r.V]
		if !ok {
			w = int32(len(lastT))
			visitors[r.V] = w
			lastT = append(lastT, 0)
			open = append(open, -1)
		}
		d.visitor[i] = w
		if r.Vi != "" {
			k := umamiVisit{r.V, r.Vi}
			v, ok := imported[k]
			if !ok {
				v = int32(d.nVisits)
				d.nVisits++
				imported[k] = v
			}
			d.visit[i] = v
			continue
		}
		if open[w] < 0 || r.T-lastT[w] > gap {
			open[w] = int32(d.nVisits)
			d.nVisits++
		}
		lastT[w] = r.T
		d.visit[i] = open[w]
	}
	d.nVisitors = len(lastT)
	return d
}

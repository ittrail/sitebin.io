package analytics

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// What a site's analytics holds on disk, and the retention rule that deletes
// its old days.

// staleTemp is how old a temp file in events/ must be before it counts as
// the leftover of a crashed write; a younger one may be a write in flight.
const staleTemp = time.Hour

// importedFile lists the ranges imported into a site:
// <siteDir>/analytics/imported.json.
func importedFile(siteDir string) string {
	return filepath.Join(siteDir, "analytics", "imported.json")
}

// ImportedRange is the range of local dates (YYYY-MM-DD, both inclusive) one
// import brought in.
type ImportedRange struct {
	From string `json:"from"`
	To   string `json:"to"`
}

// Imported reads the ranges imported into the site, in the order they were
// imported: an empty list when nothing was.
func Imported(siteDir string) ([]ImportedRange, error) {
	b, err := os.ReadFile(importedFile(siteDir))
	if errors.Is(err, fs.ErrNotExist) {
		return []ImportedRange{}, nil
	}
	if err != nil {
		return nil, err
	}
	var out []ImportedRange
	if err := json.Unmarshal(b, &out); err != nil {
		return nil, err
	}
	if out == nil {
		out = []ImportedRange{}
	}
	return out, nil
}

// Storage is what a site's analytics takes on disk: the bytes of the regular
// files under <siteDir>/analytics, and the earliest date that has a day file
// ("" when none has). It follows no link.
func Storage(siteDir string) (bytes int64, firstDay string, err error) {
	events := eventsDir(siteDir)
	err = filepath.WalkDir(filepath.Join(siteDir, "analytics"), func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil // no analytics yet, or a file removed meanwhile
			}
			return err
		}
		if !d.Type().IsRegular() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			return err
		}
		bytes += info.Size()
		if date, ok := strings.CutSuffix(d.Name(), ".jsonl"); ok && filepath.Dir(path) == events && saltDateRe.MatchString(date) && (firstDay == "" || date < firstDay) {
			firstDay = date
		}
		return nil
	})
	return bytes, firstDay, err
}

// RetentionStart is the first local date in loc (nil is UTC) that a
// retention of days keeps at now: today and the days before it, so a day
// goes once it is more than days old. A retention of 0 or less keeps every
// day, and the answer is "".
func RetentionStart(now time.Time, loc *time.Location, days int) string {
	if days <= 0 {
		return ""
	}
	if loc == nil {
		loc = time.UTC
	}
	l := now.In(loc)
	// Calendar days, counted on a clock without DST.
	return time.Date(l.Year(), l.Month(), l.Day()-days, 0, 0, 0, 0, time.UTC).Format("2006-01-02")
}

// PurgeDays deletes everything a site keeps for the local dates before
// start: each date's day file, summary and over-the-cap count, and any temp
// file a write of one of them left (every <date>.* in events/). It also
// deletes a temp file of a later date once it is staleTemp old at now: a
// crashed write's. It returns the number of files deleted. It never creates
// or removes the events directory, and a site without one has nothing to
// delete.
//
// The contract for every writer of events/: a temp file (a name with ".tmp"
// in it) is either written and renamed while the writer holds the site's
// stats lock, which the sweep's purge holds too, or renamed within an hour
// of its last write. A temp file older than that is taken for a crash's and
// deleted, even when its day is kept.
func PurgeDays(siteDir, start string, now time.Time) (int, error) {
	dir := eventsDir(siteDir)
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	n := 0
	var errs []error
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() {
			continue
		}
		date, _, ok := strings.Cut(name, ".")
		if !ok || !saltDateRe.MatchString(date) {
			continue
		}
		if date >= start {
			if !strings.Contains(name, ".tmp") {
				continue
			}
			if info, err := e.Info(); err != nil || now.Sub(info.ModTime()) < staleTemp {
				continue
			}
		}
		if err := os.Remove(filepath.Join(dir, name)); err != nil {
			if !errors.Is(err, fs.ErrNotExist) {
				errs = append(errs, err)
			}
			continue
		}
		n++
	}
	return n, errors.Join(errs...)
}

package analytics

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// eventsDir is where a site's day files live: <siteDir>/analytics/events.
// The directory name is the format version.
func eventsDir(siteDir string) string {
	return filepath.Join(siteDir, "analytics", "events")
}

// DayFile is the record file of one local date:
// <siteDir>/analytics/events/<date>.jsonl.
func DayFile(siteDir, date string) string {
	return filepath.Join(eventsDir(siteDir), date+".jsonl")
}

// overFile holds the number of records over the daily cap that were not
// stored: <siteDir>/analytics/events/<date>.over.
func overFile(siteDir, date string) string {
	return filepath.Join(eventsDir(siteDir), date+".over")
}

// LocalDate is the date (YYYY-MM-DD) of t in loc; a nil loc is UTC.
func LocalDate(t time.Time, loc *time.Location) string {
	if loc == nil {
		loc = time.UTC
	}
	return t.In(loc).Format("2006-01-02")
}

// ReadDay parses a day file. A missing file is nil, nil; a line that is not
// a JSON record (a cut last line, say) is skipped.
func ReadDay(siteDir, date string) ([]Record, error) {
	if !saltDateRe.MatchString(date) {
		return nil, fmt.Errorf("analytics: bad date %q", date)
	}
	f, err := os.Open(DayFile(siteDir, date))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return readRecords(f)
}

// readRecords parses day file lines, skipping a line that is not a JSON
// record. On a read error it returns what it parsed so far with the error.
func readRecords(r io.Reader) ([]Record, error) {
	var out []Record
	br := bufio.NewReader(r)
	for {
		line, err := br.ReadBytes('\n')
		if line = bytes.TrimSpace(line); len(line) > 0 {
			var rec Record
			if json.Unmarshal(line, &rec) == nil {
				out = append(out, rec)
			}
		}
		if err == io.EOF {
			return out, nil
		}
		if err != nil {
			return out, err
		}
	}
}

// countDay counts the lines of a day file per capped class, a cut last line
// included, and reports whether the file ends inside a line (so the next
// append must start a new one). A missing file has no lines.
//
// The class is the record's "s": the script's ("js") and the server's
// ("srv") lines are counted apart, an import's ("um") in neither. A line
// whose source cannot be read (one a crash cut short) counts as the
// server's, so no line escapes the bound.
func countDay(path string) (srv, js int, cut bool, err error) {
	f, err := os.Open(path)
	if errors.Is(err, fs.ErrNotExist) {
		return 0, 0, false, nil
	}
	if err != nil {
		return 0, 0, false, err
	}
	defer f.Close()
	br := bufio.NewReaderSize(f, 64<<10)
	for {
		line, err := br.ReadBytes('\n')
		if len(line) > 0 {
			cut = line[len(line)-1] != '\n'
			var rec struct {
				S string `json:"s"`
			}
			if l := bytes.TrimSpace(line); len(l) > 0 {
				_ = json.Unmarshal(l, &rec) // a line that does not parse has no source
				switch rec.S {
				case "js":
					js++
				case "um":
				default:
					srv++
				}
			}
		}
		if err == io.EOF {
			return srv, js, cut, nil
		}
		if err != nil {
			return 0, 0, false, err
		}
	}
}

// addOver adds n to a day's over-the-cap count, written through a temp file
// and a rename so a reader never sees half a number. An unreadable count
// starts again from zero.
func addOver(siteDir, date string, n int) error {
	path := overFile(siteDir, date)
	prev := 0
	if b, err := os.ReadFile(path); err == nil {
		prev, _ = strconv.Atoi(strings.TrimSpace(string(b)))
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), date+".over.tmp*")
	if err != nil {
		return err
	}
	_, werr := tmp.WriteString(strconv.Itoa(max(prev, 0) + n))
	cerr := tmp.Close()
	if err := errors.Join(werr, cerr, os.Chmod(tmp.Name(), 0o644)); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	return nil
}

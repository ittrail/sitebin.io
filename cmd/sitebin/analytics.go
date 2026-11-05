package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// `sitebin analytics import-umami` is a thin client: the running server does
// the import, because the recorder appends under a lock only that process
// holds. The CLI streams the files to POST /internal/analytics/import on the
// internal listener and prints the server's report. See
// docs/analytics-umami-import.md.

const importUmamiUsage = "usage: sitebin analytics import-umami <view-id> <events.jsonl> [--reports <reports.json>] [--until <RFC3339>]"

type umamiImportArgs struct {
	site, events, reports, until string
}

// parseImportUmami reads the arguments after `analytics`.
func parseImportUmami(args []string) (umamiImportArgs, error) {
	var out umamiImportArgs
	if len(args) == 0 || args[0] != "import-umami" {
		return out, errors.New("the analytics command has one subcommand, import-umami")
	}
	var pos []string
	for i := 1; i < len(args); i++ {
		switch a := args[i]; a {
		case "--reports", "--until":
			if i+1 == len(args) {
				return out, fmt.Errorf("%s needs a value", a)
			}
			i++
			if a == "--reports" {
				out.reports = args[i]
			} else {
				out.until = args[i]
			}
		default:
			if strings.HasPrefix(a, "--") {
				return out, fmt.Errorf("unknown option %s", a)
			}
			pos = append(pos, a)
		}
	}
	if len(pos) != 2 {
		return out, errors.New("give the site's view id and the events file")
	}
	out.site, out.events = pos[0], pos[1]
	if out.until != "" {
		if _, err := time.Parse(time.RFC3339, out.until); err != nil {
			return out, fmt.Errorf("--until must be an RFC 3339 time, such as 2026-11-05T00:00:00+01:00")
		}
	}
	return out, nil
}

// importUmami sends the import to the server at base (http://host:port of
// the internal listener) and prints its answer to out. It fails when the
// server cannot be reached or does not answer 200.
func importUmami(base string, args umamiImportArgs, out io.Writer) error {
	events, err := os.Open(args.events)
	if err != nil {
		return err
	}
	defer events.Close()
	var reports *os.File
	if args.reports != "" {
		if reports, err = os.Open(args.reports); err != nil {
			return err
		}
		defer reports.Close()
	}

	// The body streams: site and until first, so the server knows the site
	// before the events arrive, and the events last.
	pr, pw := io.Pipe()
	mw := multipart.NewWriter(pw)
	go func() {
		pw.CloseWithError(func() error {
			if err := mw.WriteField("site", args.site); err != nil {
				return err
			}
			if args.until != "" {
				if err := mw.WriteField("until", args.until); err != nil {
					return err
				}
			}
			for _, f := range []struct {
				name string
				file *os.File
			}{{"reports", reports}, {"events", events}} {
				if f.file == nil {
					continue
				}
				w, err := mw.CreateFormFile(f.name, filepath.Base(f.file.Name()))
				if err != nil {
					return err
				}
				if _, err := io.Copy(w, f.file); err != nil {
					return err
				}
			}
			return mw.Close()
		}())
	}()
	req, err := http.NewRequest("POST", base+"/internal/analytics/import", pr)
	if err != nil {
		pr.Close()
		return err
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("could not reach the running Sitebin server at %s; the import runs inside it, so it must be running: %w", base, err)
	}
	defer res.Body.Close()
	body, err := io.ReadAll(res.Body)
	if err != nil {
		return err
	}
	var pretty bytes.Buffer
	if json.Indent(&pretty, body, "", "  ") == nil {
		body = pretty.Bytes()
	}
	fmt.Fprintln(out, strings.TrimSpace(string(body)))
	if res.StatusCode != 200 {
		return fmt.Errorf("the server refused the import (%d)", res.StatusCode)
	}
	return nil
}

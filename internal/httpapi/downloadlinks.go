package httpapi

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/ittrail/sitebin.io/internal/ids"
	"github.com/ittrail/sitebin.io/internal/store"
)

// Download links hand a download to the browser itself. Sitebin's pages
// authenticate with headers (X-Edit-Password, X-Sitebin-Session), which a
// plain link cannot carry, so they used to fetch a ZIP into memory and save
// it only when the last byte had arrived. Instead the page asks for a link
// (POST …/download-link, behind withEditAuth) and opens it: the browser's
// download manager shows the transfer at once and writes it to disk as the
// server packs it. Design:
// docs/superpowers/specs/2026-11-19-streamed-downloads-design.md.
//
// A link is memory only, single use, valid for a minute, and opens exactly
// the request it was minted for. On use it re-checks what minted it
// (openerStands) and the lock, then runs the route as the minting actor.
const (
	downloadLinkTTL      = time.Minute
	downloadLinksPerSite = 5    // opening another evicts the site's oldest
	downloadLinksMax     = 2000 // the instance-wide backstop
	ticketParam          = "ticket"
)

const msgDownloadLinkRefused = "this download link is unknown, used or expired — start the download again"

var errTooManyDownloads = &apiError{http.StatusServiceUnavailable, "too many downloads are starting on this instance, try again in a minute"}

// downloadLinkRoute reports whether p, a clean path below
// /api/sites/{editID}, is a download a link may open: the site's files, a
// backup version, a Git archive.
func downloadLinkRoute(p string) bool {
	switch p {
	case "/download", "/git/archive":
		return true
	}
	if rest, ok := strings.CutPrefix(p, "/backups/"); ok {
		snap, ok := strings.CutSuffix(rest, "/download")
		return ok && snap != "" && !strings.Contains(snap, "/")
	}
	return false
}

// canonicalDownload is the request a link opens: the decoded path and the
// query without the ticket, its keys sorted (url.Values.Encode).
func canonicalDownload(p string, q url.Values) string {
	rest := url.Values{}
	for k, v := range q {
		if k != ticketParam {
			rest[k] = v
		}
	}
	if enc := rest.Encode(); enc != "" {
		return p + "?" + enc
	}
	return p
}

type downloadLink struct {
	viewID  string
	editID  string
	request string // canonicalDownload of the one request it opens
	act     actor  // who minted it; the route runs as this actor
	// pwHash is the edit-password hash a link minted with the password was
	// authenticated against (openerStands).
	pwHash string
	issued time.Time
}

// downloadLinks is the registry, keyed by sha256(secret); it never holds a
// secret.
type downloadLinks struct {
	mu      sync.Mutex
	now     func() time.Time
	perSite int
	max     int
	m       map[string]*downloadLink
}

func newDownloadLinks(now func() time.Time) *downloadLinks {
	return &downloadLinks{now: now, perSite: downloadLinksPerSite, max: downloadLinksMax, m: map[string]*downloadLink{}}
}

func hashTicket(secret string) string {
	sum := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(sum[:])
}

// issue mints a link to request on the site, and answers its secret — the
// only moment it exists outside the caller — and when it expires.
func (d *downloadLinks) issue(viewID, editID, request string, act actor, pwHash string) (string, time.Time, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	now := d.now()
	count, oldestKey := 0, ""
	var oldest time.Time
	for k, l := range d.m {
		if !now.Before(l.issued.Add(downloadLinkTTL)) {
			delete(d.m, k)
			continue
		}
		if l.viewID != viewID {
			continue
		}
		count++
		if oldestKey == "" || l.issued.Before(oldest) {
			oldestKey, oldest = k, l.issued
		}
	}
	if count >= d.perSite {
		delete(d.m, oldestKey)
	}
	if len(d.m) >= d.max {
		return "", time.Time{}, errTooManyDownloads
	}
	secret := ids.NewDownloadTicket()
	d.m[hashTicket(secret)] = &downloadLink{viewID: viewID, editID: editID, request: request, act: act, pwHash: pwHash, issued: now}
	return secret, now.Add(downloadLinkTTL), nil
}

// take spends the link secret: it is gone after this call whatever the
// answer, so a misused link is dead too. ok only for a live link of the site
// editID minted for exactly request.
func (d *downloadLinks) take(secret, editID, request string) (*downloadLink, bool) {
	if !strings.HasPrefix(secret, ids.DownloadTicketPrefix) {
		return nil, false
	}
	key := hashTicket(secret)
	d.mu.Lock()
	defer d.mu.Unlock()
	l, ok := d.m[key]
	if !ok {
		return nil, false
	}
	delete(d.m, key)
	if !d.now().Before(l.issued.Add(downloadLinkTTL)) || l.editID != editID || l.request != request {
		return nil, false
	}
	return l, true
}

// createDownloadLink answers POST /api/sites/{editID}/download-link
// {"path": "/download"}: a link to one of the site's downloads, opened as the
// caller withEditAuth authenticated.
func (a *API) createDownloadLink(w http.ResponseWriter, r *http.Request, site *store.Site) {
	var in struct {
		Path string `json:"path"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 4<<10)).Decode(&in); err != nil {
		writeError(w, 400, "invalid JSON body")
		return
	}
	u, err := url.Parse(in.Path)
	var q url.Values
	if err == nil {
		q, err = url.ParseQuery(u.RawQuery)
	}
	if err != nil || u.Scheme != "" || u.Opaque != "" || u.User != nil || u.Host != "" ||
		u.Fragment != "" || u.RawFragment != "" || !strings.HasPrefix(u.Path, "/") ||
		path.Clean(u.Path) != u.Path || !downloadLinkRoute(u.Path) || q.Has(ticketParam) {
		writeError(w, 400, `path must be one of the site's downloads: "/download", "/backups/<snapshot>/download" or "/git/archive"`)
		return
	}
	act := a.actorOf(r)
	pwHash := ""
	if act.role == rolePassword {
		pwHash = site.Meta.EditPasswordHash // the hash this request was verified against
	}
	base := "/api/sites/" + site.EditID
	secret, expires, err := a.downloads.issue(site.ViewID, site.EditID, canonicalDownload(base+u.Path, q), act, pwHash)
	if err != nil {
		respondErr(w, err)
		return
	}
	q.Set(ticketParam, secret)
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, map[string]any{"url": base + u.EscapedPath() + "?" + q.Encode(), "expires_at": expires.UTC()})
}

// withDownloadAuth guards the download routes. A request carrying a ticket
// is answered from the link alone — live, this site's, this exact request,
// its minter still standing, the site not locked — and runs as the actor
// that minted it. Every other request goes through withEditAuth unchanged.
func (a *API) withDownloadAuth(next func(http.ResponseWriter, *http.Request, *store.Site)) http.HandlerFunc {
	editAuth := a.withEditAuth(next)
	return func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if !q.Has(ticketParam) {
			editAuth(w, r)
			return
		}
		editID := r.PathValue("editID")
		link, ok := a.downloads.take(q.Get(ticketParam), editID, canonicalDownload(r.URL.Path, q))
		if !ok {
			writeError(w, 401, msgDownloadLinkRefused)
			return
		}
		site, err := a.st.ByEditID(editID)
		if err != nil {
			storeError(w, err)
			return
		}
		if refuseLocked(w, site) {
			return
		}
		v, err := a.openerStands(site, link.act.account, link.act.role, link.pwHash)
		if err != nil {
			respondErr(w, err)
			return
		}
		if v != openerHolds {
			writeError(w, 401, msgDownloadLinkRefused)
			return
		}
		next(w, withActor(r, link.act), site)
	}
}

// downloadName is the file name (without .zip) a caller asked for:
// control characters and those a file system refuses become "_", leading and
// trailing spaces and dots go, at most 100 characters; "" when nothing is
// left. attachmentDisposition quotes it.
func downloadName(n string) string {
	n = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f || strings.ContainsRune(`<>:"/\|?*;`, r) {
			return '_'
		}
		return r
	}, n)
	n = strings.Trim(n, " .")
	if r := []rune(n); len(r) > 100 {
		n = strings.TrimRight(string(r[:100]), " .")
	}
	return n
}

// startedWriter notes whether anything was written to the response, so a
// download that fails before its first byte can still answer an error.
type startedWriter struct {
	w       io.Writer
	started bool
}

func (s *startedWriter) Write(p []byte) (int, error) {
	s.started = true
	return s.w.Write(p)
}

package httpapi

import (
	"errors"
	"os"
	"time"

	"github.com/ittrail/sitebin.io/internal/ftp"
	"github.com/ittrail/sitebin.io/internal/provenance"
	"github.com/ittrail/sitebin.io/internal/store"
)

// FTPAuth authenticates an FTP login (username = edit UUID, password = edit
// password) and returns the site's content directory, its effective quota
// caps and its write guard. It reuses the same rate limiting and
// verification cache as the HTTP edit-auth path. Implements
// ftp.Authenticator.
func (a *API) FTPAuth(editID, password, clientIP string) (ftp.Session, error) {
	if !a.cfg.FTPEnabled {
		return ftp.Session{}, errors.New("ftp is disabled on this instance")
	}
	site, err := a.st.ByEditID(editID)
	if err != nil {
		return ftp.Session{}, errors.New("unknown site")
	}
	if !site.Meta.FTPEnabled {
		return ftp.Session{}, errors.New("ftp is not enabled for this site")
	}
	switch a.verifyEditIP(clientIP, site, password) {
	case verifyOK:
		// The API is an account feature, and FTP must not be a back door
		// around that: when accounts are enabled, an anonymous site has no
		// FTP either, exactly like it has no JSON API. ee/eeconfig.Tier has
		// no FTP field, so this is the only place that rule is enforced.
		if a.gatedAnonymous(site) {
			return ftp.Session{}, errors.New("this site was created without an account, so it has no FTP access")
		}
		// Checked at login, which is the one moment FTP asks: a session
		// opened before the lock can keep writing until it disconnects (FTP
		// is off on the hosted instance; see the lock design).
		if site.Meta.IsLocked() {
			return ftp.Session{}, errors.New(store.LockedMessage(site.Meta.Locked))
		}
		return ftp.Session{
			Dir:      site.ContentDir(),
			MaxBytes: a.st.EffMaxBytes(site),
			MaxFiles: a.st.EffMaxFiles(site),
			Guard:    ftpGuard{st: a.st, site: site},
		}, nil
	case verifyThrottled:
		return ftp.Session{}, errors.New("too many authentication attempts")
	default:
		return ftp.Session{}, errors.New("incorrect edit password")
	}
}

// FTPWrote records an FTP write in the site's provenance log. Implements
// ftp.Recorder. FTP logs in with the edit password and carries no user agent.
func (a *API) FTPWrote(editID, clientIP, action, path string) {
	site, err := a.st.ByEditID(editID)
	if err != nil {
		return
	}
	e := provenance.Entry{
		Time:    time.Now().UTC(),
		Action:  action,
		Surface: provenance.SurfaceFTP,
		Auth:    provenance.AuthPassword,
		IP:      clientIP,
		Detail:  path,
	}
	if action == provenance.ActionUpload {
		e.Files = 1
	}
	a.record(site, e)
}

// ftpGuard writes an FTP session's files the way WebDAV does: staged, and
// visible only once the abuse guard has settled them. FTP holds no site lock
// of its own, so each commit takes it.
type ftpGuard struct {
	st   *store.Store
	site *store.Site
}

func (g ftpGuard) Stage(rel string, flag int, perm os.FileMode) (ftp.File, error) {
	f, err := g.st.OpenStaged(g.site, rel, flag, perm, false)
	if err != nil {
		return nil, err // never a typed nil in the interface
	}
	return f, nil
}

func (g ftpGuard) Rename(oldRel, newRel string) error {
	return g.st.RenameChecked(g.site, oldRel, newRel, false)
}

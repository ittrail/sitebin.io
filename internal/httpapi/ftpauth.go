package httpapi

import "errors"

// FTPAuth authenticates an FTP login (username = edit UUID, password = edit
// password) and returns the site's content directory plus its effective quota
// caps. It reuses the same rate limiting and verification cache as the HTTP
// edit-auth path. Implements ftp.Authenticator.
func (a *API) FTPAuth(editID, password, clientIP string) (string, int64, int, error) {
	if !a.cfg.FTPEnabled {
		return "", 0, 0, errors.New("ftp is disabled on this instance")
	}
	site, err := a.st.ByEditID(editID)
	if err != nil {
		return "", 0, 0, errors.New("unknown site")
	}
	if !site.Meta.FTPEnabled {
		return "", 0, 0, errors.New("ftp is not enabled for this site")
	}
	switch a.verifyEditIP(clientIP, site, password) {
	case verifyOK:
		return site.ContentDir(), a.st.EffMaxBytes(site), a.st.EffMaxFiles(site), nil
	case verifyThrottled:
		return "", 0, 0, errors.New("too many authentication attempts")
	default:
		return "", 0, 0, errors.New("incorrect edit password")
	}
}

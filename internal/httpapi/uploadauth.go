package httpapi

import (
	"net/http"
	"strings"

	"github.com/ittrail/sitebin.io/internal/ids"
	"github.com/ittrail/sitebin.io/internal/store"
)

const (
	msgUploadTokenRefused     = "upload token unknown or expired — call open_upload for a new one"
	msgUploadTokenOnlyUploads = "an upload token can only upload files — use the edit password or an account API token for anything else"
)

// uploadCredential returns the upload token a request presents, or "" when it
// presents none. A token is recognised by its prefix wherever a password can
// travel — Authorization: Bearer, the password of Basic auth, X-Edit-Password
// — so that it is answered only as an upload token and never tried as
// anything else. No edit password can carry the prefix (they are base62, which
// has no underscore), and account API tokens have their own.
func uploadCredential(r *http.Request) string {
	const scheme = "Bearer "
	if h := r.Header.Get("Authorization"); len(h) > len(scheme) && strings.EqualFold(h[:len(scheme)], scheme) {
		if s := strings.TrimSpace(h[len(scheme):]); strings.HasPrefix(s, ids.UploadTokenPrefix) {
			return s
		}
	}
	if _, pw, ok := r.BasicAuth(); ok && strings.HasPrefix(pw, ids.UploadTokenPrefix) {
		return pw
	}
	if pw := r.Header.Get("X-Edit-Password"); strings.HasPrefix(pw, ids.UploadTokenPrefix) {
		return pw
	}
	return ""
}

// withUploadAuth guards the one JSON API route an upload token may use,
// POST /api/sites/{editID}/files. A request presenting a token is answered
// from the token alone — this site's and live, or a 401 — and anything else
// goes through withEditAuth unchanged.
func (a *API) withUploadAuth(next func(http.ResponseWriter, *http.Request, *store.Site)) http.HandlerFunc {
	editAuth := a.withEditAuth(next)
	return func(w http.ResponseWriter, r *http.Request) {
		secret := uploadCredential(r)
		if secret == "" {
			editAuth(w, r)
			return
		}
		editID := r.PathValue("editID")
		site, err := a.st.ByEditID(editID)
		if err != nil {
			storeError(w, err)
			return
		}
		end, ok := a.uploads.begin(secret, editID)
		if !ok {
			writeError(w, 401, msgUploadTokenRefused)
			return
		}
		defer end()
		// Tokens are issued only for sites MCP could open, but the rule is
		// cheap and belongs on every entry: an account-less site on a gated
		// instance cannot be scripted.
		if a.gatedAnonymous(site) {
			writeError(w, 403, "this site was created without an account, so it has no API — create it while signed in at "+a.apiAccountHint()+" to script it")
			return
		}
		next(w, r, site)
	}
}

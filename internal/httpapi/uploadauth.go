package httpapi

import (
	"net/http"
	"strings"

	"github.com/ittrail/sitebin.io/internal/ids"
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

package httpapi

import (
	"net/http"
	"strings"

	"github.com/ittrail/sitebin.io/internal/ids"
	"github.com/ittrail/sitebin.io/internal/provenance"
	"github.com/ittrail/sitebin.io/internal/store"
)

const (
	msgUploadTokenRefused     = "upload token unknown or expired — call open_upload for a new one"
	msgUploadTokenOnlyUploads = "an upload token can only upload files — use the edit password or an account API token for anything else"
	// msgUploadTokenShareEnded: an admin's token on a site the admin no
	// longer manages (the share ended, or a plan no longer shares).
	msgUploadTokenShareEnded = "this upload token was opened by an admin of the site who no longer manages it"
)

// errUploadShareEnded is uploadTokenStands' refusal of an ex-admin's token.
var errUploadShareEnded = &apiError{http.StatusForbidden, msgUploadTokenShareEnded}

// errUploadTokenRevoked refuses a token whose owner no longer owns the site,
// or whose edit password was changed: it is as dead as a revoked one.
var errUploadTokenRevoked = &apiError{http.StatusUnauthorized, msgUploadTokenRefused}

// opener is what openerStands found of the authority that opened a
// short-lived credential.
type opener int

const (
	openerHolds      opener = iota
	openerShareEnded        // an admin's, and the admin no longer stands
	openerRevoked           // the owner no longer owns, or the password changed
)

// openerStands re-applies, on every use of a short-lived credential (an
// upload token, a download link), what opened it, so the credential never
// outlives it. An admin's holds only while the admin is (accountStanding ==
// standAdmin): a share that ended, a plan that no longer shares — or one that
// cannot be asked (errSharingUnknown, returned as the error) — refuses the
// request, and the credential works again if the share is restored. An
// owner's holds while the account owns the site (standOwner), and the edit
// password's (any other role) while the site's hash is the one it was opened
// against. A transfer and a rotate revoke the site's upload tokens, but one
// minted in the moment after that revoke was opened on the old authority:
// this is what refuses it. Nothing is changed. It reads meta.json the request
// already loaded, takes no lock and is never on the serving path.
func (a *API) openerStands(site *store.Site, account, role, pwHash string) (opener, error) {
	switch role {
	case roleAdmin:
		st, err := a.accountStanding(site, account)
		if err != nil {
			return openerRevoked, err
		}
		if st != standAdmin {
			return openerShareEnded, nil
		}
	case roleOwner:
		st, err := a.accountStanding(site, account)
		if err != nil {
			return openerRevoked, err
		}
		if st != standOwner {
			return openerRevoked, nil
		}
	default:
		if pwHash == "" || pwHash != site.Meta.EditPasswordHash {
			return openerRevoked, nil
		}
	}
	return openerHolds, nil
}

// uploadTokenStands is openerStands for the upload token secret, as the
// refusal its surfaces answer.
func (a *API) uploadTokenStands(site *store.Site, secret string) error {
	account, role, pwHash := a.uploads.openedBy(secret)
	v, err := a.openerStands(site, account, role, pwHash)
	switch {
	case err != nil:
		return err
	case v == openerShareEnded:
		return errUploadShareEnded
	case v == openerRevoked:
		return errUploadTokenRevoked
	}
	return nil
}

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
		// Locking revokes a site's tokens, but a lock the CLI wrote from
		// another process cannot reach this process's memory: the meta the
		// token's request just read is the check that always holds.
		if refuseLocked(w, site) {
			return
		}
		if err := a.uploadTokenStands(site, secret); err != nil {
			respondErr(w, err)
			return
		}
		next(w, withActor(r, a.uploadActor(provenance.SurfaceUploadToken, secret)), site)
	}
}

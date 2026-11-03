package httpapi

import "net/http"

// accountStatus answers whether the visitor holds a valid account session.
// The instance's own pages and the front door (a session origin) ask it.
// It never sets a cookie and says nothing but a bool.
func (a *API) accountStatus(w http.ResponseWriter, r *http.Request) {
	p, ok := a.sessionProvider()
	if !ok {
		a.notFoundPage(w, r)
		return
	}
	h := w.Header()
	h.Set("Content-Type", "application/json")
	h.Set("Cache-Control", "no-store")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Add("Vary", "Origin")
	if origin := r.Header.Get("Origin"); a.isSessionOrigin(origin) {
		h.Set("Access-Control-Allow-Origin", origin)
		h.Set("Access-Control-Allow-Credentials", "true")
	}
	body := `{"signed_in":false}`
	if _, signedIn := p.SessionAccount(r); signedIn {
		body = `{"signed_in":true}`
	}
	_, _ = w.Write([]byte(body))
}

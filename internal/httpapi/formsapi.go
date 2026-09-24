package httpapi

import (
	"encoding/json"
	"net/http"

	"github.com/ittrail/sitebin.io/internal/store"
)

func decodeFormInput(w http.ResponseWriter, r *http.Request, in *formInput) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	if err := json.NewDecoder(r.Body).Decode(in); err != nil {
		writeError(w, 400, `body must be a JSON object such as {"name": "Contact", "recipient": "office@example.com"}`)
		return false
	}
	return true
}

func (a *API) listForms(w http.ResponseWriter, r *http.Request, site *store.Site) {
	writeJSON(w, 200, a.listFormsFor(site))
}

func (a *API) createForm(w http.ResponseWriter, r *http.Request, site *store.Site) {
	var in formInput
	if !decodeFormInput(w, r, &in) {
		return
	}
	out, err := a.addForm(r.Context(), site, in, clientIP(r))
	if err != nil {
		respondErr(w, err)
		return
	}
	writeJSON(w, 201, out)
}

func (a *API) patchForm(w http.ResponseWriter, r *http.Request, site *store.Site) {
	var in formInput
	if !decodeFormInput(w, r, &in) {
		return
	}
	out, err := a.updateForm(r.Context(), site, r.PathValue("key"), in, clientIP(r))
	if err != nil {
		respondErr(w, err)
		return
	}
	writeJSON(w, 200, out)
}

func (a *API) removeForm(w http.ResponseWriter, r *http.Request, site *store.Site) {
	if _, err := a.deleteForm(site, r.PathValue("key")); err != nil {
		respondErr(w, err)
		return
	}
	w.WriteHeader(204)
}

func (a *API) resendFormConfirmation(w http.ResponseWriter, r *http.Request, site *store.Site) {
	out, err := a.resendConfirmation(r.Context(), site, r.PathValue("key"), clientIP(r))
	if err != nil {
		respondErr(w, err)
		return
	}
	writeJSON(w, 202, out)
}

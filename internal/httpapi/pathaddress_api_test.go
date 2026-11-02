package httpapi

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/ittrail/sitebin.io/internal/ext"
)

// A custom domain with a path over the JSON API and MCP: the domain string
// widens, nothing is renamed, and the payload says what the owner needs to
// know about it.

func TestAddPathAddressAPI(t *testing.T) {
	e, c, edit := redirectEnv(t, nil, "m.at/demos/k1")
	// A site built for its path does not work on its generated address,
	// which can redirect there instead.
	w := e.public(t, jsonReq("PUT", "/api/sites/"+edit+"/view-address", `{"mode":"redirect","to":"m.at/demos/k1"}`, c.EditPassword))
	if w.Code != 200 {
		t.Fatalf("view address -> path address = %d %s", w.Code, w.Body)
	}
	if aw := e.internal(t, authzReq(c.ID+".sitebin.example", "/a.css?v=1", "")); aw.Code != 301 || aw.Header().Get("Location") != "http://m.at/demos/k1/a.css?v=1" {
		t.Fatalf("generated address: %d %q", aw.Code, aw.Header().Get("Location"))
	}
	w = e.public(t, jsonReq("GET", "/api/sites/"+edit, "", c.EditPassword))
	var p struct {
		CustomDomains []string `json:"custom_domains"`
		PrimaryURL    string   `json:"primary_url"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &p); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(p.CustomDomains, "m.at/demos/k1") || p.PrimaryURL != "http://m.at/demos/k1" {
		t.Fatalf("payload = %+v", p)
	}
	// Uppercase in the path is refused, not lowercased.
	if w := e.public(t, jsonReq("POST", "/api/sites/"+edit+"/domains", `{"domain":"m.at/Demos"}`, c.EditPassword)); w.Code != 400 {
		t.Fatalf("an uppercase path = %d, want 400: %s", w.Code, w.Body)
	}
}

func TestRemovePathAddressEncoded(t *testing.T) {
	e, c, edit := redirectEnv(t, nil, "m.at/old", "m.at/new", "m.at/demos/k1")
	w := e.public(t, jsonReq("PUT", "/api/sites/"+edit+"/domains/m.at%2Fold/redirect", `{"to":"m.at/new"}`, c.EditPassword))
	if w.Code != 200 {
		t.Fatalf("redirect a path address = %d %s", w.Code, w.Body)
	}
	if aw := e.internal(t, authzReq("m.at", "/old/x", "")); aw.Code != 301 || aw.Header().Get("Location") != "http://m.at/new/x" {
		t.Fatalf("authz after the redirect: %d %q", aw.Code, aw.Header().Get("Location"))
	}
	w = e.public(t, jsonReq("DELETE", "/api/sites/"+edit+"/domains/m.at%2Fdemos%2Fk1", "", c.EditPassword))
	if w.Code != 200 || strings.Contains(w.Body.String(), `"m.at/demos/k1"`) {
		t.Fatalf("remove a path address = %d %s", w.Code, w.Body)
	}
	if e.st.HostKnown("m.at") != true {
		t.Fatal("the other addresses on the host must stay")
	}
}

func TestPayloadWarningsProofsDependents(t *testing.T) {
	e := mountEnv(t)
	a := e.ownedSite(t, "u1", map[string]string{"index.html": "root"}, "m.at")
	b := e.ownedSite(t, "u1", map[string]string{"index.html": `<link href="/_astro/a.css">`}, "m.at/x")

	pb := e.api.sitePayload(b)
	if w, _ := pb["domain_warnings"].(map[string]string); !strings.Contains(w["m.at/x"], "/_astro/a.css") {
		t.Errorf("domain_warnings = %#v", pb["domain_warnings"])
	}
	proofs, _ := pb["domain_proofs"].(map[string]domainProof)
	if pr := proofs["m.at/x"]; !pr.ViaHost || pr.TXTName != "_sitebin-challenge.m.at" || !strings.HasPrefix(pr.TXTValue, "sitebin-verify=") {
		t.Errorf("domain_proofs = %#v", pb["domain_proofs"])
	}
	pa := e.api.sitePayload(a)
	if deps, _ := pa["domain_dependents"].(map[string][]string); !slices.Equal(deps["m.at"], []string{"m.at/x"}) {
		t.Errorf("domain_dependents = %#v", pa["domain_dependents"])
	}
	for _, k := range []string{"domain_warnings", "domain_proofs"} {
		if _, ok := pa[k]; ok {
			t.Errorf("%s present on a site that has nothing to say there", k)
		}
	}
	if _, ok := pb["domain_dependents"]; ok {
		t.Error("domain_dependents present on a site nothing depends on")
	}
}

func TestMCPAddDomainWarnsBasePath(t *testing.T) {
	e, c, edit := redirectEnv(t, nil)
	site, _ := e.st.ByViewID(c.ID)
	if err := e.st.SaveFile(site, "index.html", strings.NewReader(`<script src="/_astro/a.js"></script>`)); err != nil {
		t.Fatal(err)
	}
	cs := mcpClient(t, e, nil)
	res := mcpCall(t, cs, "add_domain", map[string]any{"edit_id": edit, "edit_password": c.EditPassword, "domain": "m.at/demos/k1"})
	if res.IsError || !strings.Contains(mcpText(res), "not to be built for /demos/k1") {
		t.Fatalf("add_domain: %v %s", res.IsError, mcpText(res))
	}
}

func TestAddPathAddressCommunityRefused(t *testing.T) {
	ext.Reset()
	e := newEnv(t, nil)
	c := e.createSite(t, nil, map[string]string{"index.html": "x"})
	req := jsonReq("POST", "/api/sites/"+editIDFrom(t, c.EditURL)+"/domains", `{"domain":"m.at/a"}`, c.EditPassword)
	if w := e.public(t, req); w.Code != 403 {
		t.Fatalf("community build = %d, want 403: %s", w.Code, w.Body)
	}
}

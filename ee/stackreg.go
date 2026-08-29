//go:build ee

package ee

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/ittrail/sitebin.io/ee/eeconfig"
	"github.com/ittrail/sitebin.io/internal/mcp"
)

// Self-registration against the IT-Trail SaaS Stack.
//
// The point is that deploying Sitebin is enough: it announces itself to the
// stack on every start, and auth, billing and MCP are configured from what it
// declares. Nobody opens the stack's admin portal to wire up a new instance.
//
// Two rules shape what is in the declaration.
//
// **Sitebin declares only what it alone knows**: its identity, the OIDC
// redirect URI it will actually use, its tier catalogue, and its MCP resource
// and scopes. Identity providers, password policy, MFA and realm registration
// are realm-wide settings shared with every other app on the stack — an app
// that declared them would overwrite an operator's choice on each restart, and
// every other app's with it.
//
// **It declares a catalogue, never a price.** Sitebin's tiers carry provider
// price identifiers an operator wrote into tiers.json, but no amounts, so the
// declaration names the plans and leaves the money to the payment provider. A
// deploy must not be able to change what a customer is charged.

// stackRegistration is the subset of the stack's onboarding contract Sitebin
// fills in. Fields the stack owns are absent by design, not by omission.
type stackRegistration struct {
	AppID       string `json:"app_id"`
	DisplayName string `json:"display_name"`
	Domain      string `json:"domain,omitempty"`
	Auth        struct {
		RedirectURIs []string `json:"redirectUris"`
		WebOrigins   []string `json:"webOrigins,omitempty"`
	} `json:"auth"`
	Billing *stackBilling `json:"billing,omitempty"`
	MCP     *stackMCP     `json:"mcp,omitempty"`
}

type stackBilling struct {
	TierAfterRegistration string      `json:"tierAfterRegistration,omitempty"`
	Tiers                 []stackTier `json:"tiers"`
}

type stackTier struct {
	Key         string              `json:"key"`
	DisplayName map[string]string   `json:"displayName"`
	Features    []map[string]string `json:"features,omitempty"`
}

type stackMCP struct {
	ResourceURL string     `json:"resourceUrl"`
	Scopes      []mcpScope `json:"scopes"`
	DCR         *stackDCR  `json:"dcr,omitempty"`
}

type mcpScope struct {
	Name        string            `json:"name"`
	DisplayName map[string]string `json:"displayName,omitempty"`
}

type stackDCR struct {
	Enabled         bool `json:"enabled"`
	MaxClients      int  `json:"maxClients,omitempty"`
	ConsentRequired bool `json:"consentRequired"`
}

// registerWithStack announces this instance to the stack.
//
// It never blocks startup and never fails it. A stack that is briefly
// unreachable must not stop Sitebin from serving sites: the registration is
// convergent, so the next restart makes the same declaration true again.
func (p *provider) registerWithStack() {
	reg := p.cfg.StackRegistration
	if reg == nil {
		return
	}
	body := p.stackDeclaration(reg.AppID)

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := postRegistration(ctx, reg, body); err != nil {
			slog.Error("stack self-registration failed; sitebin is serving anyway",
				"url", reg.URL, "app", reg.AppID, "err", err)
			return
		}
		slog.Info("registered with the saas stack", "app", reg.AppID, "url", reg.URL,
			"tiers", len(body.Billing.Tiers), "mcp", body.MCP != nil)
	}()
}

// stackDeclaration builds what this instance is, from what it is actually
// configured with — so the declaration cannot drift from the running config.
func (p *provider) stackDeclaration(appID string) stackRegistration {
	base := p.host.BaseURL()

	var reg stackRegistration
	reg.AppID = appID
	reg.DisplayName = "Sitebin"
	reg.Domain = p.host.BaseDomain()
	// The generic-OIDC callback, built the same way handleOAuthStart builds it,
	// so a declared URI and a used URI cannot disagree.
	reg.Auth.RedirectURIs = []string{base + "/account/auth/oidc/callback"}
	reg.Auth.WebOrigins = []string{base}

	tiers := make([]stackTier, 0, len(p.cfg.Tiers))
	for _, t := range p.cfg.Tiers {
		tiers = append(tiers, stackTier{
			Key:         t.ID,
			DisplayName: map[string]string{"en": tierLabel(t)},
			Features:    tierFeatures(t),
		})
	}
	reg.Billing = &stackBilling{
		TierAfterRegistration: p.cfg.DefaultTier,
		Tiers:                 tiers,
	}

	// MCP is declared only where it is actually served, and the resource comes
	// from the same place the resource server validates against.
	if resource := p.host.MCPResource(); resource != "" && p.host.MCPOAuthIssuer() != "" {
		scopes := make([]mcpScope, 0, len(mcp.AllScopes))
		for _, s := range mcp.AllScopes {
			scopes = append(scopes, mcpScope{Name: s, DisplayName: map[string]string{"en": scopeLabel(s)}})
		}
		reg.MCP = &stackMCP{
			ResourceURL: resource,
			Scopes:      scopes,
			DCR:         &stackDCR{Enabled: true, MaxClients: 500, ConsentRequired: true},
		}
	}
	return reg
}

func tierLabel(t eeconfig.Tier) string {
	if strings.TrimSpace(t.Label) != "" {
		return t.Label
	}
	return t.ID
}

// tierFeatures turns the quota bundle into the human lines a pricing page or a
// consent screen can show. Zero means "inherit the instance global", which is
// not a promise worth printing, so those are skipped.
func tierFeatures(t eeconfig.Tier) []map[string]string {
	var out []map[string]string
	add := func(s string) { out = append(out, map[string]string{"en": s}) }

	if t.MaxSites > 0 {
		add(fmt.Sprintf("Up to %d sites", t.MaxSites))
	}
	if t.MaxSiteBytes > 0 {
		add(fmt.Sprintf("%d MB per site", t.MaxSiteBytes/(1<<20)))
	}
	if t.MaxFiles > 0 {
		add(fmt.Sprintf("%d files per site", t.MaxFiles))
	}
	if t.MaxExpiryDays > 0 {
		add(fmt.Sprintf("Sites live %d days", t.MaxExpiryDays))
	}
	if t.CustomDomains > 0 {
		add(fmt.Sprintf("%d custom domains", t.CustomDomains))
	}
	if t.WebDAV {
		add("WebDAV")
	}
	return out
}

func scopeLabel(scope string) string {
	switch scope {
	case mcp.ScopeRead:
		return "View your sites"
	case mcp.ScopeWrite:
		return "Create and change sites"
	}
	return scope
}

func postRegistration(ctx context.Context, reg *eeconfig.StackConfig, body stackRegistration) error {
	payload, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		strings.TrimRight(reg.URL, "/")+"/api/v1/apps", bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+reg.AdminKey)

	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode >= 200 && res.StatusCode < 300 {
		return nil
	}
	var buf bytes.Buffer
	buf.ReadFrom(res.Body)
	return fmt.Errorf("stack returned %d: %s", res.StatusCode, strings.TrimSpace(buf.String()))
}

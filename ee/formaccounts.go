//go:build ee

package ee

import (
	"github.com/ittrail/sitebin.io/internal/ext"
)

// The account's half of form recipients the account already knows: the core
// decides whether an owner may skip a recipient's confirmation mail
// (httpapi.knownRecipient) and asks here only about the account. See
// docs/superpowers/specs/2026-11-17-form-recipients-known-to-the-account-design.md.

var _ ext.FormAccounts = (*provider)(nil)

// VerifiedEmail is the account's sign-in address when it is verified: by the
// identity provider's email_verified claim, or by Sitebin's own verification
// mail for a local account. An unverified address is "", never an error.
func (p *provider) VerifiedEmail(accountID string) (string, error) {
	if accountID == "" || p.accounts == nil {
		return "", nil
	}
	acc, err := p.accounts.ByID(accountID)
	if err != nil {
		return "", err
	}
	if !acc.EmailVerified || acc.Suspended() {
		return "", nil
	}
	return acc.Email, nil
}

// OwnedSiteIDs lists the sites the account owns by meta.json (ownedSiteIDs).
// Its error says the list may be incomplete, and the core then asks the
// recipient by mail.
func (p *provider) OwnedSiteIDs(accountID string) ([]string, error) {
	if accountID == "" || p.accounts == nil {
		return nil, nil
	}
	acc, err := p.accounts.ByID(accountID)
	if err != nil {
		return nil, err
	}
	return p.ownedSiteIDs(acc)
}

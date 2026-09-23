//go:build ee

package ee

import "github.com/ittrail/sitebin.io/internal/ext"

var _ ext.OperatorAccounts = (*provider)(nil)

// IsOperator reports whether the account is the instance's operator, for
// operator zones (SITEBIN_OPERATOR_DOMAINS): the same two conditions that
// open the admin console. An account that is unknown is not the operator.
func (p *provider) IsOperator(accountID string) bool {
	if accountID == "" || p.accounts == nil {
		return false
	}
	acc, err := p.accounts.ByID(accountID)
	if err != nil {
		return false
	}
	return p.isAdmin(acc)
}

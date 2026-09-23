//go:build ee

package ee

import "testing"

// The operator of operator zones is exactly who may open the admin console:
// the admin tier AND the allowlist.
func TestIsOperatorNeedsTierAndAllowlist(t *testing.T) {
	p, _, mux := setupAdmin(t, "boss@example.com")
	_, boss := adminUser(t, p, mux, "boss@example.com", "admin")
	_, listedFree := adminUser(t, p, mux, "free@example.com", "free")
	_, unlistedAdmin := adminUser(t, p, mux, "other@example.com", "admin")

	if !p.IsOperator(boss) {
		t.Error("the listed admin-tier account is not the operator")
	}
	if p.IsOperator(listedFree) || p.IsOperator(unlistedAdmin) {
		t.Error("one condition alone made an account the operator")
	}
	if p.IsOperator("") || p.IsOperator("nosuchaccount") {
		t.Error("an unknown account is the operator")
	}
}

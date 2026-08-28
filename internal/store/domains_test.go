package store

import (
	"errors"
	"testing"
)

// With a separate view domain, a site must not be able to claim an address the
// wildcard already answers for.
func TestReservedDomainsCoverTheViewDomain(t *testing.T) {
	st, err := New(t.TempDir(), "app.sitebin.io", 1<<20, 100)
	if err != nil {
		t.Fatal(err)
	}
	st.ReserveDomains("sitebin.app")
	site, _, _ := st.Create()

	for _, d := range []string{"sitebin.app", "evil.sitebin.app", "app.sitebin.io", "x.app.sitebin.io"} {
		if err := st.AddDomain(site, d); !errors.Is(err, ErrBadDomain) {
			t.Errorf("AddDomain(%q) = %v, want ErrBadDomain — the instance already serves there", d, err)
		}
	}
	if err := st.AddDomain(site, "kunde.example.org"); err != nil {
		t.Errorf("an unrelated custom domain must still work: %v", err)
	}
}

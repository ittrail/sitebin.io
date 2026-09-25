package httpapi

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ittrail/sitebin.io/internal/ids"
)

type uploadClock struct{ t time.Time }

func (c *uploadClock) now() time.Time          { return c.t }
func (c *uploadClock) advance(d time.Duration) { c.t = c.t.Add(d) }

func newTestUploads() (*uploadTokens, *uploadClock) {
	c := &uploadClock{t: time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)}
	return newUploadTokens(c.now), c
}

func TestUploadTokenOpensItsSiteOnly(t *testing.T) {
	u, _ := newTestUploads()
	secret, exp, err := u.issue("view1", "edit1")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(secret, ids.UploadTokenPrefix) {
		t.Fatalf("secret = %q", secret)
	}
	if want := time.Date(2026, 9, 25, 13, 0, 0, 0, time.UTC); !exp.Equal(want) {
		t.Errorf("expires = %v, want %v", exp, want)
	}
	end, ok := u.begin(secret, "edit1")
	if !ok {
		t.Fatal("a fresh token was refused")
	}
	end()
	end() // a second call is harmless
	if _, ok := u.begin(secret, "edit2"); ok {
		t.Fatal("the token opened another site")
	}
	if _, ok := u.begin("sbu_nottherealone", "edit1"); ok {
		t.Fatal("an unknown token was accepted")
	}
	if _, ok := u.begin("", "edit1"); ok {
		t.Fatal("an empty credential was accepted")
	}
}

func TestUploadTokenHoldsOnlyTheHash(t *testing.T) {
	u, _ := newTestUploads()
	secret, _, _ := u.issue("view1", "edit1")
	for k := range u.m {
		if strings.Contains(k, secret[len(ids.UploadTokenPrefix):]) {
			t.Fatal("the secret is held in the clear")
		}
		if k != hashUploadToken(secret) {
			t.Fatalf("key %q is not the secret's hash", k)
		}
	}
}

func TestUploadTokenUnusedDiesAfterFiveMinutes(t *testing.T) {
	u, c := newTestUploads()
	secret, _, _ := u.issue("view1", "edit1")
	c.advance(5 * time.Minute)
	if _, ok := u.begin(secret, "edit1"); ok {
		t.Fatal("an unused token outlived its first five minutes")
	}
}

func TestUploadTokenIdleCountsFromTheEndOfTheLastRequest(t *testing.T) {
	u, c := newTestUploads()
	secret, _, _ := u.issue("view1", "edit1")
	c.advance(4 * time.Minute)
	end, ok := u.begin(secret, "edit1")
	if !ok {
		t.Fatal("refused within the idle window")
	}
	c.advance(20 * time.Minute) // a long upload is still running
	end2, ok := u.begin(secret, "edit1")
	if !ok {
		t.Fatal("a token with a request in flight idled out")
	}
	end2()
	end()
	c.advance(4*time.Minute + 59*time.Second)
	end, ok = u.begin(secret, "edit1")
	if !ok {
		t.Fatal("the idle window did not restart when the requests ended")
	}
	end()
	c.advance(5 * time.Minute)
	if _, ok := u.begin(secret, "edit1"); ok {
		t.Fatal("the token outlived five idle minutes")
	}
}

func TestUploadTokenAbsoluteCap(t *testing.T) {
	u, c := newTestUploads()
	secret, _, _ := u.issue("view1", "edit1")
	for minute := 4; minute <= 56; minute += 4 {
		c.advance(4 * time.Minute)
		end, ok := u.begin(secret, "edit1")
		if !ok {
			t.Fatalf("refused at minute %d although it was kept busy", minute)
		}
		end()
	}
	c.advance(3 * time.Minute) // minute 59
	running, ok := u.begin(secret, "edit1")
	if !ok {
		t.Fatal("refused at minute 59")
	}
	c.advance(time.Minute) // minute 60
	if _, ok := u.begin(secret, "edit1"); ok {
		t.Fatal("a new request started at the 60-minute cap")
	}
	running() // the request already running completes
	if _, ok := u.begin(secret, "edit1"); ok {
		t.Fatal("the token outlived its cap")
	}
}

func TestUploadTokenPerSiteCapEvictsTheOldest(t *testing.T) {
	u, c := newTestUploads()
	other, _, _ := u.issue("view2", "edit2")
	var secrets []string
	for i := 0; i < uploadTokensPerSite+1; i++ {
		c.advance(time.Second) // distinct issue times, so "oldest" is well defined
		s, _, err := u.issue("view1", "edit1")
		if err != nil {
			t.Fatal(err)
		}
		secrets = append(secrets, s)
	}
	if _, ok := u.begin(secrets[0], "edit1"); ok {
		t.Error("the oldest token survived a sixth")
	}
	for _, s := range secrets[1:] {
		if end, ok := u.begin(s, "edit1"); !ok {
			t.Error("a newer token was evicted")
		} else {
			end()
		}
	}
	if end, ok := u.begin(other, "edit2"); !ok {
		t.Error("another site's token was evicted")
	} else {
		end()
	}
}

// Review focus 3: an agent that opens more uploads while its first is still
// running must not crash anything, and the evicted token refuses afterwards.
func TestUploadTokenEvictedWhileInFlight(t *testing.T) {
	u, c := newTestUploads()
	first, _, _ := u.issue("view1", "edit1")
	running, ok := u.begin(first, "edit1")
	if !ok {
		t.Fatal("fresh token refused")
	}
	for i := 0; i < uploadTokensPerSite; i++ {
		c.advance(time.Second)
		if _, _, err := u.issue("view1", "edit1"); err != nil {
			t.Fatal(err)
		}
	}
	running() // must not panic on an evicted record
	if _, ok := u.begin(first, "edit1"); ok {
		t.Fatal("an evicted token still opens the site")
	}
}

func TestUploadTokenGlobalBackstop(t *testing.T) {
	u, c := newTestUploads()
	u.max = 2
	u.issue("a", "ea")
	u.issue("b", "eb")
	if _, _, err := u.issue("c", "ec"); !errors.Is(err, errTooManyUploads) {
		t.Fatalf("err = %v, want errTooManyUploads", err)
	}
	c.advance(5 * time.Minute) // both expire unused and are pruned on the next issue
	if _, _, err := u.issue("c", "ec"); err != nil {
		t.Fatalf("expired tokens still counted: %v", err)
	}
}

func TestUploadTokenRevokeSite(t *testing.T) {
	u, _ := newTestUploads()
	a, _, _ := u.issue("view1", "edit1")
	b, _, _ := u.issue("view2", "edit2")
	u.revokeSite("view1")
	if _, ok := u.begin(a, "edit1"); ok {
		t.Error("a revoked token still works")
	}
	if end, ok := u.begin(b, "edit2"); !ok {
		t.Error("revoking one site revoked another")
	} else {
		end()
	}
}

//go:build ee

package account

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/ittrail/sitebin.io/internal/ids"
)

// Store persists accounts under the data root. Indexes (email, oauth) are
// small files containing the target account id — only the backend reads them,
// so no symlinks are needed. Safe for concurrent use.
type Store struct {
	root string

	mu    sync.Mutex
	locks map[string]*sync.Mutex
}

// New opens/creates the account store rooted at dataDir.
func New(dataDir string) (*Store, error) {
	s := &Store{root: dataDir, locks: map[string]*sync.Mutex{}}
	for _, d := range []string{s.accountsDir(), s.indexDir("email"), s.indexDir("oauth"), s.indexDir("billing"), s.indexDir("token")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return nil, fmt.Errorf("init account store: %w", err)
		}
	}
	return s, nil
}

func (s *Store) accountsDir() string         { return filepath.Join(s.root, "accounts") }
func (s *Store) indexDir(kind string) string { return filepath.Join(s.root, "account-index", kind) }
func (s *Store) accountDir(id string) string { return filepath.Join(s.accountsDir(), id) }

func hashKey(v string) string {
	sum := sha256.Sum256([]byte(v))
	return hex.EncodeToString(sum[:])
}

func (s *Store) lock(id string) *sync.Mutex {
	s.mu.Lock()
	defer s.mu.Unlock()
	l, ok := s.locks[id]
	if !ok {
		l = &sync.Mutex{}
		s.locks[id] = l
	}
	return l
}

// claimIndex atomically creates an index entry pointing at accountID, failing
// with taken if it already exists.
func (s *Store) claimIndex(kind, key, accountID string, taken error) error {
	p := filepath.Join(s.indexDir(kind), key)
	f, err := os.OpenFile(p, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		if os.IsExist(err) {
			return taken
		}
		return err
	}
	defer f.Close()
	_, err = f.WriteString(accountID)
	return err
}

func (s *Store) resolveIndex(kind, key string) (string, error) {
	b, err := os.ReadFile(filepath.Join(s.indexDir(kind), key))
	if err != nil {
		if os.IsNotExist(err) {
			return "", ErrNotFound
		}
		return "", err
	}
	return string(b), nil
}

// CreateLocal registers a local (email+password) account.
func (s *Store) CreateLocal(email, passwordHash, tier string) (*Account, error) {
	norm, err := normalizeEmail(email)
	if err != nil {
		return nil, err
	}
	a := s.newAccount(Local, norm, tier)
	a.PasswordHash = passwordHash
	if err := s.claimIndex("email", hashKey(norm), a.ID, ErrEmailTaken); err != nil {
		return nil, err
	}
	if err := s.persist(a); err != nil {
		os.Remove(filepath.Join(s.indexDir("email"), hashKey(norm)))
		return nil, err
	}
	return a, nil
}

// CreateOAuth registers an OAuth (Google/Microsoft) account. The email index is
// claimed too when an email is present, linking future logins.
func (s *Store) CreateOAuth(provider Provider, subject, email, tier string) (*Account, error) {
	norm := ""
	if email != "" {
		var err error
		if norm, err = normalizeEmail(email); err != nil {
			return nil, err
		}
	}
	a := s.newAccount(provider, norm, tier)
	a.OAuthSubject = subject
	a.EmailVerified = norm != "" // provider-asserted

	oauthKey := hashKey(string(provider) + ":" + subject)
	if err := s.claimIndex("oauth", oauthKey, a.ID, ErrOAuthTaken); err != nil {
		return nil, err
	}
	if norm != "" {
		if err := s.claimIndex("email", hashKey(norm), a.ID, ErrEmailTaken); err != nil {
			os.Remove(filepath.Join(s.indexDir("oauth"), oauthKey))
			return nil, err
		}
	}
	if err := s.persist(a); err != nil {
		os.Remove(filepath.Join(s.indexDir("oauth"), oauthKey))
		if norm != "" {
			os.Remove(filepath.Join(s.indexDir("email"), hashKey(norm)))
		}
		return nil, err
	}
	return a, nil
}

func (s *Store) newAccount(p Provider, email, tier string) *Account {
	now := time.Now().UTC()
	return &Account{
		ID:        ids.New(),
		Provider:  p,
		Email:     email,
		Tier:      tier,
		CreatedAt: now,
		UpdatedAt: now,
	}
}

// ByID loads an account by id.
func (s *Store) ByID(id string) (*Account, error) {
	if id == "" || !ids.ValidID(id) {
		return nil, ErrNotFound
	}
	b, err := os.ReadFile(filepath.Join(s.accountDir(id), "account.json"))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	var a Account
	if err := json.Unmarshal(b, &a); err != nil {
		return nil, fmt.Errorf("parse account %s: %w", id, err)
	}
	return &a, nil
}

// ByEmail loads an account by its (normalized) email.
func (s *Store) ByEmail(email string) (*Account, error) {
	norm, err := normalizeEmail(email)
	if err != nil {
		return nil, ErrNotFound
	}
	id, err := s.resolveIndex("email", hashKey(norm))
	if err != nil {
		return nil, err
	}
	return s.ByID(id)
}

// ByOAuth loads an account by provider + subject.
func (s *Store) ByOAuth(provider Provider, subject string) (*Account, error) {
	id, err := s.resolveIndex("oauth", hashKey(string(provider)+":"+subject))
	if err != nil {
		return nil, err
	}
	return s.ByID(id)
}

// Update atomically mutates an account under its lock.
func (s *Store) Update(a *Account, mutate func(*Account) error) error {
	l := s.lock(a.ID)
	l.Lock()
	defer l.Unlock()

	cur, err := s.ByID(a.ID)
	if err != nil {
		return err
	}
	if err := mutate(cur); err != nil {
		return err
	}
	cur.UpdatedAt = time.Now().UTC()
	if err := s.persist(cur); err != nil {
		return err
	}
	*a = *cur
	return nil
}

// persist writes account.json atomically.
func (s *Store) persist(a *Account) error {
	dir := s.accountDir(a.ID)
	if err := os.MkdirAll(filepath.Join(dir, "sites"), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(a, "", "  ")
	if err != nil {
		return err
	}
	tmp := filepath.Join(dir, "account.json.tmp")
	if err := os.WriteFile(tmp, append(b, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(dir, "account.json"))
}

// LinkSite records that the account owns viewID (a marker file named by the id).
func (s *Store) LinkSite(a *Account, viewID string) error {
	if !ids.ValidID(viewID) {
		return fmt.Errorf("invalid site id")
	}
	l := s.lock(a.ID)
	l.Lock()
	defer l.Unlock()
	f, err := os.OpenFile(filepath.Join(s.accountDir(a.ID), "sites", viewID), os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	return f.Close()
}

// UnlinkSite removes the ownership marker (no error if already gone).
func (s *Store) UnlinkSite(a *Account, viewID string) error {
	if !ids.ValidID(viewID) {
		return fmt.Errorf("invalid site id")
	}
	err := os.Remove(filepath.Join(s.accountDir(a.ID), "sites", viewID))
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// LinkBilling records a provider customer → account mapping so future webhooks
// (which carry only the customer id) resolve to this account.
func (s *Store) LinkBilling(a *Account, provider, customer string) error {
	if customer == "" {
		return nil
	}
	p := filepath.Join(s.indexDir("billing"), hashKey(provider+":"+customer))
	return os.WriteFile(p, []byte(a.ID), 0o644)
}

// ByBilling resolves an account by provider + customer id.
func (s *Store) ByBilling(provider, customer string) (*Account, error) {
	id, err := s.resolveIndex("billing", hashKey(provider+":"+customer))
	if err != nil {
		return nil, err
	}
	return s.ByID(id)
}

// ListSiteIDs returns the view ids of the sites this account owns.
func (s *Store) ListSiteIDs(a *Account) ([]string, error) {
	entries, err := os.ReadDir(filepath.Join(s.accountDir(a.ID), "sites"))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		if ids.ValidID(e.Name()) {
			out = append(out, e.Name())
		}
	}
	return out, nil
}

// Delete removes the account, its index entries, and (via deleteSite) each
// owned site. deleteSite may be nil to leave sites ownerless.
func (s *Store) Delete(a *Account, deleteSite func(viewID string) error) error {
	l := s.lock(a.ID)
	l.Lock()
	defer l.Unlock()

	siteIDs, _ := s.ListSiteIDs(a)
	if deleteSite != nil {
		for _, vid := range siteIDs {
			if err := deleteSite(vid); err != nil {
				return fmt.Errorf("delete owned site %s: %w", vid, err)
			}
		}
	}
	// remove indexes
	os.Remove(filepath.Join(s.indexDir("email"), hashKey(a.Email)))
	if a.OAuthSubject != "" {
		os.Remove(filepath.Join(s.indexDir("oauth"), hashKey(string(a.Provider)+":"+a.OAuthSubject)))
	}
	if a.Billing != nil && a.Billing.Customer != "" {
		os.Remove(filepath.Join(s.indexDir("billing"), hashKey(a.Billing.Provider+":"+a.Billing.Customer)))
	}
	if err := os.RemoveAll(s.accountDir(a.ID)); err != nil {
		return err
	}
	s.mu.Lock()
	delete(s.locks, a.ID)
	s.mu.Unlock()
	return nil
}

//go:build ee

package account

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/ittrail/sitebin.io/internal/ids"
)

// MaxTokensPerAccount bounds how many tokens one account may hold. Tokens are
// cheap to make and each one is a standing credential; a cap keeps a
// compromised session from minting an unbounded set of them.
const MaxTokensPerAccount = 25

// ErrTooManyTokens reports that the account is at MaxTokensPerAccount.
var ErrTooManyTokens = errors.New("account: too many API tokens")

// Token is the record of an account API token. The secret itself is NEVER
// stored: only its SHA-256 lives on, as the filename of an index entry
// pointing back at the account. A lost token cannot be recovered, only
// replaced — which is the property that makes storing them safe.
type Token struct {
	ID        string    `json:"id"`
	Name      string    `json:"name,omitempty"` // optional label chosen by the owner
	Prefix    string    `json:"prefix"`         // first few chars, to tell tokens apart in the UI
	CreatedAt time.Time `json:"created_at"`
}

func (s *Store) tokensDir(accountID string) string {
	return filepath.Join(s.accountDir(accountID), "tokens")
}

// CreateToken mints a token for the account and returns the record together
// with the secret. The secret is returned exactly once; after this call the
// only thing that survives is its hash.
func (s *Store) CreateToken(a *Account, name string) (Token, string, error) {
	l := s.lock(a.ID)
	l.Lock()
	defer l.Unlock()

	existing, err := s.listTokensLocked(a.ID)
	if err != nil {
		return Token{}, "", err
	}
	if len(existing) >= MaxTokensPerAccount {
		return Token{}, "", ErrTooManyTokens
	}

	secret := ids.NewAPIToken()
	tok := Token{
		ID:        ids.New(),
		Name:      strings.TrimSpace(name),
		Prefix:    secret[:len(ids.APITokenPrefix)+6],
		CreatedAt: time.Now().UTC(),
	}
	if err := os.MkdirAll(s.tokensDir(a.ID), 0o700); err != nil {
		return Token{}, "", err
	}
	// Claim the index first: it is the lookup path, and an index entry with no
	// record is a token that authenticates nothing, while a record with no
	// index entry would be a token the owner can see and revoke but never use.
	if err := s.claimIndex("token", hashKey(secret), a.ID+":"+tok.ID, fmt.Errorf("token collision")); err != nil {
		return Token{}, "", err
	}
	b, err := json.Marshal(tok)
	if err != nil {
		return Token{}, "", err
	}
	if err := os.WriteFile(filepath.Join(s.tokensDir(a.ID), tok.ID+".json"), b, 0o600); err != nil {
		os.Remove(filepath.Join(s.indexDir("token"), hashKey(secret)))
		return Token{}, "", err
	}
	return tok, secret, nil
}

// ByToken resolves a presented secret to its account. It reports ok=false for
// anything it does not recognise — a wrong token and an absent one are the same
// answer, deliberately.
func (s *Store) ByToken(secret string) (*Account, bool) {
	if !strings.HasPrefix(secret, ids.APITokenPrefix) {
		return nil, false
	}
	ref, err := s.resolveIndex("token", hashKey(secret))
	if err != nil {
		return nil, false
	}
	accountID, tokenID, ok := strings.Cut(ref, ":")
	if !ok {
		return nil, false
	}
	// The record is the revocation switch: deleting it revokes the token even
	// if the index entry outlives it.
	if _, err := os.Stat(filepath.Join(s.tokensDir(accountID), tokenID+".json")); err != nil {
		return nil, false
	}
	acc, err := s.ByID(accountID)
	if err != nil {
		return nil, false
	}
	return acc, true
}

// ListTokens returns the account's tokens, newest first.
func (s *Store) ListTokens(a *Account) ([]Token, error) {
	l := s.lock(a.ID)
	l.Lock()
	defer l.Unlock()
	return s.listTokensLocked(a.ID)
}

func (s *Store) listTokensLocked(accountID string) ([]Token, error) {
	entries, err := os.ReadDir(s.tokensDir(accountID))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	out := make([]Token, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(s.tokensDir(accountID), e.Name()))
		if err != nil {
			continue // an unreadable record must not hide the others
		}
		var t Token
		if json.Unmarshal(b, &t) == nil {
			out = append(out, t)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out, nil
}

// DeleteToken revokes a token by id. The index entry is left behind — it is
// keyed by a hash nobody can reverse, and ByToken already treats a missing
// record as "no such token", so the credential is dead either way.
func (s *Store) DeleteToken(a *Account, tokenID string) error {
	if !ids.ValidID(tokenID) {
		return fmt.Errorf("invalid token id")
	}
	l := s.lock(a.ID)
	l.Lock()
	defer l.Unlock()
	err := os.Remove(filepath.Join(s.tokensDir(a.ID), tokenID+".json"))
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

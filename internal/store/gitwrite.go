package store

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"sync"
	"time"
)

// A push writes into the site's repository (GitDir) for as long as its pack
// streams — minutes — and never holds the site lock while it does: a push
// must not hold up the site. What it holds instead is a registration here,
// one per site. Whatever ends the site's right to change — a delete, the
// purge, a lock of any kind, "Delete repository" — cancels the running push
// and waits for its git process to exit before it goes on. Otherwise git,
// still finishing, could recreate sites/<id>/git/objects/xx after the folder
// was removed, or change the repository a lock holds as evidence.
//
// The registry is per process. A lock or a delete from the CLI (another
// process) is seen by the next push, which reads meta.json, but cannot stop
// one this server is running.

// ErrGitBusy refuses a second push to a site while one is running.
var ErrGitBusy = errors.New("another push to this site is running")

// gitStopWait bounds how long a delete or a lock waits for a cancelled push
// to end. A killed git process exits at once; this only keeps a stuck one
// from holding the site lock forever. A variable for the tests.
var gitStopWait = 30 * time.Second

// gitWrite is one registered push: how to cancel it, and done, closed by
// its end.
type gitWrite struct {
	cancel context.CancelFunc
	done   chan struct{}
}

// BeginGitWrite registers a push to site, which cancel stops (the caller's
// context.CancelFunc for the git process). It refuses a second push to the
// same site (ErrGitBusy), a locked site (ErrLocked) and a site that is gone
// or being deleted (ErrNotFound). The caller runs git only after it
// succeeded, and calls end once git has exited — on every path, typically
// deferred. end is safe to call more than once.
//
// end takes only the registry's own mutex, never the site lock or the stats
// lock: a delete or a lock waits for it while holding the site lock. So
// nothing between BeginGitWrite and end may take the site lock either.
func (s *Store) BeginGitWrite(site *Site, cancel context.CancelFunc) (end func(), err error) {
	if cancel == nil {
		cancel = func() {}
	}
	w := &gitWrite{cancel: cancel, done: make(chan struct{})}
	s.gitMu.Lock()
	if err := s.gitBarred[site.ViewID]; err != nil {
		s.gitMu.Unlock()
		return nil, err
	}
	if s.gitRunning[site.ViewID] != nil {
		s.gitMu.Unlock()
		return nil, ErrGitBusy
	}
	s.gitRunning[site.ViewID] = w
	s.gitMu.Unlock()

	var once sync.Once
	end = func() {
		once.Do(func() {
			s.gitMu.Lock()
			if s.gitRunning[site.ViewID] == w {
				delete(s.gitRunning, site.ViewID)
			}
			s.gitMu.Unlock()
			close(w.done)
		})
	}
	// Registered first, meta.json read second. A lock writes meta.json and
	// then stops the registered push: one registered before that stop is
	// cancelled by it, and one registered after reads the lock here. (A
	// delete bars the site for the same window; see barGitWrites.) Locks are
	// read from meta.json, never from memory: the CLI locks from another
	// process.
	if err := s.CheckUnlocked(site); err != nil {
		end()
		return nil, err
	}
	return end, nil
}

// GitWriteRunning reports whether a push to site is registered right now. It
// only looks — it never registers, so asking cannot make a push that begins
// at the same moment fail — and the answer can be stale at once: it serves a
// readable early refusal, while BeginGitWrite stays the guarantee.
func (s *Store) GitWriteRunning(site *Site) bool {
	s.gitMu.Lock()
	defer s.gitMu.Unlock()
	return s.gitRunning[site.ViewID] != nil
}

// stopGitWrite cancels the push running on viewID, if any, and waits for it
// to end — at most gitStopWait. Delete, DeleteGitRepo and the abuse guard's
// hold call it holding the site lock; SetLock and ReleaseLock right after
// their write. It never holds the registry's mutex while it waits, so the
// push's end can always run.
func (s *Store) stopGitWrite(viewID string) {
	s.gitMu.Lock()
	w := s.gitRunning[viewID]
	s.gitMu.Unlock()
	if w == nil {
		return
	}
	w.cancel()
	t := time.NewTimer(gitStopWait)
	defer t.Stop()
	select {
	case <-w.done:
	case <-t.C:
		slog.Warn("git: a cancelled push did not end in time; going on without it", "site", viewID, "waited", gitStopWait)
	}
}

// barGitWrites refuses every new registration on viewID with err until the
// returned function is called. A delete sets it before it stops the running
// push and lifts it once the folder is gone: a push registering after the
// stop and before the removal would otherwise write into the folder as it
// goes. The caller holds the site lock, so there is one bar per site at a
// time.
func (s *Store) barGitWrites(viewID string, err error) (unbar func()) {
	s.gitMu.Lock()
	s.gitBarred[viewID] = err
	s.gitMu.Unlock()
	return func() {
		s.gitMu.Lock()
		delete(s.gitBarred, viewID)
		s.gitMu.Unlock()
	}
}

// stopGitWriteIfLocked stops the site's push when site.Meta — as a lock
// writer just wrote it — carries a lock. Every path that writes a lock calls
// it after the write.
func (s *Store) stopGitWriteIfLocked(site *Site) {
	if site.Meta.Locked != nil {
		s.stopGitWrite(site.ViewID)
	}
}

// DeleteGitRepo removes the site's repository and frees its bytes: the
// owner's "Delete repository". A locked site is refused (ErrLocked) — its
// repository is evidence — and a running push is cancelled and waited for
// first. A site with no repository is not an error.
func (s *Store) DeleteGitRepo(site *Site) error {
	l := s.lockSite(site.ViewID)
	l.Lock()
	defer l.Unlock()
	if err := s.CheckUnlocked(site); err != nil {
		return err
	}
	unbar := s.barGitWrites(site.ViewID, ErrGitBusy)
	defer unbar()
	s.stopGitWrite(site.ViewID)
	if err := os.RemoveAll(site.GitDir()); err != nil {
		return fmt.Errorf("delete repository: %w", err)
	}
	return nil
}

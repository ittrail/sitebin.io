package store

import (
	"testing"
	"time"
)

// The view counter is lightweight: a page load during a long upload, which
// holds the site lock for the whole upload, must not wait for it.
func TestRecordViewSkipsWhileTheSiteLockIsHeld(t *testing.T) {
	s := newTestStore(t)
	site, _, _ := s.Create()
	l := s.lockSite(site.ViewID)
	l.Lock()
	done := make(chan struct{})
	go func() {
		s.RecordView(site)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		l.Unlock()
		<-done
		t.Fatal("RecordView waited for the site lock")
	}
	l.Unlock()
	if v := s.Stats(site).Views; v != 0 {
		t.Errorf("a view recorded while the site was locked: views = %d", v)
	}

	s.RecordView(site)
	if st := s.Stats(site); st.Views != 1 || st.LastSeen == nil {
		t.Errorf("with the lock free: views = %d, last_seen = %v", st.Views, st.LastSeen)
	}
}

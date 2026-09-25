package store

import (
	"sync"
	"testing"
	"time"
)

// A page load counts a view while a long upload holds the site lock: the
// counter has its own lock, so it neither waits for the upload nor drops the
// view.
func TestRecordViewDoesNotWaitForTheSiteLock(t *testing.T) {
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
	if st := s.Stats(site); st.Views != 1 || st.LastSeen == nil {
		t.Errorf("a view during a held site lock: views = %d, last_seen = %v", st.Views, st.LastSeen)
	}
}

// Views that arrive together are all counted: none is dropped because another
// holds the counter at that moment.
func TestRecordViewCountsConcurrentViews(t *testing.T) {
	s := newTestStore(t)
	site, _, _ := s.Create()
	const n = 40
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.RecordView(site)
		}()
	}
	wg.Wait()
	if v := s.Stats(site).Views; v != n {
		t.Errorf("views = %d, want %d", v, n)
	}
}

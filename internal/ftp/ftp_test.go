package ftp

import (
	"testing"

	"github.com/ittrail/sitebin.io/internal/config"
)

// An FTP session that goes quiet is closed: without an idle timeout every
// half-open control connection is held for ever.
func TestFTPSettingsHaveIdleTimeout(t *testing.T) {
	d := &driver{cfg: config.Config{FTPAddr: ":0", FTPPasvMin: 30000, FTPPasvMax: 30001}}
	s, err := d.GetSettings()
	if err != nil {
		t.Fatal(err)
	}
	if s.IdleTimeout <= 0 || s.ConnectionTimeout <= 0 {
		t.Errorf("IdleTimeout=%d ConnectionTimeout=%d, both must be set", s.IdleTimeout, s.ConnectionTimeout)
	}
}

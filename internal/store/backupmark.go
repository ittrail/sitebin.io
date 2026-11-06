package store

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"time"
)

// BackupMarkFile is the name, in the data directory, of the note the last
// complete `sitebin backup` leaves: when it finished, how many entries it
// archived and how many bytes it wrote. The operator's health view reads it;
// nothing else does. The backup itself never archives it, so a restored
// instance does not claim a backup it never made.
const BackupMarkFile = "backup-last.json"

// BackupMark is that note.
type BackupMark struct {
	At      time.Time `json:"at"`
	Entries int       `json:"entries"`
	Skipped int       `json:"skipped"`
	// Bytes is the size of the compressed archive as written — the size of
	// the file the backup produced, wherever its output went.
	Bytes int64 `json:"bytes"`
}

// WriteBackupMark records m in dataDir, replacing the previous note in one
// rename, so a reader never sees half of one.
func WriteBackupMark(dataDir string, m BackupMark) error {
	b, err := json.Marshal(m)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dataDir, ".backup-last-*.tmp")
	if err != nil {
		return err
	}
	name := tmp.Name()
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		os.Remove(name)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(name)
		return err
	}
	if err := os.Chmod(name, 0o644); err != nil {
		os.Remove(name)
		return err
	}
	if err := os.Rename(name, filepath.Join(dataDir, BackupMarkFile)); err != nil {
		os.Remove(name)
		return err
	}
	return nil
}

// ReadBackupMark returns the last complete backup's note; ok is false when no
// backup has left one.
func ReadBackupMark(dataDir string) (m BackupMark, ok bool, err error) {
	b, err := os.ReadFile(filepath.Join(dataDir, BackupMarkFile))
	if errors.Is(err, fs.ErrNotExist) {
		return BackupMark{}, false, nil
	}
	if err != nil {
		return BackupMark{}, false, err
	}
	if err := json.Unmarshal(b, &m); err != nil {
		return BackupMark{}, false, err
	}
	return m, true, nil
}

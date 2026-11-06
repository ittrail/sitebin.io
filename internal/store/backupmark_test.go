package store

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestBackupMarkRoundTrip(t *testing.T) {
	dir := t.TempDir()
	if _, ok, err := ReadBackupMark(dir); err != nil || ok {
		t.Fatalf("no mark yet: ok=%v err=%v", ok, err)
	}
	at := time.Date(2026, 11, 7, 3, 10, 0, 0, time.UTC)
	if err := WriteBackupMark(dir, BackupMark{At: at, Entries: 57883, Skipped: 2, Bytes: 4908122012}); err != nil {
		t.Fatal(err)
	}
	m, ok, err := ReadBackupMark(dir)
	if err != nil || !ok {
		t.Fatalf("read: ok=%v err=%v", ok, err)
	}
	if !m.At.Equal(at) || m.Entries != 57883 || m.Skipped != 2 || m.Bytes != 4908122012 {
		t.Fatalf("mark = %+v", m)
	}
	// a second backup replaces it, and leaves no temp file behind
	if err := WriteBackupMark(dir, BackupMark{At: at.Add(24 * time.Hour), Entries: 1}); err != nil {
		t.Fatal(err)
	}
	if m, _, _ = ReadBackupMark(dir); m.Entries != 1 {
		t.Fatalf("not replaced: %+v", m)
	}
	left, _ := filepath.Glob(filepath.Join(dir, ".backup-last-*"))
	if len(left) != 0 {
		t.Fatalf("temp files left: %v", left)
	}
}

func TestBackupMarkUnreadableIsAnError(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, BackupMarkFile), []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := ReadBackupMark(dir); err == nil || ok {
		t.Fatalf("a broken note must be an error, not a backup: ok=%v err=%v", ok, err)
	}
}

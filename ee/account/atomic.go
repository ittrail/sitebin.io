//go:build ee

package account

import (
	"os"
	"path/filepath"
)

// WriteFileAtomic writes b to dst the way account.json is written: a
// temporary file beside it, renamed over it by replaceFile (which retries the
// Windows rename onto a file another handle has open). A crash leaves a
// "<dst>.tmp-*" file and the old dst whole. For the stores in ee that keep
// their own files and need the same guarantee.
func WriteFileAtomic(dst string, b []byte) error {
	f, err := os.CreateTemp(filepath.Dir(dst), filepath.Base(dst)+".tmp-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	_, err = f.Write(b)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(tmp)
		return err
	}
	if err := replaceFile(tmp, dst); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

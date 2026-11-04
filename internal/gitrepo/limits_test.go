package gitrepo

import (
	"bytes"
	"compress/zlib"
	"context"
	"crypto/rand"
	"crypto/sha1"
	"encoding/binary"
	"io"
	"io/fs"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
)

// deltaBomb is a pack of two objects a few kilobytes long: a 16 MiB blob of
// zeros and a delta against it whose result claims to be result bytes, made
// of copy instructions. Resolving it means allocating result in one piece.
func deltaBomb(t *testing.T, result uint64) []byte {
	t.Helper()
	const baseSize = 16 << 20
	const chunk = 0xFFFF00 // one copy instruction: a multiple of 256 under 16 MiB
	ops := result / chunk
	var delta bytes.Buffer
	delta.Write(packVarint(baseSize))
	delta.Write(packVarint(ops * chunk))
	for i := uint64(0); i < ops; i++ {
		// copy from offset 0 (no offset bytes), size bytes 2 and 3
		delta.Write([]byte{0x80 | 0x20 | 0x40, byte(chunk >> 8 & 0xff), byte(chunk >> 16 & 0xff)})
	}
	var pack bytes.Buffer
	pack.WriteString("PACK")
	_ = binary.Write(&pack, binary.BigEndian, uint32(2))
	_ = binary.Write(&pack, binary.BigEndian, uint32(2))
	base := pack.Len()
	pack.Write(packObjHeader(3, baseSize)) // blob
	pack.Write(deflate(t, make([]byte, baseSize)))
	deltaAt := pack.Len()
	pack.Write(packObjHeader(6, uint64(delta.Len()))) // OFS_DELTA
	rel := uint64(deltaAt - base)
	off := []byte{byte(rel & 0x7f)}
	for rel >>= 7; rel > 0; rel >>= 7 {
		rel--
		off = append([]byte{byte(0x80 | rel&0x7f)}, off...)
	}
	pack.Write(off)
	pack.Write(deflate(t, delta.Bytes()))
	sum := sha1.Sum(pack.Bytes())
	pack.Write(sum[:])
	return pack.Bytes()
}

func packObjHeader(typ int, size uint64) []byte {
	c := byte(typ<<4) | byte(size&0x0f)
	size >>= 4
	var b []byte
	for size > 0 {
		b = append(b, c|0x80)
		c = byte(size & 0x7f)
		size >>= 7
	}
	return append(b, c)
}

func packVarint(n uint64) []byte {
	var b []byte
	for {
		c := byte(n & 0x7f)
		n >>= 7
		if n == 0 {
			return append(b, c)
		}
		b = append(b, c|0x80)
	}
}

func deflate(t *testing.T, p []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	w, err := zlib.NewWriterLevel(&buf, zlib.BestCompression)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write(p); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// A pack whose delta claims gigabytes is refused at once: git dies at the
// first allocation over GIT_ALLOC_LIMIT instead of taking the memory (about
// a gigabyte of RSS per git process without it), and nothing stays behind.
func TestDeltaBombIsRefusedQuickly(t *testing.T) {
	NeedGit(t)
	r, dir := newBare(t)
	before, _ := dirSize(t, dir)
	pack := deltaBomb(t, 2<<30)
	if len(pack) > 64<<10 {
		t.Fatalf("the bomb is %d bytes; it should be a few kilobytes", len(pack))
	}
	body := cmdPkt(zeros, sha1b, "refs/heads/main", "report-status side-band-64k") + "0000" + string(pack)
	rec := httptest.NewRecorder()
	start := time.Now()
	_, err := r.ServeRPC(context.Background(), dir, ReceivePack, strings.NewReader(body), rec, RPCOptions{MaxInputSize: 100 << 20})
	took := time.Since(start)
	out := rec.Body.String()
	t.Logf("took %v, err %v", took, err)
	if strings.Contains(out, "unpack ok") {
		t.Fatalf("the bomb was unpacked:\n%q", out)
	}
	// git's own words for the allocation it refused, relayed on the side band.
	if !regexp.MustCompile(`attempting to allocate \d+ over limit 268435456`).MatchString(out) {
		t.Errorf("the refusal does not come from the allocation limit:\n%q", out)
	}
	if took > 20*time.Second {
		t.Errorf("the refusal took %v", took)
	}
	if refs, err := r.RefTargets(context.Background(), dir); err != nil || len(refs) != 0 {
		t.Errorf("refs after the bomb: %v, %v", refs, err)
	}
	if after, names := dirSize(t, dir); after != before {
		t.Errorf("repository %d bytes after the bomb, %d before: %v", after, before, names)
	}
}

// A push is kept as the pack it came in, never exploded into loose objects
// (one compressed file per object): transfer.unpackLimit=1 sends even a
// push of three objects through index-pack.
func TestPushStaysAPack(t *testing.T) {
	NeedGit(t)
	r, dir := newBare(t)
	srv, _ := serveRepo(t, r, dir, 0)
	c := newClient(t)
	wd := filepath.Join(t.TempDir(), "w")
	c.mustGit(filepath.Dir(wd), "clone", srv.URL+"/repo.git", wd)
	commitFile(t, c, wd, "a.txt", "one small file")
	c.mustGit(wd, "push", "origin", "main")

	loose := regexp.MustCompile(`^[0-9a-f]{2}$`)
	packs := 0
	err := filepath.WalkDir(filepath.Join(dir, "objects"), func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !d.Type().IsRegular() {
			return nil
		}
		if loose.MatchString(filepath.Base(filepath.Dir(p))) {
			t.Errorf("loose object after a push: %s", p)
		}
		if strings.HasSuffix(p, ".pack") {
			packs++
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if packs != 1 {
		t.Errorf("%d packs after one push, want 1", packs)
	}
}

// A single large file still pushes, clones, collects and downloads under
// the memory bounds (core.bigFileThreshold streams it whole). Opt-in, for a
// manual check: SITEBIN_GIT_LARGE_FILE_MB=100 go test ./internal/gitrepo
// -run TestLargeFileUnderTheLimits -v. A file above AllocLimit sent as a
// delta against another may be refused; one sent whole is not.
func TestLargeFileUnderTheLimits(t *testing.T) {
	mb, _ := strconv.Atoi(os.Getenv("SITEBIN_GIT_LARGE_FILE_MB"))
	if mb <= 0 {
		t.Skip("set SITEBIN_GIT_LARGE_FILE_MB to run")
	}
	NeedGit(t)
	r, dir := newBare(t)
	srv, log := serveRepo(t, r, dir, 0)
	c := newClient(t)
	url := srv.URL + "/repo.git"
	wd := filepath.Join(t.TempDir(), "w")
	c.mustGit(filepath.Dir(wd), "clone", url, wd)
	f, err := os.Create(filepath.Join(wd, "big.bin"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.CopyN(f, rand.Reader, int64(mb)<<20); err != nil {
		t.Fatal(err)
	}
	f.Close()
	c.mustGit(wd, "add", "big.bin")
	c.mustGit(wd, "commit", "-q", "-m", "big")
	want := c.mustGit(wd, "rev-parse", "HEAD:big.bin")

	start := time.Now()
	c.mustGit(wd, "push", "origin", "main")
	t.Logf("push of %d MiB: %v", mb, time.Since(start))
	if len(log.errs) != 0 {
		t.Fatalf("server errors: %v", log.errs)
	}
	start = time.Now()
	wd2 := filepath.Join(t.TempDir(), "w2")
	c.mustGit(filepath.Dir(wd2), "clone", url, wd2)
	t.Logf("clone: %v", time.Since(start))
	if got := c.mustGit(wd2, "rev-parse", "HEAD:big.bin"); got != want {
		t.Fatalf("cloned blob %s, want %s", got, want)
	}
	if err := MarkPushed(dir); err != nil {
		t.Fatal(err)
	}
	start = time.Now()
	if ran, err := r.Collect(context.Background(), dir); err != nil || !ran {
		t.Fatalf("Collect: ran=%v err=%v", ran, err)
	}
	t.Logf("gc: %v", time.Since(start))
	sha := c.mustGit(wd, "rev-parse", "HEAD")
	b, err := r.Blob(context.Background(), dir, sha, "big.bin")
	if err != nil || b.Size != int64(mb)<<20 || (!b.Binary && !b.Truncated) {
		t.Errorf("Blob = size %d binary %v truncated %v, %v", b.Size, b.Binary, b.Truncated, err)
	}
	var n countWriter
	if err := r.WriteBlob(context.Background(), dir, sha, "big.bin", &n); err != nil || int64(n) != int64(mb)<<20 {
		t.Errorf("WriteBlob wrote %d bytes: %v", n, err)
	}
	n = 0
	start = time.Now()
	if err := r.Archive(context.Background(), dir, sha, &n); err != nil || int64(n) < int64(mb)<<20 {
		t.Errorf("Archive wrote %d bytes: %v", n, err)
	}
	t.Logf("archive: %v", time.Since(start))
}

type countWriter int64

func (c *countWriter) Write(p []byte) (int, error) { *c += countWriter(len(p)); return len(p), nil }

package gitrepo

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const sha1a = "1111111111111111111111111111111111111111"
const sha1b = "2222222222222222222222222222222222222222"

func cmdPkt(old, new, ref, caps string) string {
	s := old + " " + new + " " + ref
	if caps != "" {
		s += "\x00" + caps
	}
	return pkt(s + "\n")
}

func TestReadPkt(t *testing.T) {
	var raw bytes.Buffer
	p, flush, err := readPkt(strings.NewReader(pkt("hello\n")), &raw)
	if err != nil || flush || string(p) != "hello\n" || raw.String() != pkt("hello\n") {
		t.Errorf("data pkt: %q %v %v raw %q", p, flush, err, raw.String())
	}
	if _, flush, err := readPkt(strings.NewReader("0000"), nil); err != nil || !flush {
		t.Errorf("flush: %v %v", flush, err)
	}
	for name, in := range map[string]string{
		"bad hex":       "zzzzabc",
		"length 1":      "0001",
		"length 3":      "0003",
		"over the max":  "fff1",
		"truncated hdr": "00",
		"truncated":     "000aab",
		"empty":         "",
	} {
		if _, _, err := readPkt(strings.NewReader(in), nil); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
}

func TestParseCommand(t *testing.T) {
	u, err := parseCommand([]byte(sha1a + " " + sha1b + " refs/heads/x\x00report-status side-band-64k\n"))
	if err != nil || u != (RefUpdate{Ref: "refs/heads/x", Old: sha1a, New: sha1b}) {
		t.Errorf("with caps: %+v %v", u, err)
	}
	u, err = parseCommand([]byte(sha1a + " " + zeros + " refs/heads/gone\n"))
	if err != nil || u.New != zeros {
		t.Errorf("delete: %+v %v", u, err)
	}
	for _, bad := range []string{"", "x", sha1a + " " + sha1b, "short " + sha1b + " refs/x", sha1a + " " + sha1b + " "} {
		if _, err := parseCommand([]byte(bad)); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func untouched(t *testing.T, rec *httptest.ResponseRecorder) {
	t.Helper()
	if len(rec.Header()) != 0 || rec.Body.Len() != 0 || rec.Flushed {
		t.Errorf("response touched: headers %v, body %q, flushed %v", rec.Header(), rec.Body.String(), rec.Flushed)
	}
}

func TestServeRPCPreResponseErrorsLeaveTheWriterUntouched(t *testing.T) {
	NeedGit(t)
	r, dir := newBare(t)
	ctx := context.Background()
	var bigCmds strings.Builder
	for i := 0; bigCmds.Len() < maxCommandBytes+1000; i++ {
		bigCmds.WriteString(cmdPkt(zeros, sha1b, fmt.Sprintf("refs/heads/branch-%08d-%s", i, strings.Repeat("x", 200)), ""))
	}
	cases := map[string]struct {
		svc  Service
		body string
		gz   bool
	}{
		"bad gzip":          {ReceivePack, "not gzip at all", true},
		"malformed command": {ReceivePack, pkt("garbage\n") + "0000", false},
		"bad pkt length":    {ReceivePack, "zzzz", false},
		"missing flush":     {ReceivePack, cmdPkt(zeros, sha1b, "refs/heads/x", ""), false},
		"over the cap":      {ReceivePack, bigCmds.String(), false},
		"unknown service":   {Service("git-nope"), "", false},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			_, err := r.ServeRPC(ctx, dir, c.svc, strings.NewReader(c.body), rec, RPCOptions{Gzip: c.gz})
			if err == nil {
				t.Fatal("no error")
			}
			untouched(t, rec)
		})
	}
	t.Run("busy", func(t *testing.T) {
		r1, dir1 := newBareMax(t, 1)
		hold, err := r1.acquire(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer hold()
		cctx, cancel := context.WithCancel(ctx)
		cancel()
		rec := httptest.NewRecorder()
		if _, err := r1.ServeRPC(cctx, dir1, ReceivePack, strings.NewReader(pkt("x")), rec, RPCOptions{}); !errors.Is(err, ErrBusy) {
			t.Errorf("ServeRPC err = %v", err)
		}
		untouched(t, rec)
		rec = httptest.NewRecorder()
		if err := r1.AdvertiseRefs(cctx, dir1, UploadPack, "", rec); !errors.Is(err, ErrBusy) {
			t.Errorf("AdvertiseRefs err = %v", err)
		}
		untouched(t, rec)
	})
}

func newBareMax(t *testing.T, max int) (*Runner, string) {
	r, _ := newRunner(t, max)
	dir := filepath.Join(t.TempDir(), "git")
	if err := r.Init(dir); err != nil {
		t.Fatal(err)
	}
	return r, dir
}

func TestServeRPCReportsEveryCommand(t *testing.T) {
	NeedGit(t)
	r, dir := newBare(t)
	body := pkt("shallow "+sha1a+"\n") +
		cmdPkt(zeros, sha1b, "refs/heads/new", "report-status") +
		cmdPkt(sha1a, zeros, "refs/heads/gone", "") + "0000"
	res, _ := r.ServeRPC(context.Background(), dir, ReceivePack, strings.NewReader(body), httptest.NewRecorder(), RPCOptions{})
	want := []RefUpdate{
		{Ref: "refs/heads/new", Old: zeros, New: sha1b},
		{Ref: "refs/heads/gone", Old: sha1a, New: zeros},
	}
	if len(res.Updates) != 2 || res.Updates[0] != want[0] || res.Updates[1] != want[1] {
		t.Errorf("updates = %+v, want %+v", res.Updates, want)
	}
	if res.BytesIn != int64(len(body)) {
		t.Errorf("BytesIn = %d, want %d", res.BytesIn, len(body))
	}
}

func TestBytesInCountsGzipBytesAsReceived(t *testing.T) {
	NeedGit(t)
	r, dir := newBare(t)
	var raw bytes.Buffer
	_ = writePkt(&raw, "want "+strings.Repeat("0", 40)+"\n")
	raw.WriteString("0000")
	_ = writePkt(&raw, "done\n")
	var gz bytes.Buffer
	zw := gzip.NewWriter(&gz)
	_, _ = zw.Write(raw.Bytes())
	_ = zw.Close()
	wire := int64(gz.Len())
	res, _ := r.ServeRPC(context.Background(), dir, UploadPack, &gz, httptest.NewRecorder(), RPCOptions{Gzip: true})
	if res.BytesIn != wire {
		t.Errorf("BytesIn = %d, want the %d gzip bytes on the wire", res.BytesIn, wire)
	}
}

func TestStalledClientDoesNotHoldASlot(t *testing.T) {
	NeedGit(t)
	r, dir := newBareMax(t, 1)
	pr, pw := io.Pipe()
	t.Cleanup(func() { pw.Close() })
	go func() {
		// The command section, a flush, a pack header and junk, then silence: the
		// connection stays open while git (max input 1 byte) gives up.
		_, _ = io.WriteString(pw, cmdPkt(zeros, sha1b, "refs/heads/main", "report-status")+"0000"+"PACK\x00\x00\x00\x02\x00\x00\x00\x01"+strings.Repeat("\x00", 64))
	}()
	done := make(chan error, 1)
	go func() {
		_, err := r.ServeRPC(context.Background(), dir, ReceivePack, pr, httptest.NewRecorder(), RPCOptions{MaxInputSize: 1})
		done <- err
	}()
	select {
	case <-done: // git reports its refusal in-band; the error value is not the point
	case <-time.After(15 * time.Second):
		t.Fatal("ServeRPC still blocked on the stalled body")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	release, err := r.acquire(ctx)
	if err != nil {
		t.Fatalf("slot not released: %v", err)
	}
	release()
}

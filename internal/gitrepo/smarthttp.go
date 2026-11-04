package gitrepo

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

// Service is a smart HTTP service name.
type Service string

const (
	UploadPack  Service = "git-upload-pack"
	ReceivePack Service = "git-receive-pack"
)

// ErrBadRequest marks a request ServeRPC refused before git started: a body
// that is not gzip, or a push command section it cannot parse. Nothing was
// written to the client, so the caller may answer it with a 400.
var ErrBadRequest = errors.New("gitrepo: bad request")

// maxStderr caps the stderr kept for an error message.
const maxStderr = 8 << 10

// maxCommandBytes caps the command section of a push we are willing to parse.
const maxCommandBytes = 1 << 20

// ParseService maps the ?service= value or the URL's last segment to a Service.
func ParseService(s string) (Service, bool) {
	switch Service(s) {
	case UploadPack:
		return UploadPack, true
	case ReceivePack:
		return ReceivePack, true
	}
	return "", false
}

// ProtocolFromHeader returns "version=2" when the Git-Protocol header asks for
// exactly that, and "" otherwise (anything else is served as protocol v0).
func ProtocolFromHeader(h string) string {
	if strings.TrimSpace(h) == "version=2" {
		return "version=2"
	}
	return ""
}

func (s Service) sub() string { return strings.TrimPrefix(string(s), "git-") }

func protoEnv(protocol string) []string {
	if protocol == "version=2" {
		return []string{"GIT_PROTOCOL=version=2"}
	}
	return nil
}

// flushWriter copies git's output to the client as it comes, and keeps a
// client that stops reading from holding the git process (and its slot):
// every write gets a deadline — idle from its start, never past end — and a
// write that fails cancels git through fail.
type flushWriter struct {
	w    http.ResponseWriter
	rc   *http.ResponseController
	idle time.Duration
	end  time.Time
	fail context.CancelFunc
}

// newFlushWriter writes the response of a git process that fail cancels.
// Call the returned done once nothing writes any more: it lifts the write
// deadline from the connection, which outlives the response.
func (r *Runner) newFlushWriter(w http.ResponseWriter, fail context.CancelFunc) (fw *flushWriter, done func()) {
	fw = &flushWriter{w: w, rc: http.NewResponseController(w), idle: r.responseIdle, end: time.Now().Add(r.responseMax), fail: fail}
	return fw, func() { _ = fw.rc.SetWriteDeadline(time.Time{}) }
}

func (f *flushWriter) Write(p []byte) (int, error) {
	d := time.Now().Add(f.idle)
	if d.After(f.end) {
		d = f.end
	}
	// A writer without deadlines (a test recorder) cannot block either.
	_ = f.rc.SetWriteDeadline(d)
	n, err := f.w.Write(p)
	if err == nil {
		if ferr := f.rc.Flush(); ferr != nil && !errors.Is(ferr, http.ErrNotSupported) {
			err = ferr
		}
	}
	if err != nil {
		f.fail()
	}
	return n, err
}

// setHeaders marks a smart HTTP response: its content type, never cached,
// never sniffed or rendered as anything else.
func setHeaders(w http.ResponseWriter, contentType string) {
	h := w.Header()
	h.Set("Content-Type", contentType)
	h.Set("Cache-Control", "no-cache")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Content-Security-Policy", "sandbox")
}

// capWriter keeps the first max bytes written.
type capWriter struct {
	buf bytes.Buffer
	max int
}

func (c *capWriter) Write(p []byte) (int, error) {
	if room := c.max - c.buf.Len(); room > 0 {
		if len(p) < room {
			room = len(p)
		}
		c.buf.Write(p[:room])
	}
	return len(p), nil
}

// countReader counts bytes read; atomic because git's stdin copier may still
// be reading after Run gave up waiting for it.
type countReader struct {
	r io.Reader
	n atomic.Int64
}

func (c *countReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n.Add(int64(n))
	return n, err
}

// AdvertiseRefs answers GET info/refs?service=<svc>.
func (r *Runner) AdvertiseRefs(ctx context.Context, dir string, svc Service, protocol string, w http.ResponseWriter) error {
	if _, ok := ParseService(string(svc)); !ok {
		return errors.New("gitrepo: unknown service")
	}
	release, err := r.acquire(ctx)
	if err != nil {
		return err
	}
	defer release()
	ctx, cancel := context.WithTimeout(ctx, r.responseMax)
	defer cancel()
	setHeaders(w, "application/x-"+string(svc)+"-advertisement")
	fw, done := r.newFlushWriter(w, cancel)
	defer done()
	if protocol != "version=2" {
		if err := writePkt(fw, "# service="+string(svc)+"\n"); err != nil {
			return err
		}
		if _, err := io.WriteString(fw, "0000"); err != nil {
			return err
		}
	}
	cmd := r.commandWith(ctx, dir, nil, protoEnv(protocol), svc.sub(), "--stateless-rpc", "--advertise-refs", ".")
	guardProcess(cmd)
	errBuf := &capWriter{max: maxStderr}
	cmd.Stdout, cmd.Stderr = fw, errBuf
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("gitrepo: %s advertise: %w: %s", svc, err, strings.TrimSpace(errBuf.buf.String()))
	}
	return nil
}

// RefUpdate is one ref command of a push. 40 zeros mean created or deleted.
type RefUpdate struct{ Ref, Old, New string }

// RPCOptions tune ServeRPC.
type RPCOptions struct {
	Protocol     string // "version=2" or ""
	Gzip         bool   // body is gzip-encoded
	MaxInputSize int64  // receive.maxInputSize for a push; 0 leaves git's default
}

// RPCResult reports what a request did.
type RPCResult struct {
	Updates []RefUpdate
	// BytesIn counts request body bytes as received on the wire, that is
	// before gzip decoding.
	BytesIn int64
	// Started: git ran. A push that started and then failed (killed,
	// cancelled, cut off) may have left objects behind.
	Started bool
}

// processWait bounds how long Run waits for git's pipes after git has exited
// or been cancelled. Without it a client that stalls on the request body
// would hold a process slot until the server's read timeout.
const processWait = 3 * time.Second

// guardProcess makes cmd end promptly: the whole process group dies on
// cancel (where supported) and Run stops waiting for I/O after processWait.
func guardProcess(cmd *exec.Cmd) {
	ownProcessGroup(cmd)
	cmd.WaitDelay = processWait
}

// ServeRPC answers POST <svc>: body is the request, git's answer streams to w.
//
// body is read on a goroutine of its own, beside the one copying git's
// output to w, and that goroutine may still be reading when ServeRPC
// returns (git exited early; the copier ends at its next write). So body
// must never touch w — http.MaxBytesReader does, through the server's own
// writer, once its limit is passed — and the caller must stop body's reads
// before its handler returns, since net/http forbids reading a request body
// after that.
func (r *Runner) ServeRPC(ctx context.Context, dir string, svc Service, body io.Reader, w http.ResponseWriter, o RPCOptions) (RPCResult, error) {
	var res RPCResult
	if _, ok := ParseService(string(svc)); !ok {
		return res, errors.New("gitrepo: unknown service")
	}
	release, err := r.acquire(ctx)
	if err != nil {
		return res, err
	}
	defer release()
	ctx, cancel := context.WithTimeout(ctx, r.responseMax)
	defer cancel()

	// On HTTP/1.x the request body may become unreadable once the response
	// starts, and we stream git's output while still feeding its stdin. The
	// error is ignored: HTTP/2 is full duplex already and test recorders do
	// not implement it.
	_ = http.NewResponseController(w).EnableFullDuplex()

	cr := &countReader{r: body}
	var in io.Reader = cr
	if o.Gzip {
		zr, err := gzip.NewReader(cr)
		if err != nil {
			return res, fmt.Errorf("gitrepo: gzip body: %w: %w", ErrBadRequest, err)
		}
		defer zr.Close()
		in = zr
	}

	var extraC []string
	if svc == ReceivePack {
		// Read the command section (up to the first flush) so the caller
		// learns which refs the push touches, then replay it into git.
		var raw bytes.Buffer
		for {
			p, flush, err := readPkt(in, &raw)
			if err != nil {
				res.BytesIn = cr.n.Load()
				return res, fmt.Errorf("gitrepo: push commands: %w: %w", ErrBadRequest, err)
			}
			if flush {
				break
			}
			if raw.Len() > maxCommandBytes {
				res.BytesIn = cr.n.Load()
				return res, fmt.Errorf("gitrepo: push commands too long: %w", ErrBadRequest)
			}
			if bytes.HasPrefix(p, []byte("shallow ")) {
				// A push from a shallow clone lists its shallow boundary
				// first; git still gets the line, it is no ref update.
				continue
			}
			u, err := parseCommand(p)
			if err != nil {
				res.BytesIn = cr.n.Load()
				return res, fmt.Errorf("%w: %w", ErrBadRequest, err)
			}
			res.Updates = append(res.Updates, u)
		}
		in = io.MultiReader(&raw, in)
		if o.MaxInputSize > 0 {
			extraC = append(extraC, "receive.maxInputSize="+strconv.FormatInt(o.MaxInputSize, 10))
		}
	}

	setHeaders(w, "application/x-"+string(svc)+"-result")
	cmd := r.commandWith(ctx, dir, extraC, protoEnv(o.Protocol), svc.sub(), "--stateless-rpc", ".")
	guardProcess(cmd)
	errBuf := &capWriter{max: maxStderr}
	fw, done := r.newFlushWriter(w, cancel)
	defer done()
	cmd.Stdout, cmd.Stderr = fw, errBuf
	// Feed stdin ourselves instead of through cmd.Stdin: exec's own copier
	// cannot be interrupted while it blocks on the client's request body, so
	// a git that exits early (refusal, bad pack, size limit) would keep this
	// call, and its process slot, until the server's read timeout. Our copier
	// is simply abandoned when git is gone; it ends when the body does.
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return res, fmt.Errorf("gitrepo: %s: %w", svc, err)
	}
	if err := cmd.Start(); err != nil {
		return res, fmt.Errorf("gitrepo: %s: %w", svc, err)
	}
	res.Started = true
	go func() {
		_, _ = io.Copy(stdin, in)
		_ = stdin.Close()
	}()
	runErr := cmd.Wait()
	res.BytesIn = cr.n.Load()
	if runErr != nil {
		return res, fmt.Errorf("gitrepo: %s: %w: %s", svc, runErr, strings.TrimSpace(errBuf.buf.String()))
	}
	return res, nil
}

// WriteRefusal answers a smart HTTP request with a git-level error, which the
// client prints as "fatal: remote error: <msg>".
func WriteRefusal(w http.ResponseWriter, svc Service, msg string) {
	setHeaders(w, "application/x-"+string(svc)+"-advertisement")
	w.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(w, pkt("# service="+string(svc)+"\n")+"0000"+pkt("ERR "+msg+"\n"))
}

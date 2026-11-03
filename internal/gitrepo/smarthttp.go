package gitrepo

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
)

// Service is a smart HTTP service name.
type Service string

const (
	UploadPack  Service = "git-upload-pack"
	ReceivePack Service = "git-receive-pack"
)

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

// flushWriter copies git's output to the client as it comes.
type flushWriter struct {
	w  http.ResponseWriter
	rc *http.ResponseController
}

func newFlushWriter(w http.ResponseWriter) flushWriter {
	return flushWriter{w, http.NewResponseController(w)}
}

func (f flushWriter) Write(p []byte) (int, error) {
	n, err := f.w.Write(p)
	if err == nil {
		_ = f.rc.Flush()
	}
	return n, err
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

type countReader struct {
	r io.Reader
	n int64
}

func (c *countReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)
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
	w.Header().Set("Content-Type", "application/x-"+string(svc)+"-advertisement")
	w.Header().Set("Cache-Control", "no-cache")
	fw := newFlushWriter(w)
	if protocol != "version=2" {
		if err := writePkt(fw, "# service="+string(svc)+"\n"); err != nil {
			return err
		}
		if _, err := io.WriteString(fw, "0000"); err != nil {
			return err
		}
	}
	cmd := r.commandWith(ctx, dir, nil, protoEnv(protocol), svc.sub(), "--stateless-rpc", "--advertise-refs", ".")
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
	BytesIn int64 // request body bytes as received (before gunzip)
}

// ServeRPC answers POST <svc>: body is the request, git's answer streams to w.
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

	cr := &countReader{r: body}
	var in io.Reader = cr
	if o.Gzip {
		zr, err := gzip.NewReader(cr)
		if err != nil {
			return res, fmt.Errorf("gitrepo: gzip body: %w", err)
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
				res.BytesIn = cr.n
				return res, fmt.Errorf("gitrepo: push commands: %w", err)
			}
			if flush {
				break
			}
			if raw.Len() > maxCommandBytes {
				res.BytesIn = cr.n
				return res, errors.New("gitrepo: push commands too long")
			}
			u, err := parseCommand(p)
			if err != nil {
				res.BytesIn = cr.n
				return res, err
			}
			res.Updates = append(res.Updates, u)
		}
		in = io.MultiReader(&raw, in)
		if o.MaxInputSize > 0 {
			extraC = append(extraC, "receive.maxInputSize="+strconv.FormatInt(o.MaxInputSize, 10))
		}
	}

	w.Header().Set("Content-Type", "application/x-"+string(svc)+"-result")
	w.Header().Set("Cache-Control", "no-cache")
	cmd := r.commandWith(ctx, dir, extraC, protoEnv(o.Protocol), svc.sub(), "--stateless-rpc", ".")
	errBuf := &capWriter{max: maxStderr}
	cmd.Stdin = in
	cmd.Stdout, cmd.Stderr = newFlushWriter(w), errBuf
	runErr := cmd.Run()
	res.BytesIn = cr.n
	if runErr != nil {
		return res, fmt.Errorf("gitrepo: %s: %w: %s", svc, runErr, strings.TrimSpace(errBuf.buf.String()))
	}
	return res, nil
}

// WriteRefusal answers a smart HTTP request with a git-level error, which the
// client prints as "fatal: remote error: <msg>".
func WriteRefusal(w http.ResponseWriter, svc Service, msg string) {
	w.Header().Set("Content-Type", "application/x-"+string(svc)+"-advertisement")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(w, pkt("# service="+string(svc)+"\n")+"0000"+pkt("ERR "+msg+"\n"))
}

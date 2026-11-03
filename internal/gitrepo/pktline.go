package gitrepo

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// maxPkt is the largest pkt-line git allows (length field included).
const maxPkt = 65520

// writePkt writes one length-prefixed pkt-line.
func writePkt(w io.Writer, payload string) error {
	if len(payload)+4 > maxPkt {
		return errors.New("gitrepo: pkt-line too long")
	}
	_, err := fmt.Fprintf(w, "%04x%s", len(payload)+4, payload)
	return err
}

// pkt returns payload as a pkt-line string.
func pkt(payload string) string {
	var b bytes.Buffer
	_ = writePkt(&b, payload)
	return b.String()
}

// readPkt reads one pkt-line. flush is true for "0000" (payload nil). The
// raw bytes read are appended to raw when it is not nil.
func readPkt(r io.Reader, raw *bytes.Buffer) (payload []byte, flush bool, err error) {
	var hdr [4]byte
	if _, err = io.ReadFull(r, hdr[:]); err != nil {
		return nil, false, err
	}
	if raw != nil {
		raw.Write(hdr[:])
	}
	n, perr := strconv.ParseUint(string(hdr[:]), 16, 32)
	if perr != nil {
		return nil, false, errors.New("gitrepo: bad pkt-line length")
	}
	switch {
	case n == 0:
		return nil, true, nil
	case n < 4 || n > maxPkt:
		return nil, false, errors.New("gitrepo: bad pkt-line length")
	}
	payload = make([]byte, n-4)
	if _, err = io.ReadFull(r, payload); err != nil {
		return nil, false, err
	}
	if raw != nil {
		raw.Write(payload)
	}
	return payload, false, nil
}

// parseCommand parses "old new ref" (optionally followed by "\x00caps" and a
// newline) from a receive-pack command pkt-line.
func parseCommand(p []byte) (RefUpdate, error) {
	s := string(p)
	if i := strings.IndexByte(s, 0); i >= 0 {
		s = s[:i]
	}
	s = strings.TrimSuffix(s, "\n")
	parts := strings.SplitN(s, " ", 3)
	if len(parts) != 3 || len(parts[0]) != 40 || len(parts[1]) != 40 || parts[2] == "" {
		return RefUpdate{}, errors.New("gitrepo: bad ref update command")
	}
	return RefUpdate{Old: parts[0], New: parts[1], Ref: parts[2]}, nil
}

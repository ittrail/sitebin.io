package gitrepo

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// Limits of the read queries.
const (
	// LogPage is how many commits one history page holds.
	LogPage = 30
	// MaxChangedFiles caps the files listed for one commit.
	MaxChangedFiles = 300
	// MaxTextBytes caps the text of a file shown inline.
	MaxTextBytes = 512 << 10
	// MaxTreeEntries caps the entries listed for one folder.
	MaxTreeEntries = 5000
	// ReadTimeout bounds every read command.
	ReadTimeout = 15 * time.Second
	// StreamTimeout bounds a file or ZIP download, which a slow client may
	// take minutes to receive.
	StreamTimeout = 10 * time.Minute

	// binarySniff is how much of a file git itself looks at for a NUL.
	binarySniff = 8000
	// maxOutput bounds what a listing command may print before it is killed.
	maxOutput = 32 << 20
)

var (
	// ErrBadRef means a ref or object name is unusable or names no commit.
	ErrBadRef = errors.New("gitrepo: bad ref")
	// ErrNotFound means a path does not exist in the commit.
	ErrNotFound = errors.New("gitrepo: not found")
)

// CommitSummary is one line of history.
type CommitSummary struct {
	SHA     string    `json:"sha"`
	Subject string    `json:"subject"`
	Author  string    `json:"author"`
	Date    time.Time `json:"date"`
}

// Ref is a branch or a tag; Name is the short name and Kind is "branch" or "tag".
type Ref struct {
	Name    string        `json:"name"`
	Kind    string        `json:"kind"`
	Commit  CommitSummary `json:"commit"`
	Default bool          `json:"default"`
}

// ChangedFile is one file a commit touches; Added and Removed are -1 for a
// binary file.
type ChangedFile struct {
	Path    string `json:"path"`
	Status  string `json:"status"`
	Added   int    `json:"added"`
	Removed int    `json:"removed"`
}

// CommitDetail is a commit with its message and changed files.
type CommitDetail struct {
	CommitSummary
	Message        string        `json:"message"`
	AuthorEmail    string        `json:"author_email"`
	Committer      string        `json:"committer"`
	CommitterDate  time.Time     `json:"committer_date"`
	Parents        []string      `json:"parents"`
	Files          []ChangedFile `json:"files"`
	FilesTruncated bool          `json:"files_truncated"`
}

// TreeEntry is one name in a folder; Type is "dir", "file", "link" or "submodule".
type TreeEntry struct {
	Name string `json:"name"`
	Type string `json:"type"`
	Size int64  `json:"size"`
}

// Tree is the listing of one folder.
type Tree struct {
	Entries   []TreeEntry `json:"entries"`
	Truncated bool        `json:"truncated"`
}

// Blob is a file's content for display: text up to MaxTextBytes, or just its
// size when it is binary.
type Blob struct {
	Size      int64  `json:"size"`
	Binary    bool   `json:"binary"`
	Text      string `json:"text"`
	Truncated bool   `json:"truncated"`
}

// capBuf collects output up to max bytes and cancels the command beyond it.
type capBuf struct {
	buf    bytes.Buffer
	max    int
	over   bool
	cancel context.CancelFunc
}

func (c *capBuf) Write(p []byte) (int, error) {
	room := c.max - c.buf.Len()
	if len(p) > room {
		c.buf.Write(p[:room])
		c.over = true
		c.cancel()
		return len(p), nil
	}
	return c.buf.Write(p)
}

// exitFailure reports whether err is git exiting with a failure status
// (as opposed to a timeout or a start failure).
func exitFailure(err error) bool {
	var ee *exec.ExitError
	return errors.As(err, &ee)
}

// run executes git for a read: a slot, the read timeout, a bounded stdout.
// over is true when the output was cut at max bytes (the command is killed
// and its status ignored).
func (r *Runner) run(ctx context.Context, dir string, max int, args ...string) (out []byte, over bool, err error) {
	ctx, cancel := context.WithTimeout(ctx, ReadTimeout)
	defer cancel()
	release, err := r.acquire(ctx)
	if err != nil {
		return nil, false, err
	}
	defer release()
	runCtx, stop := context.WithCancel(ctx)
	defer stop()
	cb := &capBuf{max: max, cancel: stop}
	var stderr bytes.Buffer
	cmd := r.command(runCtx, dir, args...)
	guardProcess(cmd)
	cmd.Stdout = cb
	cmd.Stderr = &stderr
	runErr := cmd.Run()
	if cb.over {
		return cb.buf.Bytes(), true, nil
	}
	if runErr != nil {
		if ctx.Err() != nil {
			return nil, false, ctx.Err()
		}
		if exitFailure(runErr) {
			return nil, false, fmt.Errorf("gitrepo: git %s: %w: %s", args[0], runErr, strings.TrimSpace(stderr.String()))
		}
		return nil, false, runErr
	}
	return cb.buf.Bytes(), false, nil
}

// stream runs git with its stdout going straight to w.
func (r *Runner) stream(ctx context.Context, dir string, w io.Writer, args ...string) error {
	ctx, cancel := context.WithTimeout(ctx, StreamTimeout)
	defer cancel()
	release, err := r.acquire(ctx)
	if err != nil {
		return err
	}
	defer release()
	var stderr bytes.Buffer
	cmd := r.command(ctx, dir, args...)
	guardProcess(cmd)
	cmd.Stdout = w
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("gitrepo: git %s: %w: %s", args[0], err, strings.TrimSpace(stderr.String()))
	}
	return nil
}

// checkSHA accepts only a full hex object name, which can never be an option.
func checkSHA(sha string) error {
	if n := len(sha); n != 40 && n != 64 {
		return ErrBadRef
	}
	for _, c := range sha {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return ErrBadRef
		}
	}
	return nil
}

// cleanPath turns an untrusted path into a slash path relative to the tree
// root ("" is the root). Anything that could leave the tree is ErrNotFound.
func cleanPath(p string) (string, error) {
	if strings.ContainsAny(p, "\x00\\") || strings.HasPrefix(p, "/") {
		return "", ErrNotFound
	}
	var parts []string
	for _, s := range strings.Split(p, "/") {
		switch s {
		case "", ".":
		case "..":
			return "", ErrNotFound
		default:
			parts = append(parts, s)
		}
	}
	return strings.Join(parts, "/"), nil
}

// object is the <sha>:<path> spelling of a path in a commit.
func object(sha, path string) (string, error) {
	if err := checkSHA(sha); err != nil {
		return "", err
	}
	p, err := cleanPath(path)
	if err != nil {
		return "", err
	}
	return sha + ":" + p, nil
}

// Resolve turns a branch, tag, full ref name or (abbreviated) sha into a
// commit sha. A branch wins over a tag of the same name.
func (r *Runner) Resolve(ctx context.Context, dir, ref string) (string, error) {
	if ref == "" || strings.HasPrefix(ref, "-") || strings.ContainsAny(ref, "\x00\n") {
		return "", ErrBadRef
	}
	// Only a plain ref name or a hex object name is accepted: git would
	// otherwise read "main@{0}", "main~1", ":/text" and the like as revision
	// expressions, in any of the three spellings.
	valid, err := r.validRefName(ctx, dir, "refs/heads/"+ref)
	if err != nil {
		return "", err
	}
	candidates := []string{}
	if valid {
		candidates = append(candidates, "refs/heads/"+ref, "refs/tags/"+ref)
		if strings.HasPrefix(ref, "refs/") {
			candidates = append(candidates, ref)
		}
	}
	if isHex(ref) && len(ref) >= 4 && len(ref) <= 64 {
		candidates = append(candidates, ref)
	}
	for _, name := range candidates {
		out, _, err := r.run(ctx, dir, 4096, "rev-parse", "--verify", "--quiet", "--end-of-options", name+"^{commit}")
		if err != nil {
			if exitFailure(errors.Unwrap(err)) {
				continue
			}
			return "", err
		}
		sha := strings.TrimSpace(string(out))
		if checkSHA(sha) == nil {
			return sha, nil
		}
	}
	return "", ErrBadRef
}

func isHex(s string) bool {
	for _, c := range s {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F') {
			return false
		}
	}
	return s != ""
}

// validRefName asks git whether name (which starts with "refs/") is a
// well-formed full ref name.
func (r *Runner) validRefName(ctx context.Context, dir, name string) (bool, error) {
	_, _, err := r.run(ctx, dir, 4096, "check-ref-format", name)
	if err == nil {
		return true, nil
	}
	if exitFailure(errors.Unwrap(err)) {
		return false, nil
	}
	return false, err
}

// DefaultBranch is the short name HEAD points at, even when it is unborn.
func (r *Runner) DefaultBranch(ctx context.Context, dir string) (string, error) {
	out, _, err := r.run(ctx, dir, 4096, "symbolic-ref", "HEAD")
	if err != nil {
		return "", err
	}
	return strings.TrimPrefix(strings.TrimSpace(string(out)), "refs/heads/"), nil
}

func parseTime(s string) time.Time {
	t, _ := time.Parse(time.RFC3339, s)
	return t
}

// Refs lists the branches and tags that point at a commit.
func (r *Runner) Refs(ctx context.Context, dir string) ([]Ref, error) {
	def, _ := r.DefaultBranch(ctx, dir)
	const f = "%(refname)%00%(objecttype)%00%(objectname)%00%(authorname)%00%(authordate:iso-strict)%00%(contents:subject)%00" +
		"%(*objecttype)%00%(*objectname)%00%(*authorname)%00%(*authordate:iso-strict)%00%(*contents:subject)%00"
	out, _, err := r.run(ctx, dir, maxOutput, "for-each-ref", "--format="+f, "refs/heads", "refs/tags")
	if err != nil {
		return nil, err
	}
	const fields = 11
	toks := strings.Split(string(out), "\x00")
	var refs []Ref
	for i := 0; i+fields <= len(toks); i += fields {
		t := toks[i : i+fields]
		t[0] = strings.TrimLeft(t[0], "\n") // for-each-ref ends each record with a newline
		var ref Ref
		switch {
		case strings.HasPrefix(t[0], "refs/heads/"):
			ref = Ref{Name: strings.TrimPrefix(t[0], "refs/heads/"), Kind: "branch"}
			ref.Default = ref.Name == def
		case strings.HasPrefix(t[0], "refs/tags/"):
			ref = Ref{Name: strings.TrimPrefix(t[0], "refs/tags/"), Kind: "tag"}
		default:
			continue
		}
		switch {
		case t[1] == "commit":
			ref.Commit = CommitSummary{SHA: t[2], Author: t[3], Date: parseTime(t[4]), Subject: t[5]}
		case t[1] == "tag" && t[6] == "commit":
			ref.Commit = CommitSummary{SHA: t[7], Author: t[8], Date: parseTime(t[9]), Subject: t[10]}
		default:
			continue // a tag of a tree or blob
		}
		refs = append(refs, ref)
	}
	if refs == nil {
		refs = []Ref{}
	}
	return refs, nil
}

// Log lists up to n commits reachable from sha, newest first, after skipping skip.
func (r *Runner) Log(ctx context.Context, dir, sha string, skip, n int) ([]CommitSummary, error) {
	if err := checkSHA(sha); err != nil {
		return nil, err
	}
	if skip < 0 {
		skip = 0
	}
	if n < 1 || n > LogPage {
		n = LogPage
	}
	out, _, err := r.run(ctx, dir, maxOutput, "log", "-z", "--format=%H%x00%an%x00%aI%x00%s",
		"--skip="+strconv.Itoa(skip), "--max-count="+strconv.Itoa(n), "--end-of-options", sha, "--")
	if err != nil {
		return nil, err
	}
	toks := strings.Split(string(out), "\x00")
	list := []CommitSummary{}
	for i := 0; i+4 <= len(toks); i += 4 {
		list = append(list, CommitSummary{SHA: toks[i], Author: toks[i+1], Date: parseTime(toks[i+2]), Subject: toks[i+3]})
	}
	return list, nil
}

// Commit returns a commit's metadata and the files it changes (against its
// first parent; a root commit adds everything).
func (r *Runner) Commit(ctx context.Context, dir, sha string) (CommitDetail, error) {
	var d CommitDetail
	if err := checkSHA(sha); err != nil {
		return d, err
	}
	out, _, err := r.run(ctx, dir, 4<<20, "show", "-s", "--format=%H%x00%an%x00%ae%x00%aI%x00%cn%x00%cI%x00%P%x00%s%x00%B", "--end-of-options", sha)
	if err != nil {
		if exitFailure(errors.Unwrap(err)) {
			return d, ErrNotFound
		}
		return d, err
	}
	t := strings.SplitN(string(out), "\x00", 9)
	if len(t) != 9 {
		return d, fmt.Errorf("gitrepo: unexpected commit output")
	}
	d.SHA, d.Author, d.AuthorEmail, d.Date = t[0], t[1], t[2], parseTime(t[3])
	d.Committer, d.CommitterDate = t[4], parseTime(t[5])
	d.Parents = strings.Fields(t[6])
	d.Subject = t[7]
	d.Message = strings.TrimRight(t[8], "\n")

	diff := []string{"show", "-m", "--first-parent", "--no-renames", "-z", "--format="}
	nums, numsOver, err := r.run(ctx, dir, maxOutput, append(append([]string{}, diff...), "--numstat", "--end-of-options", sha)...)
	if err != nil {
		return d, err
	}
	stats, statsOver, err := r.run(ctx, dir, maxOutput, append(append([]string{}, diff...), "--name-status", "--end-of-options", sha)...)
	if err != nil {
		return d, err
	}
	status := map[string]string{}
	st := strings.Split(string(stats), "\x00")
	for i := 0; i+1 < len(st); i += 2 {
		status[st[i+1]] = st[i]
	}
	d.Files = []ChangedFile{}
	for _, rec := range strings.Split(string(nums), "\x00") {
		if rec == "" {
			continue
		}
		// "<added>\t<removed>\t<path>"; the path may hold anything but NUL.
		p := strings.SplitN(rec, "\t", 3)
		if len(p) != 3 {
			continue
		}
		if len(d.Files) == MaxChangedFiles {
			d.FilesTruncated = true
			break
		}
		f := ChangedFile{Path: p[2], Status: status[p[2]], Added: -1, Removed: -1}
		if p[0] != "-" {
			f.Added, _ = strconv.Atoi(p[0])
			f.Removed, _ = strconv.Atoi(p[1])
		}
		if f.Status == "" {
			f.Status = "M"
		}
		d.Files = append(d.Files, f)
	}
	if numsOver || statsOver {
		d.FilesTruncated = true
	}
	return d, nil
}

// Tree lists one folder of a commit.
func (r *Runner) Tree(ctx context.Context, dir, sha, path string) (Tree, error) {
	obj, err := object(sha, path)
	if err != nil {
		return Tree{}, err
	}
	out, over, err := r.run(ctx, dir, maxOutput, "ls-tree", "-z", "--long", "--end-of-options", obj)
	if err != nil {
		if exitFailure(errors.Unwrap(err)) {
			return Tree{}, ErrNotFound
		}
		return Tree{}, err
	}
	tr := Tree{Entries: []TreeEntry{}, Truncated: over}
	for _, rec := range strings.Split(string(out), "\x00") {
		if rec == "" {
			continue
		}
		meta, name, ok := strings.Cut(rec, "\t")
		if !ok {
			continue
		}
		f := strings.Fields(meta) // mode type sha size
		if len(f) < 4 {
			continue
		}
		if len(tr.Entries) == MaxTreeEntries {
			tr.Truncated = true
			break
		}
		e := TreeEntry{Name: name}
		switch f[0] {
		case "040000":
			e.Type = "dir"
		case "120000":
			e.Type = "link"
		case "160000":
			e.Type = "submodule"
		default:
			e.Type = "file"
		}
		if f[3] != "-" {
			e.Size, _ = strconv.ParseInt(f[3], 10, 64)
		}
		tr.Entries = append(tr.Entries, e)
	}
	sort.SliceStable(tr.Entries, func(i, j int) bool {
		a, b := tr.Entries[i], tr.Entries[j]
		if (a.Type == "dir") != (b.Type == "dir") {
			return a.Type == "dir"
		}
		return a.Name < b.Name
	})
	return tr, nil
}

// blobSize checks that obj is a blob and returns its size.
func (r *Runner) blobSize(ctx context.Context, dir, obj string) (int64, error) {
	out, _, err := r.run(ctx, dir, 64, "cat-file", "-t", obj)
	if err != nil {
		if exitFailure(errors.Unwrap(err)) {
			return 0, ErrNotFound
		}
		return 0, err
	}
	if strings.TrimSpace(string(out)) != "blob" {
		return 0, ErrNotFound
	}
	out, _, err = r.run(ctx, dir, 64, "cat-file", "-s", obj)
	if err != nil {
		return 0, err
	}
	return strconv.ParseInt(strings.TrimSpace(string(out)), 10, 64)
}

// Blob reads a file for display.
func (r *Runner) Blob(ctx context.Context, dir, sha, path string) (Blob, error) {
	obj, err := object(sha, path)
	if err != nil {
		return Blob{}, err
	}
	if obj == sha+":" {
		return Blob{}, ErrNotFound
	}
	size, err := r.blobSize(ctx, dir, obj)
	if err != nil {
		return Blob{}, err
	}
	b := Blob{Size: size}
	out, over, err := r.run(ctx, dir, MaxTextBytes, "cat-file", "blob", obj)
	if err != nil {
		return Blob{}, err
	}
	head := out
	if len(head) > binarySniff {
		head = head[:binarySniff]
	}
	if bytes.IndexByte(head, 0) >= 0 {
		b.Binary = true
		return b, nil
	}
	// The command is killed at the cap, so a file of exactly MaxTextBytes is
	// not truncated; one byte more is.
	b.Truncated = over || size > int64(len(out))
	if b.Truncated {
		// Do not end in half a character.
		for i := 0; i < utf8.UTFMax && len(out) > 0 && !utf8.Valid(out); i++ {
			if r, n := utf8.DecodeLastRune(out); r == utf8.RuneError && n <= 1 {
				out = out[:len(out)-1]
			} else {
				break
			}
		}
	}
	b.Text = string(out)
	return b, nil
}

// WriteBlob streams a file's raw bytes to w.
func (r *Runner) WriteBlob(ctx context.Context, dir, sha, path string, w io.Writer) error {
	obj, err := object(sha, path)
	if err != nil {
		return err
	}
	if obj == sha+":" {
		return ErrNotFound
	}
	if _, err := r.blobSize(ctx, dir, obj); err != nil {
		return err
	}
	return r.stream(ctx, dir, w, "cat-file", "blob", obj)
}

// Archive streams the commit's tree as a ZIP file.
func (r *Runner) Archive(ctx context.Context, dir, sha string, w io.Writer) error {
	if err := checkSHA(sha); err != nil {
		return err
	}
	return r.stream(ctx, dir, w, "archive", "--format=zip", sha)
}

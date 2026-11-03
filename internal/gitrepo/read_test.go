package gitrepo

import (
	"bytes"
	"errors"
	"fmt"
	"os/exec"
	"sort"
	"strings"
	"testing"
	"time"
)

// importer builds fixture history with git fast-import, so file names that a
// Windows filesystem cannot hold (a double quote, a newline) still work.
type importer struct {
	buf bytes.Buffer
	n   int
}

// commit adds one commit on ref. files maps path to content; names in del
// are removed.
func (im *importer) commit(ref, msg string, files map[string]string, del ...string) {
	im.n++
	fmt.Fprintf(&im.buf, "commit %s\ncommitter T <t@t> %d +0000\ndata %d\n%s\n", ref, 1700000000+im.n, len(msg), msg)
	names := make([]string, 0, len(files))
	for p := range files {
		names = append(names, p)
	}
	sort.Strings(names)
	for _, p := range names {
		c := files[p]
		fmt.Fprintf(&im.buf, "M 100644 inline %s\ndata %d\n%s\n", quotePath(p), len(c), c)
	}
	for _, p := range del {
		fmt.Fprintf(&im.buf, "D %s\n", quotePath(p))
	}
	im.buf.WriteString("\n")
}

func quotePath(p string) string {
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`)
	return `"` + r.Replace(p) + `"`
}

func (im *importer) run(t *testing.T, c *client, dir string) {
	t.Helper()
	cmd := exec.Command("git", "-c", "user.name=t", "-c", "user.email=t@t", "fast-import", "--quiet")
	cmd.Dir = dir
	cmd.Env = c.env
	cmd.Stdin = &im.buf
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("fast-import: %v\n%s", err, out)
	}
}

func head(t *testing.T, c *client, dir, ref string) string {
	t.Helper()
	return c.mustGit(dir, "rev-parse", ref)
}

func TestEmptyRepoReads(t *testing.T) {
	r, dir := newBare(t)
	ctx := t.Context()
	if !Exists(dir) {
		t.Fatal("Exists is false")
	}
	refs, err := r.Refs(ctx, dir)
	if err != nil || len(refs) != 0 {
		t.Fatalf("Refs = %v, %v", refs, err)
	}
	if b, err := r.DefaultBranch(ctx, dir); err != nil || b != "main" {
		t.Fatalf("DefaultBranch = %q, %v", b, err)
	}
	if _, err := r.Resolve(ctx, dir, "main"); !errors.Is(err, ErrBadRef) {
		t.Fatalf("Resolve(main) = %v, want ErrBadRef", err)
	}
}

func TestRefsListsBranchesAndTagsWithDefault(t *testing.T) {
	r, dir := newBare(t)
	c := newClient(t)
	im := &importer{}
	im.commit("refs/heads/main", "first", map[string]string{"a.txt": "a"})
	im.commit("refs/heads/feature/x", "on feature", map[string]string{"b.txt": "b"})
	im.run(t, c, dir)
	main := head(t, c, dir, "refs/heads/main")
	c.mustGit(dir, "tag", "light", main)
	c.mustGit(dir, "tag", "-a", "-m", "annotated", "ann", main)
	c.mustGit(dir, "tag", "main", main) // a tag named like the default branch
	refs, err := r.Refs(t.Context(), dir)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]Ref{}
	for _, x := range refs {
		got[x.Kind+":"+x.Name] = x
	}
	if len(got) != 5 {
		t.Fatalf("refs = %+v", refs)
	}
	if b, err := r.DefaultBranch(t.Context(), dir); err != nil || b != "main" {
		t.Fatalf("DefaultBranch with a tag named main = %q, %v", b, err)
	}
	m := got["branch:main"]
	if !m.Default || m.Commit.SHA != main || m.Commit.Subject != "first" || m.Commit.Author != "T" || m.Commit.Date.IsZero() {
		t.Fatalf("main = %+v", m)
	}
	if got["branch:feature/x"].Default || got["branch:feature/x"].Commit.Subject != "on feature" {
		t.Fatalf("feature/x = %+v", got["branch:feature/x"])
	}
	for _, k := range []string{"tag:light", "tag:ann"} {
		if got[k].Commit.SHA != main || got[k].Default || got[k].Commit.Subject != "first" {
			t.Fatalf("%s = %+v", k, got[k])
		}
	}
}

func TestLogPagesBySkip(t *testing.T) {
	r, dir := newBare(t)
	c := newClient(t)
	im := &importer{}
	for i := 1; i <= 65; i++ {
		im.commit("refs/heads/main", fmt.Sprintf("commit %d", i), map[string]string{"f.txt": fmt.Sprint(i)})
	}
	im.run(t, c, dir)
	sha, err := r.Resolve(t.Context(), dir, "main")
	if err != nil {
		t.Fatal(err)
	}
	var sizes []int
	var first []string
	for skip := 0; ; skip += LogPage {
		page, err := r.Log(t.Context(), dir, sha, skip, LogPage)
		if err != nil {
			t.Fatal(err)
		}
		if len(page) == 0 {
			break
		}
		sizes = append(sizes, len(page))
		first = append(first, page[0].Subject)
	}
	if fmt.Sprint(sizes) != "[30 30 5]" || first[0] != "commit 65" || first[1] != "commit 35" || first[2] != "commit 5" {
		t.Fatalf("sizes %v first %v", sizes, first)
	}
}

func TestCommitDetailListsChangedFiles(t *testing.T) {
	r, dir := newBare(t)
	c := newClient(t)
	im := &importer{}
	im.commit("refs/heads/main", "base", map[string]string{"keep.txt": "1\n", "gone.txt": "x\ny\n", "mod.txt": "a\nb\nc\n"})
	im.commit("refs/heads/main", "change things\n\nbody line", map[string]string{
		"new.txt": "l1\nl2\nl3\n", "mod.txt": "a\nB\nc\nd\n", "bin.dat": "ab\x00cd",
	}, "gone.txt")
	im.run(t, c, dir)
	sha, _ := r.Resolve(t.Context(), dir, "main")
	d, err := r.Commit(t.Context(), dir, sha)
	if err != nil {
		t.Fatal(err)
	}
	if d.SHA != sha || d.Subject != "change things" || d.Message != "change things\n\nbody line" || d.AuthorEmail != "t@t" || d.Committer != "T" || len(d.Parents) != 1 || d.CommitterDate.IsZero() {
		t.Fatalf("detail = %+v", d)
	}
	want := map[string]ChangedFile{
		"new.txt":  {"new.txt", "A", 3, 0},
		"gone.txt": {"gone.txt", "D", 0, 2},
		"mod.txt":  {"mod.txt", "M", 2, 1},
		"bin.dat":  {"bin.dat", "A", -1, -1},
	}
	if len(d.Files) != len(want) || d.FilesTruncated {
		t.Fatalf("files = %+v", d.Files)
	}
	for _, f := range d.Files {
		if want[f.Path] != f {
			t.Fatalf("file %+v, want %+v", f, want[f.Path])
		}
	}

	// A root commit lists everything as added.
	root, _ := r.Log(t.Context(), dir, sha, 1, 1)
	rd, err := r.Commit(t.Context(), dir, root[0].SHA)
	if err != nil || len(rd.Files) != 3 || len(rd.Parents) != 0 {
		t.Fatalf("root = %+v, %v", rd, err)
	}

	big := &importer{}
	files := map[string]string{}
	for i := 0; i < 301; i++ {
		files[fmt.Sprintf("f%03d.txt", i)] = "x\n"
	}
	big.commit("refs/heads/big", "many", files)
	big.run(t, c, dir)
	bsha, _ := r.Resolve(t.Context(), dir, "big")
	bd, err := r.Commit(t.Context(), dir, bsha)
	if err != nil {
		t.Fatal(err)
	}
	if !bd.FilesTruncated || len(bd.Files) != MaxChangedFiles {
		t.Fatalf("truncated=%v files=%d", bd.FilesTruncated, len(bd.Files))
	}
}

func TestOddNamesAndRefs(t *testing.T) {
	r, dir := newBare(t)
	c := newClient(t)
	odd := "a\"b\nc.txt"
	// Plumbing: fast-import refuses a newline in a name, and a Windows
	// filesystem cannot hold one.
	subTree := plumb(t, c, dir, blobEntry(t, c, dir, "in side.txt", "deep"), "mktree", "-z")
	tree := plumb(t, c, dir, blobEntry(t, c, dir, odd, "odd content")+blobEntry(t, c, dir, "ümlaut.md", "umlaut")+blobEntry(t, c, dir, "x:", "colon")+"040000 tree "+subTree+"\tdir\x00", "mktree", "-z")
	commitA := plumb(t, c, dir, "", "commit-tree", tree, "-m", "odd")
	c.mustGit(dir, "update-ref", "refs/heads/main", commitA)
	im := &importer{}
	im.commit("refs/heads/feature/x", "fx", map[string]string{"x.txt": "x"})
	im.run(t, c, dir)
	ctx := t.Context()
	sha, _ := r.Resolve(ctx, dir, "main")
	tr, err := r.Tree(ctx, dir, sha, "")
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]TreeEntry{}
	for _, e := range tr.Entries {
		names[e.Name] = e
	}
	if names[odd].Type != "file" || names["ümlaut.md"].Type != "file" || names["dir"].Type != "dir" || len(names) != 4 {
		t.Fatalf("entries = %+v", tr.Entries)
	}
	for name, want := range map[string]string{"x:": "colon", odd: "odd content", "ümlaut.md": "umlaut", "dir/in side.txt": "deep"} {
		b, err := r.Blob(ctx, dir, sha, name)
		if err != nil || b.Text != want || b.Binary || b.Size != int64(len(want)) {
			t.Fatalf("Blob(%q) = %+v, %v", name, b, err)
		}
		var buf bytes.Buffer
		if err := r.WriteBlob(ctx, dir, sha, name, &buf); err != nil || buf.String() != want {
			t.Fatalf("WriteBlob(%q) = %q, %v", name, buf.String(), err)
		}
	}
	sub, err := r.Tree(ctx, dir, sha, "dir/")
	if err != nil || len(sub.Entries) != 1 || sub.Entries[0].Name != "in side.txt" {
		t.Fatalf("subtree = %+v, %v", sub, err)
	}
	if _, err := r.Blob(ctx, dir, sha, "nope.txt"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing blob = %v", err)
	}
	if _, err := r.Tree(ctx, dir, sha, "ümlaut.md"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("tree of a file = %v", err)
	}
	if _, err := r.Blob(ctx, dir, sha, "dir"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("blob of a dir = %v", err)
	}

	if fx, err := r.Resolve(ctx, dir, "feature/x"); err != nil || fx != head(t, c, dir, "refs/heads/feature/x") {
		t.Fatalf("feature/x = %q, %v", fx, err)
	}

	// Tag v1 on A, branch v1 on B: the branch wins, the full name reaches the tag.
	a := head(t, c, dir, "refs/heads/main")
	b := head(t, c, dir, "refs/heads/feature/x")
	c.mustGit(dir, "tag", "v1", a)
	c.mustGit(dir, "update-ref", "refs/heads/v1", b)
	if got, err := r.Resolve(ctx, dir, "v1"); err != nil || got != b {
		t.Fatalf("Resolve(v1) = %q, %v; want branch %q", got, err, b)
	}
	if got, err := r.Resolve(ctx, dir, "refs/tags/v1"); err != nil || got != a {
		t.Fatalf("Resolve(refs/tags/v1) = %q, %v; want tag %q", got, err, a)
	}
	if got, err := r.Resolve(ctx, dir, a[:10]); err != nil || got != a {
		t.Fatalf("Resolve(abbrev) = %q, %v", got, err)
	}
	if got, err := r.Resolve(ctx, dir, a); err != nil || got != a {
		t.Fatalf("Resolve(full) = %q, %v", got, err)
	}
}

func TestResolveRefusesOptions(t *testing.T) {
	r, dir := newBare(t)
	c := newClient(t)
	im := &importer{}
	im.commit("refs/heads/main", "one", map[string]string{"a": "1"})
	im.commit("refs/heads/main", "two", map[string]string{"a": "2"})
	im.run(t, c, dir)
	tip := head(t, c, dir, "refs/heads/main")
	c.mustGit(dir, "tag", "v1", tip)
	for _, ref := range []string{"--upload-pack=x", "-x", "a\x00b", "main\nx", "",
		":/x", ":/two", "main@{0}", "main~1", "main^", "HEAD", "refs/heads/main~1", "v1^{tree}", "main:a", "refs/../x"} {
		if _, err := r.Resolve(t.Context(), dir, ref); !errors.Is(err, ErrBadRef) {
			t.Errorf("Resolve(%q) = %v, want ErrBadRef", ref, err)
		}
	}
	for _, ref := range []string{"main", "v1", "refs/tags/v1", "refs/heads/main", tip[:7], strings.ToUpper(tip[:12]), tip} {
		if got, err := r.Resolve(t.Context(), dir, ref); err != nil || got != tip {
			t.Errorf("Resolve(%q) = %q, %v; want %s", ref, got, err, tip)
		}
	}
	// A sha argument that is not a hex object name never reaches git.
	if _, err := r.Log(t.Context(), dir, "--output=x", 0, 5); !errors.Is(err, ErrBadRef) {
		t.Errorf("Log with option sha = %v", err)
	}
	if err := r.Archive(t.Context(), dir, "-x", &bytes.Buffer{}); !errors.Is(err, ErrBadRef) {
		t.Errorf("Archive with option sha = %v", err)
	}
}

func TestTreeRefusesTraversal(t *testing.T) {
	r, dir := newBare(t)
	c := newClient(t)
	im := &importer{}
	im.commit("refs/heads/main", "x", map[string]string{"a.txt": "a", "d/b.txt": "b"})
	im.run(t, c, dir)
	sha, _ := r.Resolve(t.Context(), dir, "main")
	for _, p := range []string{"../x", "/etc", "d/../../x", "d/..", `d\b.txt`, "a\x00b"} {
		if _, err := r.Tree(t.Context(), dir, sha, p); !errors.Is(err, ErrNotFound) {
			t.Errorf("Tree(%q) = %v, want ErrNotFound", p, err)
		}
		if _, err := r.Blob(t.Context(), dir, sha, p); !errors.Is(err, ErrNotFound) {
			t.Errorf("Blob(%q) = %v, want ErrNotFound", p, err)
		}
	}
}

func TestBigTreeAndBinary(t *testing.T) {
	r, dir := newBare(t)
	c := newClient(t)
	files := map[string]string{
		"bin.dat":  "head\x00tail",
		"big.txt":  strings.Repeat("0123456789abcdef", 600*1024/16),
		"small.md": "hello",
	}
	for i := 0; i < 5001; i++ {
		files[fmt.Sprintf("many/f%05d", i)] = "x"
	}
	im := &importer{}
	im.commit("refs/heads/main", "big", files)
	im.run(t, c, dir)
	ctx := t.Context()
	sha, _ := r.Resolve(ctx, dir, "main")
	tr, err := r.Tree(ctx, dir, sha, "many")
	if err != nil || len(tr.Entries) != MaxTreeEntries || !tr.Truncated {
		t.Fatalf("tree: %d entries, truncated=%v, %v", len(tr.Entries), tr.Truncated, err)
	}
	root, _ := r.Tree(ctx, dir, sha, "")
	if root.Truncated || len(root.Entries) != 4 {
		t.Fatalf("root = %+v", root)
	}
	bin, err := r.Blob(ctx, dir, sha, "bin.dat")
	if err != nil || !bin.Binary || bin.Text != "" || bin.Size != 9 {
		t.Fatalf("binary = %+v, %v", bin, err)
	}
	big, err := r.Blob(ctx, dir, sha, "big.txt")
	if err != nil || !big.Truncated || len(big.Text) != MaxTextBytes || big.Size != 600*1024 || big.Binary {
		t.Fatalf("big: truncated=%v len=%d size=%d, %v", big.Truncated, len(big.Text), big.Size, err)
	}
}

func TestArchiveIsZip(t *testing.T) {
	r, dir := newBare(t)
	c := newClient(t)
	im := &importer{}
	im.commit("refs/heads/main", "x", map[string]string{"hello.txt": "hi", "d/inner.txt": "in"})
	im.run(t, c, dir)
	sha, _ := r.Resolve(t.Context(), dir, "main")
	var buf bytes.Buffer
	if err := r.Archive(t.Context(), dir, sha, &buf); err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(buf.Bytes(), []byte("PK\x03\x04")) || !bytes.Contains(buf.Bytes(), []byte("hello.txt")) || !bytes.Contains(buf.Bytes(), []byte("d/inner.txt")) {
		t.Fatalf("not a zip with the files: %d bytes", buf.Len())
	}
}

// plumb runs git in dir with stdin and returns trimmed stdout.
func plumb(t *testing.T, c *client, dir, stdin string, args ...string) string {
	t.Helper()
	full := append([]string{"-c", "user.name=t", "-c", "user.email=t@t"}, args...)
	cmd := exec.Command("git", full...)
	cmd.Dir = dir
	cmd.Env = c.env
	cmd.Stdin = strings.NewReader(stdin)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// blobEntry writes a blob and returns its mktree line.
func blobEntry(t *testing.T, c *client, dir, name, content string) string {
	sha := plumb(t, c, dir, content, "hash-object", "-w", "--stdin")
	return "100644 blob " + sha + "\t" + name + "\x00"
}

func TestStreamingOutlivesQueries(t *testing.T) {
	if StreamTimeout != 10*time.Minute || StreamTimeout <= ReadTimeout {
		t.Fatalf("StreamTimeout = %v, ReadTimeout = %v", StreamTimeout, ReadTimeout)
	}
}

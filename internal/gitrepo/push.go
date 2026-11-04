package gitrepo

import (
	"context"
	"errors"
	"os/exec"
	"strings"
)

// What a push did. Git refuses a ref update in-band and still exits 0, so
// the caller learns what a push changed by comparing the repository's refs
// before and after it, never from git's exit status alone.

// RefTargets maps every ref of dir to the object it points at (for an
// annotated tag, the tag object). An empty repository has none.
func (r *Runner) RefTargets(ctx context.Context, dir string) (map[string]string, error) {
	out, over, err := r.run(ctx, dir, maxOutput, "for-each-ref", "--format=%(objectname) %(refname)")
	if err != nil {
		return nil, err
	}
	if over {
		return nil, errors.New("gitrepo: too many refs to list")
	}
	refs := map[string]string{}
	for _, line := range strings.Split(string(out), "\n") {
		if sha, ref, ok := strings.Cut(line, " "); ok && ref != "" {
			refs[ref] = sha
		}
	}
	return refs, nil
}

// IsAncestor reports whether commit old is an ancestor of commit new — a
// fast-forward from old to new. Both must be full object names. A commit
// dir does not hold is an error, never false.
func (r *Runner) IsAncestor(ctx context.Context, dir, old, new string) (bool, error) {
	if checkSHA(old) != nil || checkSHA(new) != nil {
		return false, ErrBadRef
	}
	_, _, err := r.run(ctx, dir, 4096, "merge-base", "--is-ancestor", old, new)
	if err == nil {
		return true, nil
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) && ee.ExitCode() == 1 {
		return false, nil
	}
	return false, err
}

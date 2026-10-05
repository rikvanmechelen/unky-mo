package gitfiles

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"

	moexec "github.com/rvanmech/unky-mo/internal/exec"
)

// The Overview of commits selected in the Git log tab. Only consecutive
// commits are supported: a selection the Overview can show as the diff
// between two real commits, the one just before the selection (Base) and
// its newest commit (Head).

// MaxSelection caps how many commits one selection may name.
const MaxSelection = 200

// Selection is a resolved run of consecutive commits.
type Selection struct {
	Root string // the checkout's top level
	Base string // the commit just before the selection
	Head string // the newest selected commit
}

// ErrBadSelection is returned for an empty selection, one over
// MaxSelection, or one with an id that isn't a full hex object id.
var ErrBadSelection = errors.New("bad commit selection")

// SelectionError explains why valid commits aren't consecutive.
type SelectionError struct {
	// Reason is "gap" (Commit, between two selected commits, isn't
	// selected), "merge" (the selected merge Commit brings in Other, which
	// isn't selected), "root" (Commit has no parent to compare with) or
	// "heads" (Commit and Other are on different branches).
	Reason string
	Commit string
	Other  string
}

func (e *SelectionError) Error() string {
	c, o := short(e.Commit), short(e.Other)
	switch e.Reason {
	case "gap":
		return fmt.Sprintf("not consecutive: %s is between selected commits but isn't selected", c)
	case "merge":
		return fmt.Sprintf("the merge %s brings in %s, which isn't selected", c, o)
	case "root":
		return fmt.Sprintf("%s is the first commit: there's nothing before it to compare with", c)
	default:
		return fmt.Sprintf("%s and %s are on different branches", c, o)
	}
}

func short(hash string) string {
	if len(hash) > 7 {
		return hash[:7]
	}
	return hash
}

// ResolveSelection checks that hashes (full commit ids, in any order) are
// consecutive commits of the repository containing dir, and returns the
// commits to compare. Consecutive means: one newest commit, exactly one
// commit just before the selection, and nothing in between left out.
func ResolveSelection(ctx context.Context, cmd moexec.Commander, dir string, hashes []string) (*Selection, error) {
	sel := map[string]bool{}
	var ids []string
	for _, h := range hashes {
		if !hashRe.MatchString(h) {
			return nil, ErrBadSelection
		}
		if !sel[h] {
			sel[h] = true
			ids = append(ids, h)
		}
	}
	if len(ids) == 0 || len(ids) > MaxSelection {
		return nil, ErrBadSelection
	}
	root, err := Root(ctx, cmd, dir)
	if err != nil {
		return nil, err
	}
	if err := allCommits(ctx, cmd, root, ids); err != nil {
		return nil, err
	}

	parents, err := selectionParents(ctx, cmd, root, ids)
	if err != nil {
		return nil, err
	}
	isParent := map[string]bool{}
	for _, id := range ids {
		if len(parents[id]) == 0 {
			return nil, &SelectionError{Reason: "root", Commit: id}
		}
		for _, p := range parents[id] {
			isParent[p] = true
		}
	}

	// The newest commit: of the commits no other selected commit builds on,
	// the one the others lead to. Several left means several branches.
	var tips []string
	for _, id := range ids {
		if !isParent[id] {
			tips = append(tips, id)
		}
	}
	head := tips[0]
	if len(tips) > 1 {
		out, err := git(ctx, cmd, root, append([]string{"merge-base", "--independent"}, tips...)...)
		if err != nil {
			return nil, fmt.Errorf("git merge-base: %w", err)
		}
		heads := strings.Fields(string(out))
		if len(heads) != 1 {
			if len(heads) < 2 {
				return nil, fmt.Errorf("git merge-base --independent: no answer")
			}
			return nil, &SelectionError{Reason: "heads", Commit: heads[0], Other: heads[1]}
		}
		head = heads[0]
	}

	// The boundary: parents outside the selection.
	var boundary []string
	seen := map[string]bool{}
	for _, id := range ids {
		for _, p := range parents[id] {
			if !sel[p] && !seen[p] {
				seen[p] = true
				boundary = append(boundary, p)
			}
		}
	}

	// Everything head has that the boundary hasn't is always part of the
	// selection. A selected commit missing from it sits behind a boundary
	// commit that descends from it: a gap (also when that commit is on a
	// side branch forking off the selection and merged back into it).
	reach, err := revList(ctx, cmd, root, append(append([]string{head, "--not"}, boundary...), "--")...)
	if err != nil {
		return nil, err
	}
	reached := map[string]bool{}
	for _, c := range reach {
		reached[c] = true
	}
	for _, id := range ids {
		if reached[id] {
			continue
		}
		between, err := revList(ctx, cmd, root, "--topo-order", "--ancestry-path", id+".."+head, "--")
		if err != nil {
			return nil, err
		}
		// Children before parents: the last one not selected is the
		// oldest missing.
		missing := ""
		for _, c := range between {
			if !sel[c] {
				missing = c
			}
		}
		if missing == "" {
			return nil, fmt.Errorf("selection: no missing commit between %s and %s", short(id), short(head))
		}
		return nil, &SelectionError{Reason: "gap", Commit: missing}
	}

	// A run without merges has one boundary; a second one is a merge's
	// other side, which forked off before the selection started.
	if len(boundary) > 1 {
		for _, id := range ids {
			ps := parents[id]
			if len(ps) < 2 {
				continue
			}
			for _, p := range ps {
				if !sel[p] {
					return nil, &SelectionError{Reason: "merge", Commit: id, Other: p}
				}
			}
		}
		return nil, fmt.Errorf("selection: %d boundary commits without a merge", len(boundary))
	}
	return &Selection{Root: root, Base: boundary[0], Head: head}, nil
}

// allCommits checks that every id names a commit (not a tree, blob or tag
// object, which rev-list would accept or peel).
func allCommits(ctx context.Context, cmd moexec.Commander, root string, ids []string) error {
	stdout, stderr, err := cmd.OutputStdin(ctx, root, []byte(strings.Join(ids, "\n")+"\n"), "git", "cat-file", "--batch-check=%(objectname) %(objecttype)")
	if err != nil {
		return fmt.Errorf("git cat-file --batch-check: %v: %s", err, bytes.TrimSpace(stderr))
	}
	lines := strings.Split(strings.TrimSpace(string(stdout)), "\n")
	if len(lines) != len(ids) {
		return ErrUnknownCommit
	}
	for i, l := range lines {
		if l != ids[i]+" commit" {
			return ErrUnknownCommit
		}
	}
	return nil
}

// selectionParents returns each commit's parents.
func selectionParents(ctx context.Context, cmd moexec.Commander, root string, ids []string) (map[string][]string, error) {
	lines, err := git(ctx, cmd, root, append(append([]string{"rev-list", "--no-walk=unsorted", "--parents"}, ids...), "--")...)
	if err != nil {
		return nil, fmt.Errorf("git rev-list: %w", err)
	}
	parents := map[string][]string{}
	for _, l := range strings.Split(string(lines), "\n") {
		if f := strings.Fields(l); len(f) > 0 {
			parents[f[0]] = f[1:]
		}
	}
	for _, id := range ids {
		if _, ok := parents[id]; !ok {
			return nil, ErrUnknownCommit
		}
	}
	return parents, nil
}

func revList(ctx context.Context, cmd moexec.Commander, root string, args ...string) ([]string, error) {
	out, err := git(ctx, cmd, root, append([]string{"rev-list"}, args...)...)
	if err != nil {
		return nil, fmt.Errorf("git rev-list: %w", err)
	}
	return strings.Fields(string(out)), nil
}

// GetOverviewRange reads the change from commit base to commit head (full
// commit ids, normally a ResolveSelection answer) in the repo at root.
// Nothing is read from the working tree.
func GetOverviewRange(ctx context.Context, cmd moexec.Commander, root, base, head string) (*Overview, error) {
	for _, h := range []string{base, head} {
		if !hashRe.MatchString(h) || gitLine(ctx, cmd, root, "cat-file", "-t", h) != "commit" {
			return nil, ErrUnknownCommit
		}
	}
	o := &Overview{Root: root, Mode: ModeCommits, MergeBase: base, Rev: base, Head: head, Files: []OverviewFile{}, Kinds: map[Kind]KindTotal{}, Areas: []string{}}
	o.Branch = CurrentBranch(ctx, cmd, root)
	return overviewBetween(ctx, cmd, o, root, base, head)
}

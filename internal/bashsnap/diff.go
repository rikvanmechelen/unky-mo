package bashsnap

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// MaxPatch caps the unified diff Diff returns.
const MaxPatch = 2 << 20

// FileStat is one file a Bash call changed. Binary files have no counts.
type FileStat struct {
	Path    string `json:"path"`
	OldPath string `json:"oldPath,omitempty"`
	Added   int    `json:"added"`
	Removed int    `json:"removed"`
	Binary  bool   `json:"binary,omitempty"`
}

// Change is one Bash call's change: the checkout it ran in, when it ran,
// and its files.
type Change struct {
	Root  string     `json:"-"`
	Start time.Time  `json:"start"`
	End   time.Time  `json:"end"`
	Files []FileStat `json:"files"`
}

// Diff is a Bash call's change as a unified diff (git's), with its files.
type Diff struct {
	Files     []FileStat `json:"files"`
	Patch     string     `json:"patch"`
	Truncated bool       `json:"truncated,omitempty"`
}

// diffEnv lets git read both snapshots' objects and the repo's.
func (s *Store) diffEnv(ctx context.Context, rec *Record) ([]string, error) {
	if !rec.Changed() || !s.validRecord(rec) {
		return nil, ErrNoRecord
	}
	out, _, err := s.cmd.Output(ctx, rec.Root, "git", "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return nil, fmt.Errorf("checkout %s is gone: %w", rec.Root, err)
	}
	common := strings.TrimSpace(string(out))
	alternates := []string{filepath.Join(common, "objects")}
	if rec.PreObjects != rec.PostObjects {
		alternates = append(alternates, rec.PreObjects)
	}
	return []string{
		"GIT_OBJECT_DIRECTORY=" + rec.PostObjects,
		"GIT_ALTERNATE_OBJECT_DIRECTORIES=" + strings.Join(alternates, string(filepath.ListSeparator)),
	}, nil
}

// Stat lists the files rec changed.
func (s *Store) Stat(ctx context.Context, rec *Record) ([]FileStat, error) {
	env, err := s.diffEnv(ctx, rec)
	if err != nil {
		return nil, err
	}
	out, stderr, err := s.cmd.Output(ctx, rec.Root, "env", append(gitArgs(env), "diff", "--no-ext-diff", "--numstat", "-z", "-M", rec.Pre, rec.Post)...)
	if err != nil {
		return nil, gitErr("diff", stderr, err)
	}
	return parseNumstat(out)
}

// Diff reads rec's change: its files and the unified diff, cut at MaxPatch.
func (s *Store) Diff(ctx context.Context, rec *Record) (*Diff, error) {
	files, err := s.Stat(ctx, rec)
	if err != nil {
		return nil, err
	}
	env, err := s.diffEnv(ctx, rec)
	if err != nil {
		return nil, err
	}
	out, stderr, err := s.cmd.Output(ctx, rec.Root, "env", append(gitArgs(env), "diff", "--no-ext-diff", "--no-color", "-M", "-U3", rec.Pre, rec.Post)...)
	if err != nil {
		return nil, gitErr("diff", stderr, err)
	}
	d := &Diff{Files: files}
	if len(out) > MaxPatch {
		// Cut at a line end, so the last hunk is short rather than torn.
		cut := strings.LastIndexByte(string(out[:MaxPatch]), '\n')
		out, d.Truncated = out[:cut+1], true
	}
	d.Patch = string(out)
	return d, nil
}

var errNumstat = errors.New("malformed numstat")

// parseNumstat reads `git diff --numstat -z`: "added\tremoved\tpath\0", or
// for a rename "added\tremoved\t\0old\0new\0"; binary counts are "-".
func parseNumstat(out []byte) ([]FileStat, error) {
	fields := strings.Split(string(out), "\x00")
	var files []FileStat
	for i := 0; i < len(fields); i++ {
		if fields[i] == "" {
			continue
		}
		parts := strings.SplitN(fields[i], "\t", 3)
		if len(parts) != 3 {
			return nil, errNumstat
		}
		f := FileStat{Path: parts[2]}
		if parts[0] == "-" && parts[1] == "-" {
			f.Binary = true
		} else {
			a, err1 := strconv.Atoi(parts[0])
			r, err2 := strconv.Atoi(parts[1])
			if err1 != nil || err2 != nil {
				return nil, errNumstat
			}
			f.Added, f.Removed = a, r
		}
		if f.Path == "" {
			if i+2 >= len(fields) || fields[i+1] == "" || fields[i+2] == "" {
				return nil, errNumstat
			}
			f.OldPath, f.Path = fields[i+1], fields[i+2]
			i += 2
		}
		files = append(files, f)
	}
	return files, nil
}

// Package bashsnap records what each Bash call of a Claude session changed
// in its git checkout. Claude Code's PreToolUse and PostToolUse hooks run
// `mo snapshot pre|post`, which writes the working tree (tracked files and
// untracked ones that aren't ignored) as a git tree before and after the
// command; the web chat view diffs the two.
//
// Snapshots never touch the repository: each one is built in a private index
// (per session, root and day) whose new objects go to unky-mo's own object
// directory under the user's cache dir, with the repo's objects as an
// alternate. The repo's index, refs and object store are left as they are.
package bashsnap

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	moexec "github.com/rvanmech/unky-mo/internal/exec"
)

// MaxUntrackedSize is the largest untracked file a snapshot reads; bigger
// ones (build output nobody ignored) are left out rather than hashed and
// copied on every Bash call.
const MaxUntrackedSize = 2 << 20

// Retention is how long records are kept. Object buckets live a day longer,
// since a record can point into the previous day's bucket.
const Retention = 24 * time.Hour

var (
	sessionRe = regexp.MustCompile(`^[0-9A-Za-z][0-9A-Za-z-]{0,63}$`)
	toolUseRe = regexp.MustCompile(`^toolu_[0-9A-Za-z_]{1,64}$`)
	treeRe    = regexp.MustCompile(`^[0-9a-f]{40}([0-9a-f]{24})?$`)
	dayRe     = regexp.MustCompile(`^\d{8}$`)
)

// ValidSession and ValidToolUse check ids before they become file names.
func ValidSession(id string) bool { return sessionRe.MatchString(id) }
func ValidToolUse(id string) bool { return toolUseRe.MatchString(id) }

// Record is one Bash call's snapshots. A record exists only while the
// command runs (Post empty) or when it changed something; an unchanged
// command's record is removed by Post.
type Record struct {
	Root        string    `json:"root"`
	Pre         string    `json:"pre"`
	Post        string    `json:"post,omitempty"`
	PreObjects  string    `json:"preObjects"`
	PostObjects string    `json:"postObjects,omitempty"`
	Start       time.Time `json:"start"`
	End         time.Time `json:"end,omitzero"`
	// Background: a run_in_background command. Its PostToolUse fires at
	// launch, so the record keeps no diff.
	Background bool `json:"background,omitempty"`
}

// Changed reports whether the record holds a finished, non-empty change.
func (r *Record) Changed() bool {
	return r != nil && !r.Background && r.Post != "" && r.Post != r.Pre
}

// Store keeps snapshots under Dir: buckets/<day>/objects (object store),
// buckets/<day>/index/<session>-<root hash> (private indexes) and
// records/<session>/<tool_use_id>.json.
type Store struct {
	Dir string
	cmd moexec.Commander
	now func() time.Time
}

// NewStore returns the store under the user's cache directory
// (~/.cache/unky-mo/bash-snapshots on Linux).
func NewStore(cmd moexec.Commander) (*Store, error) {
	cache, err := os.UserCacheDir()
	if err != nil {
		return nil, err
	}
	return newStore(filepath.Join(cache, "unky-mo", "bash-snapshots"), cmd, time.Now), nil
}

func newStore(dir string, cmd moexec.Commander, now func() time.Time) *Store {
	return &Store{Dir: dir, cmd: cmd, now: now}
}

// HookInput is the part of Claude Code's hook payload a snapshot needs.
type HookInput struct {
	SessionID string `json:"session_id"`
	Cwd       string `json:"cwd"`
	ToolName  string `json:"tool_name"`
	ToolUseID string `json:"tool_use_id"`
	ToolInput struct {
		RunInBackground bool `json:"run_in_background"`
	} `json:"tool_input"`
}

// ErrSkip means the hook payload isn't a Bash call this store records.
var ErrSkip = errors.New("not a recordable Bash call")

// Hook handles one hook payload: phase "pre" snapshots before the command,
// "post" after it. Anything that isn't a Bash call in a git checkout is
// ErrSkip.
func (s *Store) Hook(ctx context.Context, phase string, payload io.Reader) error {
	var in HookInput
	if err := json.NewDecoder(io.LimitReader(payload, 4<<20)).Decode(&in); err != nil {
		return fmt.Errorf("%w: %v", ErrSkip, err)
	}
	if in.ToolName != "Bash" || !ValidSession(in.SessionID) || !ValidToolUse(in.ToolUseID) || !filepath.IsAbs(in.Cwd) {
		return ErrSkip
	}
	switch phase {
	case "pre":
		return s.Pre(ctx, in.SessionID, in.ToolUseID, in.Cwd, in.ToolInput.RunInBackground)
	case "post":
		return s.Post(ctx, in.SessionID, in.ToolUseID)
	}
	return fmt.Errorf("unknown phase %q", phase)
}

// Pre snapshots cwd's checkout before a Bash call and starts its record.
func (s *Store) Pre(ctx context.Context, session, toolUse, cwd string, background bool) error {
	start := s.now()
	if background {
		root, err := s.toplevel(ctx, cwd)
		if err != nil {
			return err
		}
		return s.write(session, toolUse, &Record{Root: root, Start: start, Background: true})
	}
	snap, err := s.Take(ctx, session, cwd)
	if err != nil {
		return err
	}
	return s.write(session, toolUse, &Record{Root: snap.Root, Pre: snap.Tree, PreObjects: snap.Objects, Start: start})
}

// Post snapshots the same checkout after the call. A call that changed
// nothing has its record removed.
func (s *Store) Post(ctx context.Context, session, toolUse string) error {
	rec, err := s.Record(session, toolUse)
	if err != nil {
		return ErrSkip // no pre snapshot (it failed or timed out)
	}
	if rec.Background || rec.Post != "" {
		return nil
	}
	snap, err := s.Take(ctx, session, rec.Root)
	if err != nil {
		return err
	}
	if snap.Tree == rec.Pre {
		return os.Remove(s.recordPath(session, toolUse))
	}
	rec.Post, rec.PostObjects, rec.End = snap.Tree, snap.Objects, s.now()
	return s.write(session, toolUse, rec)
}

// Snapshot is a working tree written as a git tree.
type Snapshot struct {
	Root    string // the checkout's top level
	Tree    string
	Objects string // the object directory new objects went to
}

// Take writes cwd's checkout as a tree: every tracked file and every
// untracked, non-ignored file up to MaxUntrackedSize.
func (s *Store) Take(ctx context.Context, session, cwd string) (*Snapshot, error) {
	out, _, err := s.cmd.Output(ctx, cwd, "git", "rev-parse", "--path-format=absolute", "--show-toplevel", "--git-path", "index", "--git-common-dir")
	if err != nil {
		return nil, fmt.Errorf("%w: not a git checkout", ErrSkip)
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(lines) != 3 {
		return nil, fmt.Errorf("%w: unexpected rev-parse output", ErrSkip)
	}
	root, realIndex, common := lines[0], lines[1], lines[2]

	day := s.now().UTC().Format("20060102")
	bucket := filepath.Join(s.Dir, "buckets", day)
	objects := filepath.Join(bucket, "objects")
	index := filepath.Join(bucket, "index", session+"-"+shortHash(root))
	if err := os.MkdirAll(objects, 0o700); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(index), 0o700); err != nil {
		return nil, err
	}
	// Seed a new private index from the repo's, so its stat cache saves
	// rehashing every tracked file the first time.
	if _, err := os.Stat(index); errors.Is(err, os.ErrNotExist) {
		if data, err := os.ReadFile(realIndex); err == nil {
			_ = writeAtomic(index, data)
		}
	}
	env := []string{
		"GIT_INDEX_FILE=" + index,
		"GIT_OBJECT_DIRECTORY=" + objects,
		"GIT_ALTERNATE_OBJECT_DIRECTORIES=" + filepath.Join(common, "objects"),
	}

	specs := []string{"."}
	if big, err := s.bigUntracked(ctx, root, env); err == nil {
		for _, p := range big {
			specs = append(specs, ":(exclude,literal)"+p)
		}
	}
	addArgs := append(gitArgs(env), "add", "-A", "--ignore-errors", "--pathspec-from-file=-", "--pathspec-file-nul")
	if err := s.locked(ctx, func() error {
		_, stderr, err := s.cmd.OutputStdin(ctx, root, []byte(strings.Join(specs, "\x00")), "env", addArgs...)
		return gitErr("add", stderr, err)
	}); err != nil {
		return nil, err
	}
	var tree string
	if err := s.locked(ctx, func() error {
		out, stderr, err := s.cmd.Output(ctx, root, "env", append(gitArgs(env), "write-tree", "--missing-ok")...)
		tree = strings.TrimSpace(string(out))
		return gitErr("write-tree", stderr, err)
	}); err != nil {
		return nil, err
	}
	if !treeRe.MatchString(tree) {
		return nil, fmt.Errorf("write-tree: unexpected output %q", tree)
	}
	return &Snapshot{Root: root, Tree: tree, Objects: objects}, nil
}

// gitArgs runs git through env(1) with the snapshot's environment (the
// Commander seam has no environment of its own). Filters are switched off
// so nothing runs on the files (git-lfs would otherwise store each version
// in the repo's .git/lfs), and paths aren't quoted.
func gitArgs(env []string) []string {
	return append(append([]string{}, env...), "git",
		"-c", "core.quotePath=false",
		"-c", "filter.lfs.process=", "-c", "filter.lfs.clean=cat", "-c", "filter.lfs.required=false",
		"-c", "core.fsmonitor=false",
		"-c", "gc.auto=0")
}

// bigUntracked lists untracked, non-ignored files over MaxUntrackedSize.
func (s *Store) bigUntracked(ctx context.Context, root string, env []string) ([]string, error) {
	out, _, err := s.cmd.Output(ctx, root, "env", append(gitArgs(env), "ls-files", "-z", "--others", "--exclude-standard")...)
	if err != nil {
		return nil, err
	}
	var big []string
	for _, p := range strings.Split(string(out), "\x00") {
		if p == "" {
			continue
		}
		if fi, err := os.Lstat(filepath.Join(root, p)); err == nil && fi.Mode().IsRegular() && fi.Size() > MaxUntrackedSize {
			big = append(big, p)
		}
	}
	return big, nil
}

// locked retries fn while another snapshot of the same session holds the
// private index's lock (parallel Bash calls).
func (s *Store) locked(ctx context.Context, fn func() error) error {
	var err error
	for wait := 20 * time.Millisecond; ; wait *= 2 {
		err = fn()
		if err == nil || !strings.Contains(err.Error(), ".lock") || wait > 2*time.Second {
			return err
		}
		select {
		case <-ctx.Done():
			return err
		case <-time.After(wait):
		}
	}
}

func gitErr(what string, stderr []byte, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("git %s: %v: %s", what, err, strings.TrimSpace(string(stderr)))
}

func (s *Store) toplevel(ctx context.Context, cwd string) (string, error) {
	out, _, err := s.cmd.Output(ctx, cwd, "git", "rev-parse", "--show-toplevel")
	if err != nil {
		return "", fmt.Errorf("%w: not a git checkout", ErrSkip)
	}
	return strings.TrimSpace(string(out)), nil
}

func shortHash(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:6])
}

func (s *Store) recordPath(session, toolUse string) string {
	return filepath.Join(s.Dir, "records", session, toolUse+".json")
}

func (s *Store) write(session, toolUse string, rec *Record) error {
	data, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	path := s.recordPath(session, toolUse)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return writeAtomic(path, data)
}

func writeAtomic(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	return nil
}

// ErrNoRecord is a tool use without a (valid) record.
var ErrNoRecord = errors.New("no snapshot record")

// Record reads one Bash call's record. Its trees and object directories are
// checked, since they reach git later.
func (s *Store) Record(session, toolUse string) (*Record, error) {
	if !ValidSession(session) || !ValidToolUse(toolUse) {
		return nil, ErrNoRecord
	}
	data, err := os.ReadFile(s.recordPath(session, toolUse))
	if err != nil {
		return nil, ErrNoRecord
	}
	var rec Record
	if err := json.Unmarshal(data, &rec); err != nil || !s.validRecord(&rec) {
		return nil, ErrNoRecord
	}
	return &rec, nil
}

func (s *Store) validRecord(r *Record) bool {
	if !filepath.IsAbs(r.Root) {
		return false
	}
	if r.Background {
		return true
	}
	if !treeRe.MatchString(r.Pre) || !s.validObjects(r.PreObjects) {
		return false
	}
	return r.Post == "" || (treeRe.MatchString(r.Post) && s.validObjects(r.PostObjects))
}

// validObjects accepts only this store's own bucket object directories.
func (s *Store) validObjects(dir string) bool {
	rel, err := filepath.Rel(filepath.Join(s.Dir, "buckets"), dir)
	if err != nil {
		return false
	}
	parts := strings.Split(rel, string(filepath.Separator))
	return len(parts) == 2 && dayRe.MatchString(parts[0]) && parts[1] == "objects"
}

// Records returns a session's records that hold a change, by tool use id.
func (s *Store) Records(session string) (map[string]*Record, error) {
	if !ValidSession(session) {
		return nil, ErrNoRecord
	}
	entries, err := os.ReadDir(filepath.Join(s.Dir, "records", session))
	if errors.Is(err, os.ErrNotExist) {
		return map[string]*Record{}, nil
	}
	if err != nil {
		return nil, err
	}
	out := map[string]*Record{}
	for _, e := range entries {
		id, ok := strings.CutSuffix(e.Name(), ".json")
		if !ok || !ValidToolUse(id) {
			continue
		}
		if rec, err := s.Record(session, id); err == nil && rec.Changed() {
			out[id] = rec
		}
	}
	return out, nil
}

// Sweep removes records older than Retention and object buckets a day older
// than that.
func (s *Store) Sweep() {
	now := s.now()
	sessions, _ := os.ReadDir(filepath.Join(s.Dir, "records"))
	for _, sd := range sessions {
		dir := filepath.Join(s.Dir, "records", sd.Name())
		files, _ := os.ReadDir(dir)
		for _, f := range files {
			if fi, err := f.Info(); err == nil && now.Sub(fi.ModTime()) > Retention {
				os.Remove(filepath.Join(dir, f.Name()))
			}
		}
		os.Remove(dir) // only succeeds once empty
	}
	cutoff := now.UTC().Add(-Retention - 24*time.Hour).Format("20060102")
	buckets, _ := os.ReadDir(filepath.Join(s.Dir, "buckets"))
	for _, b := range buckets {
		if dayRe.MatchString(b.Name()) && b.Name() < cutoff {
			os.RemoveAll(filepath.Join(s.Dir, "buckets", b.Name()))
		}
	}
}

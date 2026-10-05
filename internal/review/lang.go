package review

import (
	"bytes"
	"sort"
	"strconv"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/rvanmech/unky-mo/internal/gitfiles"
)

// ref is one outgoing reference of a file: the unit it points at, and the
// line it's on.
type ref struct {
	to   string
	line int
}

// language analyzes one language's dependencies between units — the parts
// of the code rules talk about, always named by a repo path ("internal/web",
// "app/models", "src/features/cart"). One instance serves one analysis, so
// detect can keep repo-wide state (Go's module path, Rails' constant index).
type language interface {
	name() string
	// exact is false for languages whose references are inferred from
	// names (Ruby constants, Swift types): their edges are approximate.
	exact() bool
	// detect reports whether the repo uses the language.
	detect(idx *index) bool
	// owns reports whether the language reads this file.
	owns(path string) bool
	// unit is the unit a file belongs to, "" if it belongs to none.
	unit(path string) string
	// refs lists a file's references to units. ok is false when the file
	// can't be read (mid-edit): its references are unknown, not empty.
	refs(path, src string) (refs []ref, ok bool)
}

// labeler is implemented by languages whose unit paths are long (Kotlin's
// app/src/main/kotlin/org/moma/app/data): a short name for display.
type labeler interface {
	label(unit string) string
}

// languages are the analyzers, fresh for each analysis.
func languages() []language {
	return []language{&goLang{}, &rubyLang{}, &nodeLang{}, &pyLang{}, &ktLang{}, &swiftLang{}}
}

// isTest reports whether a path is a test file, which never counts towards
// architecture or API.
func isTest(p string) bool {
	return gitfiles.Classify(p, "M", 1, false, nil) == gitfiles.KindTest
}

// index is one version of the tree as a file list: the change's new
// version (the working tree's tracked and untracked files, or the head
// commit's), or with rev set, that commit's (the base). Blob ids are known
// for tracked files, and key the symbol cache.
type index struct {
	r     *repo
	rev   string   // the commit this index lists, "" for the working tree
	paths []string // sorted
	blobs map[string]string
	sizes map[string]int64
	texts map[string]*string // prefetched contents (rev indexes only)
}

// newIndex lists the change's new version.
func newIndex(r *repo) *index {
	if r.head != "" {
		return newIndexAt(r, r.head)
	}
	x := &index{r: r, blobs: map[string]string{}}
	// <mode> SP <oid> SP <stage> TAB <path>
	if out, _, err := r.cmd.Output(r.ctx, r.root, "git", "ls-files", "-s", "-z"); err == nil {
		for _, rec := range strings.Split(string(out), "\x00") {
			meta, p, ok := strings.Cut(rec, "\t")
			if f := strings.Fields(meta); ok && len(f) == 3 {
				if _, dup := x.blobs[p]; !dup {
					x.paths = append(x.paths, p)
				}
				x.blobs[p] = f[1]
			}
		}
	}
	if out, _, err := r.cmd.Output(r.ctx, r.root, "git", "ls-files", "-z", "--others", "--exclude-standard"); err == nil {
		for _, p := range strings.Split(string(out), "\x00") {
			if p != "" {
				x.paths = append(x.paths, p)
			}
		}
	}
	sort.Strings(x.paths)
	return x
}

// newIndexAt lists the files of commit rev.
func newIndexAt(r *repo, rev string) *index {
	x := &index{r: r, rev: rev, blobs: map[string]string{}, sizes: map[string]int64{}}
	if rev == "" {
		return x
	}
	// <mode> SP <type> SP <oid> SP+ <size> TAB <path>
	out, _, err := r.cmd.Output(r.ctx, r.root, "git", "ls-tree", "-r", "-z", "-l", "--full-tree", rev)
	if err == nil {
		for _, rec := range strings.Split(string(out), "\x00") {
			meta, p, ok := strings.Cut(rec, "\t")
			if f := strings.Fields(meta); ok && len(f) == 4 && f[1] == "blob" {
				x.paths = append(x.paths, p)
				x.blobs[p] = f[2]
				if n, err := strconv.ParseInt(f[3], 10, 64); err == nil {
					x.sizes[p] = n
				}
			}
		}
	}
	sort.Strings(x.paths)
	return x
}

func (x *index) has(p string) bool {
	i := sort.SearchStrings(x.paths, p)
	return i < len(x.paths) && x.paths[i] == p
}

// hasDir reports whether any file lives under dir.
func (x *index) hasDir(dir string) bool {
	prefix := strings.TrimSuffix(dir, "/") + "/"
	i := sort.SearchStrings(x.paths, prefix)
	return i < len(x.paths) && strings.HasPrefix(x.paths[i], prefix)
}

// under lists the files under dir (recursively), or every file for "".
func (x *index) under(dir string) []string {
	if dir == "" || dir == "." {
		return x.paths
	}
	prefix := strings.TrimSuffix(dir, "/") + "/"
	i := sort.SearchStrings(x.paths, prefix)
	j := i
	for j < len(x.paths) && strings.HasPrefix(x.paths[j], prefix) {
		j++
	}
	return x.paths[i:j]
}

// read returns a file's text in the index's version.
func (x *index) read(p string) *string {
	if t, ok := x.texts[p]; ok {
		return t
	}
	switch {
	case x.rev == "" || x.rev == x.r.head:
		return x.r.readAfter(p)
	case x.rev == x.r.rev:
		return x.r.readBefore(p)
	}
	return nil
}

// prefetch reads the given files of a commit's index with a few batched
// git processes, so later reads don't take three processes each. Files
// over the size cap, and working tree indexes, are left alone.
func (x *index) prefetch(paths []string) {
	if x.rev == "" {
		return
	}
	if x.texts == nil {
		x.texts = map[string]*string{}
	}
	byOID := map[string][]string{}
	var oids []string
	for _, p := range paths {
		oid := x.blobs[p]
		if _, done := x.texts[p]; done || oid == "" || x.sizes[p] > gitfiles.MaxContentBytes {
			continue
		}
		if byOID[oid] == nil {
			oids = append(oids, oid)
		}
		byOID[oid] = append(byOID[oid], p)
	}
	if len(oids) == 0 {
		return
	}
	blobs, err := gitfiles.ReadBlobs(x.r.ctx, x.r.cmd, x.r.root, oids)
	if err != nil {
		return // reads fall back to one file at a time
	}
	for _, oid := range oids {
		var t *string
		if b, ok := blobs[oid]; ok && bytes.IndexByte(b, 0) < 0 && utf8.Valid(b) {
			s := string(b)
			t = &s
		}
		for _, p := range byOID[oid] {
			x.texts[p] = t
		}
	}
}

// symbols returns what extract finds in file p, cached by language and blob
// id across analyses, so polling doesn't re-read unchanged files. A file
// without a blob id (untracked) is read every time.
func (x *index) symbols(lang, p string, extract func(src string) any) any {
	oid := x.blobs[p]
	if oid != "" {
		if v, ok := symCache.get(lang + "\x00" + oid); ok {
			return v
		}
	}
	src := x.read(p)
	if src == nil {
		return nil
	}
	v := extract(*src)
	if oid != "" {
		symCache.put(lang+"\x00"+oid, v)
	}
	return v
}

// cached reports whether symbols(lang, p, …) would be served from the
// cache without reading the file.
func (x *index) cached(lang, p string) bool {
	oid := x.blobs[p]
	if oid == "" {
		return false
	}
	_, ok := symCache.get(lang + "\x00" + oid)
	return ok
}

// prefetchFor prefetches the files keep selects whose lang symbols aren't
// cached yet, ahead of a loop of symbols calls.
func (x *index) prefetchFor(lang string, keep func(p string) bool) {
	if x.rev == "" {
		return
	}
	var reads []string
	for _, p := range x.paths {
		if keep(p) && !x.cached(lang, p) {
			reads = append(reads, p)
		}
	}
	x.prefetch(reads)
}

// symCache is a bounded process-wide cache: on overflow, the oldest
// entries go first.
var symCache = &boundedCache{max: 20000, m: map[string]any{}}

type boundedCache struct {
	mu    sync.Mutex
	max   int
	m     map[string]any
	order []string
}

func (c *boundedCache) get(k string) (any, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	v, ok := c.m[k]
	return v, ok
}

func (c *boundedCache) put(k string, v any) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.m[k]; !ok {
		c.order = append(c.order, k)
		if len(c.order) > c.max {
			delete(c.m, c.order[0])
			c.order = c.order[1:]
		}
	}
	c.m[k] = v
}

package gitfiles

import (
	"bytes"
	"context"
	"fmt"
	"strconv"
	"strings"

	moexec "github.com/rvanmech/unky-mo/internal/exec"
)

// blobBatch is how many ids one `git cat-file --batch` process reads.
const blobBatch = 500

// ReadBlobs reads blobs by object id, many per git process, where ReadAt
// takes three processes per file. Ids that are missing, aren't blobs or are
// larger than MaxContentBytes are absent from the result. An id that isn't
// a full hex object id is an error, before git runs.
func ReadBlobs(ctx context.Context, cmd moexec.Commander, root string, oids []string) (map[string][]byte, error) {
	for _, id := range oids {
		if !hashRe.MatchString(id) {
			return nil, fmt.Errorf("not an object id: %q", id)
		}
	}
	out := make(map[string][]byte, len(oids))
	for len(oids) > 0 {
		n := min(len(oids), blobBatch)
		chunk := oids[:n]
		oids = oids[n:]
		stdout, stderr, err := cmd.OutputStdin(ctx, root, []byte(strings.Join(chunk, "\n")+"\n"), "git", "cat-file", "--batch")
		if err != nil {
			return nil, fmt.Errorf("git cat-file --batch: %v: %s", err, bytes.TrimSpace(stderr))
		}
		if err := parseBatch(stdout, out); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// parseBatch reads `git cat-file --batch` output: per id either
// "<oid> <type> <size>\n<content>\n" or "<oid> missing\n".
func parseBatch(b []byte, out map[string][]byte) error {
	for len(b) > 0 {
		nl := bytes.IndexByte(b, '\n')
		if nl < 0 {
			return fmt.Errorf("git cat-file --batch: truncated header")
		}
		f := strings.Fields(string(b[:nl]))
		b = b[nl+1:]
		if len(f) == 2 && (f[1] == "missing" || f[1] == "ambiguous") {
			continue
		}
		if len(f) != 3 {
			return fmt.Errorf("git cat-file --batch: bad header %q", f)
		}
		size, err := strconv.Atoi(f[2])
		if err != nil || size < 0 || size+1 > len(b) {
			return fmt.Errorf("git cat-file --batch: bad size in %q", f)
		}
		if f[1] == "blob" && size <= MaxContentBytes {
			out[f[0]] = b[:size:size]
		}
		b = b[size+1:]
	}
	return nil
}

package gitfiles

import (
	"context"
	"strconv"
	"strings"
	"testing"

	moexec "github.com/rvanmech/unky-mo/internal/exec"
	mock_exec "github.com/rvanmech/unky-mo/internal/exec/mocks"
	"go.uber.org/mock/gomock"
)

func TestReadBlobsRealGit(t *testing.T) {
	dir := newRepo(t)
	a := gitOut(t, dir, "rev-parse", "HEAD:a.go")
	b := gitOut(t, dir, "rev-parse", "HEAD:sub/b.go")
	tree := gitOut(t, dir, "rev-parse", "HEAD^{tree}")
	missing := strings.Repeat("0", len(a))
	got, err := ReadBlobs(context.Background(), moexec.DefaultCommander, dir, []string{a, missing, b, tree})
	if err != nil {
		t.Fatal(err)
	}
	if string(got[a]) != "one\ntwo\n" || string(got[b]) != "b\n" {
		t.Errorf("blobs = %q", got)
	}
	if _, ok := got[missing]; ok {
		t.Error("a missing id is in the result")
	}
	if _, ok := got[tree]; ok {
		t.Error("a tree is in the result")
	}
}

func TestReadBlobsRefusesNonIDs(t *testing.T) {
	ctrl := gomock.NewController(t)
	cmd := mock_exec.NewMockCommander(ctrl) // no expectations: git must not run
	for _, id := range []string{"HEAD", "abc123", "--batch-all-objects", strings.Repeat("g", 40)} {
		if _, err := ReadBlobs(context.Background(), cmd, "/repo", []string{id}); err == nil {
			t.Errorf("%q: no error", id)
		}
	}
}

func TestParseBatch(t *testing.T) {
	id1, id2 := strings.Repeat("a", 40), strings.Repeat("b", 40)
	big := strings.Repeat("x", MaxContentBytes+1)
	in := id1 + " blob 3\nab\n\n" + id2 + " missing\n" + id2 + " blob " + strconv.Itoa(len(big)) + "\n" + big + "\n"
	out := map[string][]byte{}
	if err := parseBatch([]byte(in), out); err != nil {
		t.Fatal(err)
	}
	if string(out[id1]) != "ab\n" || len(out) != 1 {
		t.Errorf("out = %d entries, %q", len(out), out[id1])
	}
	if err := parseBatch([]byte(id1+" blob 99\nshort\n"), map[string][]byte{}); err == nil {
		t.Error("truncated content: no error")
	}
}

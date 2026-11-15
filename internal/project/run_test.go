package project

import (
	"context"
	"errors"
	"testing"

	mock_exec "github.com/rvanmech/unky-mo/internal/exec/mocks"
	"go.uber.org/mock/gomock"
)

// The package's git calls go through Commander, with a deadline, so a
// test (or a caller) can stand in for git.
func TestCommanderSeam(t *testing.T) {
	ctrl := gomock.NewController(t)
	cmd := mock_exec.NewMockCommander(ctrl)
	old := Commander
	Commander = cmd
	t.Cleanup(func() { Commander = old })

	cmd.EXPECT().Output(gomock.Any(), "/ws/a", "git", "-C", "/ws/a", "status", "--porcelain").DoAndReturn(
		func(ctx context.Context, dir, name string, args ...string) ([]byte, []byte, error) {
			if _, ok := ctx.Deadline(); !ok {
				t.Error("no deadline on the git call")
			}
			return []byte(" M main.go\n"), nil, nil
		})
	if dirty, err := IsDirty("/ws/a"); err != nil || !dirty {
		t.Errorf("IsDirty = %v, %v", dirty, err)
	}

	cmd.EXPECT().CombinedOutput(gomock.Any(), "/ws/a", "git", "-C", "/ws/a", "stash", "pop").Return([]byte("conflict in x"), errors.New("exit 1"))
	if err := StashPop("/ws/a"); err == nil || err.Error() != "conflict in x" {
		t.Errorf("StashPop error %v", err)
	}
}

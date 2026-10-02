package claude

import (
	"os"
	"os/exec"
	"strconv"
	"testing"

	mock_exec "github.com/rvanmech/unky-mo/internal/exec/mocks"
	"go.uber.org/mock/gomock"
)

// withSessionsCommander swaps the package's exec seam for the duration of
// the test.
func withSessionsCommander(t *testing.T, cmd *mock_exec.MockCommander) {
	t.Helper()
	old := sessionsCommander
	sessionsCommander = cmd
	t.Cleanup(func() { sessionsCommander = old })
}

func TestReadSessionsPassesAllFlag(t *testing.T) {
	ctrl := gomock.NewController(t)
	cmd := mock_exec.NewMockCommander(ctrl)
	cmd.EXPECT().
		Output(gomock.Any(), "", "claude", "agents", "--json", "--all").
		Return([]byte(agentsJSON), nil, nil)
	withSessionsCommander(t, cmd)

	sessions, err := ReadSessions()
	if err != nil {
		t.Fatalf("ReadSessions: %v", err)
	}
	if len(sessions) != 2 {
		t.Fatalf("want 2 sessions, got %d", len(sessions))
	}
	if sessions[0].PID != 79873 || sessions[0].SessionID != "80013ad0-d9ea-4a6b-9861-c05ecc2f8be3" {
		t.Errorf("unexpected session: %+v", sessions[0])
	}
}

func TestLiveSessionsOmitsAllFlagAndFiltersDeadPIDs(t *testing.T) {
	// Spawn and reap a process so we have a PID that's guaranteed dead
	// without risking collision with something actually alive on the box.
	c := exec.Command("true")
	if err := c.Run(); err != nil {
		t.Fatalf("spawning throwaway process: %v", err)
	}
	deadPID := c.Process.Pid

	livePID := os.Getpid() // this test process is definitely alive

	ctrl := gomock.NewController(t)
	cmd := mock_exec.NewMockCommander(ctrl)
	cmd.EXPECT().
		Output(gomock.Any(), "", "claude", "agents", "--json").
		Return([]byte(`[
			{"pid":`+strconv.Itoa(livePID)+`,"cwd":"/tmp/live","kind":"interactive","startedAt":1,"sessionId":"live","name":"","status":"idle"},
			{"pid":`+strconv.Itoa(deadPID)+`,"cwd":"/tmp/dead","kind":"interactive","startedAt":1,"sessionId":"dead","name":"","status":"idle"}
		]`), nil, nil)
	withSessionsCommander(t, cmd)

	sessions, err := LiveSessions()
	if err != nil {
		t.Fatalf("LiveSessions: %v", err)
	}
	if len(sessions) != 1 || sessions[0].SessionID != "live" {
		t.Fatalf("want only the live session to survive, got %+v", sessions)
	}
}

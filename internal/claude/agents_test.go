package claude

import (
	"context"
	"errors"
	"testing"

	mock_exec "github.com/rvanmech/unky-mo/internal/exec/mocks"
	"go.uber.org/mock/gomock"
)

const agentsJSON = `[
  {"pid":79873,"cwd":"/Users/rvanmech/workspace/unky-mo","kind":"interactive","startedAt":1790948805119,"sessionId":"80013ad0-d9ea-4a6b-9861-c05ecc2f8be3","name":"unky-mo-50","status":"busy"},
  {"pid":84938,"cwd":"/Users/rvanmech/workspace/moma-apps-rails","kind":"interactive","startedAt":1790948978736,"sessionId":"3d4656be-de4b-48ea-8e1d-d0597ed92731","name":"moma-apps-rails-d0","status":"idle"}
]`

func TestLiveAgentsParsesOutput(t *testing.T) {
	ctrl := gomock.NewController(t)
	cmd := mock_exec.NewMockCommander(ctrl)
	cmd.EXPECT().
		Output(gomock.Any(), "", "claude", "agents", "--json").
		Return([]byte(agentsJSON), nil, nil)

	agents, err := LiveAgents(context.Background(), cmd, false)
	if err != nil {
		t.Fatalf("LiveAgents: %v", err)
	}
	if len(agents) != 2 {
		t.Fatalf("want 2 agents, got %d", len(agents))
	}
	if agents[0].PID != 79873 || agents[0].SessionID != "80013ad0-d9ea-4a6b-9861-c05ecc2f8be3" || agents[0].Status != "busy" {
		t.Errorf("first agent wrong: %+v", agents[0])
	}
	if agents[1].Status != "idle" || agents[1].Name != "moma-apps-rails-d0" {
		t.Errorf("second agent wrong: %+v", agents[1])
	}
}

func TestLiveAgentsPassesAllFlag(t *testing.T) {
	ctrl := gomock.NewController(t)
	cmd := mock_exec.NewMockCommander(ctrl)
	cmd.EXPECT().
		Output(gomock.Any(), "", "claude", "agents", "--json", "--all").
		Return([]byte(`[]`), nil, nil)

	agents, err := LiveAgents(context.Background(), cmd, true)
	if err != nil {
		t.Fatalf("LiveAgents: %v", err)
	}
	if len(agents) != 0 {
		t.Errorf("want 0 agents, got %d", len(agents))
	}
}

func TestLiveAgentsCommandError(t *testing.T) {
	ctrl := gomock.NewController(t)
	cmd := mock_exec.NewMockCommander(ctrl)
	cmd.EXPECT().
		Output(gomock.Any(), "", "claude", "agents", "--json").
		Return(nil, []byte("command not found"), errors.New("exit 127"))

	_, err := LiveAgents(context.Background(), cmd, false)
	if err == nil {
		t.Fatal("want error when claude binary is missing, got nil")
	}
}

func TestLiveAgentsMalformedJSON(t *testing.T) {
	ctrl := gomock.NewController(t)
	cmd := mock_exec.NewMockCommander(ctrl)
	cmd.EXPECT().
		Output(gomock.Any(), "", "claude", "agents", "--json").
		Return([]byte("not json"), nil, nil)

	_, err := LiveAgents(context.Background(), cmd, false)
	if err == nil {
		t.Fatal("want error on malformed JSON, got nil")
	}
}

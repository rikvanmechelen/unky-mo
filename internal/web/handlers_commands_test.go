package web

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/rvanmech/unky-mo/internal/claude"
	"github.com/rvanmech/unky-mo/internal/state"
	mock_web "github.com/rvanmech/unky-mo/internal/web/mocks"
	"go.uber.org/mock/gomock"
)

func commandsFixture(t *testing.T) (*Server, *mock_web.MockSlashCommands) {
	t.Helper()
	ctrl := gomock.NewController(t)
	st := mock_web.NewMockStateReader(ctrl)
	st.EXPECT().Read().Return(&state.StateFile{Projects: []state.ProjectState{
		{Name: "foo", WindowID: "@5", SessionID: "s5", Path: "/ws/foo", Status: "idle"},
		{Name: "bar", WindowID: "@6", Path: "/ws/bar", Status: "none"},
	}}, nil).AnyTimes()
	cmds := mock_web.NewMockSlashCommands(ctrl)
	return NewServer(Deps{State: st, Commands: cmds}, 0, "test"), cmds
}

func TestCommandsList(t *testing.T) {
	srv, cmds := commandsFixture(t)
	// Two fetches inside the cache TTL share one listing, read for the
	// state row's checkout and session.
	cmds.EXPECT().List("/ws/foo", "s5").Return([]claude.SlashCommand{
		{Name: "compact", Description: "Summarize", ArgumentHint: "<instructions>", Source: claude.CommandBuiltin},
		{Name: "model", Description: "Set the model", Source: claude.CommandBuiltin, Dialog: true, Aliases: []string{"m"}},
		{Name: "eng:review", Description: "Review", Source: claude.CommandPlugin},
	}).Times(1)

	for range 2 {
		rec := get(t, srv, "/api/sessions/@5/commands")
		if rec.Code != http.StatusOK {
			t.Fatalf("got %d %s", rec.Code, rec.Body)
		}
		var got []commandView
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		if len(got) != 3 || got[0].ArgumentHint != "<instructions>" || !got[1].Dialog || got[1].Aliases[0] != "m" || got[2].Source != "plugin" {
			t.Errorf("got %+v", got)
		}
	}
}

func TestCommandsNoSession(t *testing.T) {
	srv, _ := commandsFixture(t) // no List expectation: gomock fails on a call
	for _, path := range []string{"/api/sessions/@6/commands", "/api/sessions/@9/commands"} {
		if rec := get(t, srv, path); rec.Code != http.StatusNotFound {
			t.Errorf("%s: want 404, got %d", path, rec.Code)
		}
	}
}

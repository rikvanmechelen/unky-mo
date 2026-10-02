package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/rvanmech/unky-mo/internal/project"
	mock_web "github.com/rvanmech/unky-mo/internal/web/mocks"
	"go.uber.org/mock/gomock"
)

func TestHandleProjects(t *testing.T) {
	ctrl := gomock.NewController(t)
	mockProjects := mock_web.NewMockProjectLister(ctrl)
	mockProjects.EXPECT().LoadProjects().Return([]project.Project{{Name: "a", Path: "/ws/a"}}, nil)

	srv := NewServer(Deps{Projects: mockProjects}, 0, "test")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/projects", nil))

	var got []project.Project
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got) != 1 || got[0].Name != "a" {
		t.Fatalf("unexpected body: %+v", got)
	}
}

func TestHandleWorktrees(t *testing.T) {
	ctrl := gomock.NewController(t)
	mockProjects := mock_web.NewMockProjectLister(ctrl)
	mockProjects.EXPECT().LoadProjects().Return([]project.Project{{Name: "a", Path: "/ws/a"}}, nil)
	mockWorktrees := mock_web.NewMockWorktreeReader(ctrl)
	mockWorktrees.EXPECT().ListBranches("/ws/a").Return([]project.Branch{{Name: "main", IsMain: true}}, nil)

	srv := NewServer(Deps{Projects: mockProjects, Worktrees: mockWorktrees}, 0, "test")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/projects/a/worktrees", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status: want 200, got %d", rec.Code)
	}
	var got []project.Branch
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got) != 1 || got[0].Name != "main" {
		t.Fatalf("unexpected body: %+v", got)
	}
}

func TestHandleWorktreesUnknownProject(t *testing.T) {
	ctrl := gomock.NewController(t)
	mockProjects := mock_web.NewMockProjectLister(ctrl)
	mockProjects.EXPECT().LoadProjects().Return([]project.Project{}, nil)

	srv := NewServer(Deps{Projects: mockProjects}, 0, "test")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/projects/missing/worktrees", nil))

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status: want 404, got %d", rec.Code)
	}
}

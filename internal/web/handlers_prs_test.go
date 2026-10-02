package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/rvanmech/unky-mo/internal/github"
	"github.com/rvanmech/unky-mo/internal/project"
	mock_web "github.com/rvanmech/unky-mo/internal/web/mocks"
	"go.uber.org/mock/gomock"
)

func TestHandlePRs(t *testing.T) {
	ctrl := gomock.NewController(t)
	mockProjects := mock_web.NewMockProjectLister(ctrl)
	mockProjects.EXPECT().LoadProjects().Return([]project.Project{{Name: "a", Path: "/ws/a"}}, nil).AnyTimes()
	mockPRs := mock_web.NewMockPRClient(ctrl)
	mockPRs.EXPECT().ListPRs("/ws/a").Return([]github.PullRequest{{Number: 1, Title: "fix"}}, nil).Times(1)

	srv := NewServer(Deps{Projects: mockProjects, PRs: mockPRs}, 0, "test")

	// Two requests within the cache TTL should only hit ListPRs once.
	for i := 0; i < 2; i++ {
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/projects/a/prs", nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("status: want 200, got %d", rec.Code)
		}
	}
}

func TestHandlePRDetail(t *testing.T) {
	ctrl := gomock.NewController(t)
	mockProjects := mock_web.NewMockProjectLister(ctrl)
	mockProjects.EXPECT().LoadProjects().Return([]project.Project{{Name: "a", Path: "/ws/a"}}, nil)
	mockPRs := mock_web.NewMockPRClient(ctrl)
	mockPRs.EXPECT().GetPRDetail("/ws/a", 7).Return(&github.PRDetail{Number: 7, Title: "fix"}, nil)

	srv := NewServer(Deps{Projects: mockProjects, PRs: mockPRs}, 0, "test")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/projects/a/prs/7", nil))

	var got github.PRDetail
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Number != 7 {
		t.Fatalf("want PR 7, got %+v", got)
	}
}

func TestHandlePRDetailBadNumber(t *testing.T) {
	ctrl := gomock.NewController(t)
	mockProjects := mock_web.NewMockProjectLister(ctrl)
	mockProjects.EXPECT().LoadProjects().Return([]project.Project{{Name: "a", Path: "/ws/a"}}, nil)

	srv := NewServer(Deps{Projects: mockProjects}, 0, "test")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/projects/a/prs/not-a-number", nil))

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status: want 400, got %d", rec.Code)
	}
}

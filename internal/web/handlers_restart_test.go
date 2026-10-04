package web

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	mock_web "github.com/rvanmech/unky-mo/internal/web/mocks"
	"go.uber.org/mock/gomock"

	"github.com/rvanmech/unky-mo/internal/notify"
)

func TestHandleRestartQueuesRestart(t *testing.T) {
	ctrl := gomock.NewController(t)
	r := mock_web.NewMockRestarter(ctrl)
	r.EXPECT().Restart().Return(nil).Times(1)

	srv := NewServer(Deps{Restarter: r}, 0, "test")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/restart", nil))
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status: want 202, got %d", rec.Code)
	}
}

func TestHandleRestartTUINotRunningIs503(t *testing.T) {
	ctrl := gomock.NewController(t)
	r := mock_web.NewMockRestarter(ctrl)
	r.EXPECT().Restart().Return(notify.ErrTUINotRunning)

	srv := NewServer(Deps{Restarter: r}, 0, "test")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/restart", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status: want 503, got %d", rec.Code)
	}
}

func TestHandleRestartOtherErrorIs500(t *testing.T) {
	ctrl := gomock.NewController(t)
	r := mock_web.NewMockRestarter(ctrl)
	r.EXPECT().Restart().Return(errors.New("write failed"))

	srv := NewServer(Deps{Restarter: r}, 0, "test")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/restart", nil))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status: want 500, got %d", rec.Code)
	}
}

// Only POST restarts: a GET (a prefetch, a stray link) must not.
func TestHandleRestartRejectsGet(t *testing.T) {
	ctrl := gomock.NewController(t)
	r := mock_web.NewMockRestarter(ctrl) // no expectations: any call fails

	srv := NewServer(Deps{Restarter: r}, 0, "test")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/restart", nil))
	if rec.Code < 400 {
		t.Fatalf("status: want an error, got %d", rec.Code)
	}
}

func TestHandleBootStablePerServer(t *testing.T) {
	boot := func(srv *Server) string {
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/boot", nil))
		var body struct{ Boot string }
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || body.Boot == "" {
			t.Fatalf("bad /api/boot response %q: %v", rec.Body.String(), err)
		}
		return body.Boot
	}
	a := NewServer(Deps{}, 0, "test")
	if boot(a) != boot(a) {
		t.Fatal("boot id should be stable within one server")
	}
	if boot(a) == boot(NewServer(Deps{}, 0, "test")) {
		t.Fatal("a new server should have a new boot id")
	}
}

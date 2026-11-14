package web

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func getMenu(srv *Server, window, etag string) *httptest.ResponseRecorder {
	req := httptest.NewRequest("GET", "/api/sessions/"+window+"/menu", nil)
	if etag != "" {
		req.Header.Set("If-None-Match", etag)
	}
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	return rec
}

func TestHandleMenuReturnsTheParsedMenu(t *testing.T) {
	srv, pane := modeServer(t, "idle")
	pane.EXPECT().CaptureStyled("mo:@4.0").Return(menuFixture(t, "model.ansi"), nil)
	rec := getMenu(srv, "@4", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	var body struct {
		Menu *menuView `json:"menu"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Menu == nil || body.Menu.Title != "Select model" || len(body.Menu.Rows) < 9 || body.Menu.Sig == "" {
		t.Fatalf("menu = %+v", body.Menu)
	}
	if rec.Header().Get("ETag") == "" {
		t.Error("no ETag")
	}
}

func TestHandleMenuNoMenuIsNull(t *testing.T) {
	srv, pane := modeServer(t, "idle")
	pane.EXPECT().CaptureStyled("mo:@4.0").Return(footerScreen("  ⏵⏵ auto mode on (shift+tab to cycle)"), nil)
	rec := getMenu(srv, "@4", "")
	if rec.Code != http.StatusOK || rec.Body.String() != "{\"menu\":null}\n" {
		t.Fatalf("status %d body %q", rec.Code, rec.Body)
	}
}

func TestHandleMenuIgnoresPermissionDialogs(t *testing.T) {
	srv, pane := modeServer(t, "permission")
	pane.EXPECT().CaptureStyled("mo:@4.0").Return(menuFixture(t, "../permission/bash.txt"), nil)
	if rec := getMenu(srv, "@4", ""); rec.Body.String() != "{\"menu\":null}\n" {
		t.Fatalf("body %q", rec.Body)
	}
}

func TestHandleMenuETag304(t *testing.T) {
	srv, pane := modeServer(t, "idle")
	pane.EXPECT().CaptureStyled("mo:@4.0").Return(menuFixture(t, "model.ansi"), nil).Times(2)
	etag := getMenu(srv, "@4", "").Header().Get("ETag")
	rec := getMenu(srv, "@4", etag)
	if rec.Code != http.StatusNotModified || rec.Body.Len() != 0 {
		t.Fatalf("status %d body %q", rec.Code, rec.Body)
	}
}

func TestHandleMenuUnknownWindow(t *testing.T) {
	srv, _ := modeServer(t, "idle")
	if rec := getMenu(srv, "@99", ""); rec.Code != http.StatusNotFound {
		t.Fatalf("status %d", rec.Code)
	}
}

func TestHandleMenuCaptureFailure(t *testing.T) {
	srv, pane := modeServer(t, "idle")
	pane.EXPECT().CaptureStyled("mo:@4.0").Return("", errTest)
	if rec := getMenu(srv, "@4", ""); rec.Code != http.StatusBadGateway {
		t.Fatalf("status %d", rec.Code)
	}
}

var errTest = errors.New("tmux gone")

package web

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/rvanmech/unky-mo/internal/state"
	mock_web "github.com/rvanmech/unky-mo/internal/web/mocks"
	"go.uber.org/mock/gomock"
)

func liveState(t *testing.T, windowID, status string) *mock_web.MockStateReader {
	t.Helper()
	mockState := mock_web.NewMockStateReader(gomock.NewController(t))
	mockState.EXPECT().Read().Return(&state.StateFile{
		Projects: []state.ProjectState{{WindowID: windowID, SessionID: "s1", Status: status}},
	}, nil).AnyTimes()
	return mockState
}

func postAttachment(t *testing.T, srv *Server, windowID string, body io.Reader) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/sessions/"+windowID+"/attachments", body))
	return rec
}

func TestHandleUploadAttachmentReturnsID(t *testing.T) {
	store := newAttachmentStore(t.TempDir(), time.Now)
	// Uploading isn't gated on status: attach while Claude works.
	srv := NewServer(Deps{State: liveState(t, "@5", "active"), Attachments: store}, 0, "test")

	rec := postAttachment(t, srv, "@5", strings.NewReader(pngHeader))
	if rec.Code != http.StatusOK {
		t.Fatalf("status: want 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp struct{ ID string }
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatal(err)
	}
	if _, ok := store.Path("@5", resp.ID); !ok {
		t.Errorf("id %q not in the store", resp.ID)
	}
}

func TestHandleUploadAttachmentErrors(t *testing.T) {
	store := newAttachmentStore(t.TempDir(), time.Now)
	srv := NewServer(Deps{State: liveState(t, "@5", "idle"), Attachments: store}, 0, "test")

	if rec := postAttachment(t, srv, "@9", strings.NewReader(pngHeader)); rec.Code != http.StatusNotFound {
		t.Errorf("unknown window: want 404, got %d", rec.Code)
	}
	if rec := postAttachment(t, srv, "@5", strings.NewReader("<svg></svg>")); rec.Code != http.StatusUnsupportedMediaType {
		t.Errorf("not an image: want 415, got %d", rec.Code)
	}
	big := bytes.NewReader(append([]byte(pngHeader), make([]byte, maxAttachmentBytes)...))
	if rec := postAttachment(t, srv, "@5", big); rec.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("oversized: want 413, got %d", rec.Code)
	}
}

func TestHandleDeleteAttachment(t *testing.T) {
	store := newAttachmentStore(t.TempDir(), time.Now)
	id, _ := store.Save("@5", strings.NewReader(pngHeader))
	srv := NewServer(Deps{Attachments: store}, 0, "test")

	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodDelete, "/api/sessions/@5/attachments/"+id, nil))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status: want 204, got %d", rec.Code)
	}
	if _, ok := store.Path("@5", id); ok {
		t.Error("attachment still there")
	}
}

func TestHandleGetAttachment(t *testing.T) {
	store := newAttachmentStore(t.TempDir(), time.Now)
	id, err := store.Save("@5", strings.NewReader(pngHeader))
	if err != nil {
		t.Fatal(err)
	}
	srv := NewServer(Deps{Attachments: store}, 0, "test")
	get := func(path string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		return rec
	}

	rec := get("/api/sessions/@5/attachments/" + id)
	if rec.Code != http.StatusOK {
		t.Fatalf("status: want 200, got %d", rec.Code)
	}
	if got := rec.Header().Get("Content-Type"); got != "image/png" {
		t.Errorf("content type: want image/png, got %q", got)
	}
	if rec.Body.String() != pngHeader {
		t.Error("body isn't the stored image")
	}

	for _, path := range []string{
		"/api/sessions/@6/attachments/" + id,                               // another window's id
		"/api/sessions/@5/attachments/" + strings.Repeat("0", 32) + ".png", // unknown
		"/api/sessions/@5/attachments/..%2F..%2Fetc%2Fpasswd",
		"/api/sessions/@5/attachments/notanid.png",
	} {
		if rec := get(path); rec.Code != http.StatusNotFound {
			t.Errorf("%s: want 404, got %d", path, rec.Code)
		}
	}
}

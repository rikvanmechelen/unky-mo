package web

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
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
	var resp Attachment
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatal(err)
	}
	if resp.Kind != AttachmentImage {
		t.Errorf("kind %q, want image", resp.Kind)
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
	if rec := postAttachment(t, srv, "@5", strings.NewReader("")); rec.Code != http.StatusBadRequest {
		t.Errorf("empty: want 400, got %d", rec.Code)
	}
	big := bytes.NewReader(append([]byte(pngHeader), make([]byte, maxImageBytes)...))
	if rec := postAttachment(t, srv, "@5", big); rec.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("oversized image: want 413, got %d", rec.Code)
	}
	huge := bytes.NewReader(make([]byte, maxFileBytes+1))
	if rec := postAttachment(t, srv, "@5", huge); rec.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("oversized file: want 413, got %d", rec.Code)
	}
}

// Any other content is a file, stored under the name from ?name=.
func TestHandleUploadAttachmentFile(t *testing.T) {
	store := newAttachmentStore(t.TempDir(), time.Now)
	srv := NewServer(Deps{State: liveState(t, "@5", "idle"), Attachments: store}, 0, "test")

	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/sessions/@5/attachments?name=my%20report.pdf", strings.NewReader("%PDF-1.7")))
	if rec.Code != http.StatusOK {
		t.Fatalf("status: want 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var a Attachment
	if err := json.NewDecoder(rec.Body).Decode(&a); err != nil {
		t.Fatal(err)
	}
	if a.Kind != AttachmentFile || a.Name != "my_report.pdf" {
		t.Errorf("got %+v, want a file named my_report.pdf", a)
	}
	if p, ok := store.Path("@5", a.ID); !ok || filepath.Base(p) != "my_report.pdf" {
		t.Errorf("Path(%q) = %q, %v", a.ID, p, ok)
	}
}

func TestHandleDeleteAttachment(t *testing.T) {
	store := newAttachmentStore(t.TempDir(), time.Now)
	id := saveID(t, store, "@5", strings.NewReader(pngHeader))
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
	id := saveID(t, store, "@5", strings.NewReader(pngHeader))
	file := saveFile(t, store, "@5", "page.html", "<html><script>alert(1)</script></html>")
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
		"/api/sessions/@5/attachments/" + file, // a file, never served back
	} {
		if rec := get(path); rec.Code != http.StatusNotFound {
			t.Errorf("%s: want 404, got %d", path, rec.Code)
		}
	}
}

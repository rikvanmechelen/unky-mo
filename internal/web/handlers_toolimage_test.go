package web

import (
	"bytes"
	"encoding/base64"
	"image"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/rvanmech/unky-mo/internal/claude"
	"github.com/rvanmech/unky-mo/internal/state"
	mock_web "github.com/rvanmech/unky-mo/internal/web/mocks"
	"go.uber.org/mock/gomock"
)

func tinyPNG(t *testing.T, w int) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := png.Encode(&buf, image.NewGray(image.Rect(0, 0, w, 1))); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// toolImageServer serves window @1 with a session whose JSONL holds lines.
func toolImageServer(t *testing.T, lines ...string) *Server {
	t.Helper()
	projectPath := t.TempDir()
	dir := claude.ProjectsDirForPath(projectPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	writeFile(t, dir, "sessIMG.jsonl", strings.Join(lines, "\n")+"\n")

	mockState := mock_web.NewMockStateReader(gomock.NewController(t))
	mockState.EXPECT().Read().Return(&state.StateFile{
		Projects: []state.ProjectState{{WindowID: "@1", SessionID: "sessIMG", Path: projectPath, Status: "idle"}},
	}, nil).AnyTimes()
	return NewServer(Deps{State: mockState}, 0, "test")
}

func imageResult(id string, payloads ...[]byte) string {
	var blocks []string
	for _, p := range payloads {
		blocks = append(blocks, `{"type":"image","source":{"type":"base64","media_type":"image/png","data":"`+base64.StdEncoding.EncodeToString(p)+`"}}`)
	}
	return `{"type":"user","uuid":"u-` + id + `","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"` + id + `","content":[` + strings.Join(blocks, ",") + `]}]}}`
}

func TestHandleToolImage(t *testing.T) {
	first, second := tinyPNG(t, 1), tinyPNG(t, 2)
	svg := []byte(`<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`)
	html := []byte(`<!DOCTYPE html><html><script>alert(1)</script></html>`)
	srv := toolImageServer(t,
		// The id quoted in a prompt's text first: not a tool result.
		`{"type":"user","uuid":"p1","message":{"role":"user","content":"look at \"toolu_A\" please"}}`,
		imageResult("toolu_A", first, second),
		imageResult("toolu_SVG", svg),
		imageResult("toolu_HTML", html),
		`{"type":"user","uuid":"t1","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_TEXT","content":"no image"}]}}`,
	)

	get := func(url string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, url, nil))
		return rec
	}

	for i, want := range [][]byte{first, second} {
		rec := get("/api/sessions/@1/tool-image/toolu_A?i=" + string(rune('0'+i)))
		if rec.Code != http.StatusOK || !bytes.Equal(rec.Body.Bytes(), want) {
			t.Fatalf("image %d: status %d, %d bytes", i, rec.Code, rec.Body.Len())
		}
		if got := rec.Header().Get("Content-Type"); got != "image/png" {
			t.Errorf("image %d: Content-Type %q", i, got)
		}
		if rec.Header().Get("X-Content-Type-Options") != "nosniff" || !strings.Contains(rec.Header().Get("Cache-Control"), "immutable") {
			t.Errorf("image %d: headers %v", i, rec.Header())
		}
	}

	for name, tc := range map[string]struct {
		url  string
		code int
	}{
		"index past the images":  {"/api/sessions/@1/tool-image/toolu_A?i=2", http.StatusNotFound},
		"unknown id":             {"/api/sessions/@1/tool-image/toolu_NOPE?i=0", http.StatusNotFound},
		"result without images":  {"/api/sessions/@1/tool-image/toolu_TEXT?i=0", http.StatusNotFound},
		"svg payload":            {"/api/sessions/@1/tool-image/toolu_SVG?i=0", http.StatusNotFound},
		"html payload":           {"/api/sessions/@1/tool-image/toolu_HTML?i=0", http.StatusNotFound},
		"malformed id":           {"/api/sessions/@1/tool-image/toolu_A%22,%22x?i=0", http.StatusBadRequest},
		"missing index":          {"/api/sessions/@1/tool-image/toolu_A", http.StatusBadRequest},
		"negative index":         {"/api/sessions/@1/tool-image/toolu_A?i=-1", http.StatusBadRequest},
		"index over the cap":     {"/api/sessions/@1/tool-image/toolu_A?i=32", http.StatusBadRequest},
		"window without session": {"/api/sessions/@9/tool-image/toolu_A?i=0", http.StatusNotFound},
	} {
		if rec := get(tc.url); rec.Code != tc.code {
			t.Errorf("%s: want %d, got %d", name, tc.code, rec.Code)
		}
	}
}

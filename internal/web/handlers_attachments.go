package web

import (
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
)

// handleUploadAttachment stores one upload (the raw request body, its name
// in ?name=) for a live session's composer and returns its id, kind and
// stored name; a later /prompt names the id in "attachments". It isn't
// gated on the session's status: a file can be attached while Claude is
// busy and sent once it's idle.
func (s *Server) handleUploadAttachment(w http.ResponseWriter, r *http.Request) {
	windowID := r.PathValue("windowID")
	if s.deps.Attachments == nil {
		writeError(w, http.StatusServiceUnavailable, fmt.Errorf("attachments are not available"))
		return
	}
	if _, _, _, ok := s.resolveSession(windowID); !ok {
		writeError(w, http.StatusNotFound, fmt.Errorf("no live session for window %s", windowID))
		return
	}
	a, err := s.deps.Attachments.Save(windowID, r.URL.Query().Get("name"), http.MaxBytesReader(w, r.Body, maxFileBytes+1))
	var tooBig *http.MaxBytesError
	switch {
	case errors.Is(err, ErrAttachmentTooLarge), errors.As(err, &tooBig):
		writeError(w, http.StatusRequestEntityTooLarge, ErrAttachmentTooLarge)
	case errors.Is(err, ErrEmptyAttachment):
		writeError(w, http.StatusBadRequest, err)
	case err != nil:
		writeError(w, http.StatusInternalServerError, fmt.Errorf("failed to store the file: %w", err))
	default:
		writeJSON(w, a)
	}
}

// handleDeleteAttachment drops an image removed from the composer before
// sending. Best effort: anything left behind expires (AttachmentStore.Sweep).
func (s *Server) handleDeleteAttachment(w http.ResponseWriter, r *http.Request) {
	s.deps.Attachments.Delete(r.PathValue("windowID"), r.PathValue("id"))
	w.WriteHeader(http.StatusNoContent)
}

// handleGetAttachment serves a stored image, for the thumbnails of a draft
// restored after a page reload. Like DELETE it isn't gated on a live
// session; the store only hands out ids saved under that window. Other
// files are never served: that would hand any uploaded content back to a
// browser under this origin.
func (s *Server) handleGetAttachment(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	p, ok := s.deps.Attachments.Path(r.PathValue("windowID"), id)
	if !ok || !isImageAttachmentID(id) {
		writeError(w, http.StatusNotFound, fmt.Errorf("no such attachment"))
		return
	}
	// The store keeps only the four sniffed types, named by extension.
	ext := strings.TrimPrefix(filepath.Ext(p), ".")
	for typ, e := range attachmentExts {
		if e == ext {
			w.Header().Set("Content-Type", typ)
		}
	}
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "private, max-age=86400")
	http.ServeFile(w, r, p)
}

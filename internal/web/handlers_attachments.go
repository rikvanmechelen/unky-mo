package web

import (
	"errors"
	"fmt"
	"net/http"
)

// handleUploadAttachment stores one image (the raw request body) for a live
// session's composer and returns its id, which a later /prompt names in
// "attachments". It isn't gated on the session's status: an image can be
// attached while Claude is busy and sent once it's idle.
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
	id, err := s.deps.Attachments.Save(windowID, http.MaxBytesReader(w, r.Body, maxAttachmentBytes+1))
	var tooBig *http.MaxBytesError
	switch {
	case errors.Is(err, ErrAttachmentTooLarge), errors.As(err, &tooBig):
		writeError(w, http.StatusRequestEntityTooLarge, ErrAttachmentTooLarge)
	case errors.Is(err, ErrUnsupportedAttachment):
		writeError(w, http.StatusUnsupportedMediaType, err)
	case err != nil:
		writeError(w, http.StatusInternalServerError, fmt.Errorf("failed to store image: %w", err))
	default:
		writeJSON(w, map[string]string{"id": id})
	}
}

// handleDeleteAttachment drops an image removed from the composer before
// sending. Best effort: anything left behind expires (AttachmentStore.Sweep).
func (s *Server) handleDeleteAttachment(w http.ResponseWriter, r *http.Request) {
	s.deps.Attachments.Delete(r.PathValue("windowID"), r.PathValue("id"))
	w.WriteHeader(http.StatusNoContent)
}

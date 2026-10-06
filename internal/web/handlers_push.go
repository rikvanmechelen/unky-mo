package web

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/rvanmech/unky-mo/internal/webpush"
)

// maxPushBody bounds the push endpoints' request bodies: a subscription is
// a URL and two short keys.
const maxPushBody = 8 << 10

// maxPushLabel bounds a device's label ("Chrome on Android").
const maxPushLabel = 80

// decodePushBody decodes a small JSON body, answering 400/413 itself.
func decodePushBody(w http.ResponseWriter, r *http.Request, v any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, maxPushBody)
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			writeError(w, http.StatusRequestEntityTooLarge, errors.New("request body too large"))
		} else {
			writeError(w, http.StatusBadRequest, fmt.Errorf("invalid request body: %w", err))
		}
		return false
	}
	return true
}

// pushEnabled answers 404 when notifications are off ([web] disable_push).
func (s *Server) pushEnabled(w http.ResponseWriter) bool {
	if s.deps.Push == nil {
		writeError(w, http.StatusNotFound, errors.New("notifications are disabled"))
		return false
	}
	return true
}

// handlePushInfo tells the browser whether it can subscribe, and with which
// VAPID key.
func (s *Server) handlePushInfo(w http.ResponseWriter, r *http.Request) {
	if s.deps.Push == nil {
		writeJSON(w, map[string]any{"enabled": false})
		return
	}
	writeJSON(w, map[string]any{"enabled": true, "publicKey": s.deps.Push.PublicKey()})
}

// handlePushLookup returns this device's subscription state, for the
// notifications dialog.
func (s *Server) handlePushLookup(w http.ResponseWriter, r *http.Request) {
	if !s.pushEnabled(w) {
		return
	}
	e, ok := s.deps.Push.Lookup(r.URL.Query().Get("endpoint"))
	if !ok {
		writeJSON(w, map[string]any{"subscribed": false})
		return
	}
	writeJSON(w, map[string]any{"subscribed": true, "events": e.Events, "label": e.Label})
}

func (s *Server) handlePushSubscribe(w http.ResponseWriter, r *http.Request) {
	if !s.pushEnabled(w) {
		return
	}
	var body struct {
		Subscription webpush.Subscription `json:"subscription"`
		Events       PushEvents           `json:"events"`
		Label        string               `json:"label"`
	}
	if !decodePushBody(w, r, &body) {
		return
	}
	if err := body.Subscription.Validate(); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	label := strings.TrimSpace(strings.ReplaceAll(stripControl(body.Label), "\n", " "))
	if len(label) > maxPushLabel {
		label = strings.ToValidUTF8(label[:maxPushLabel], "")
	}
	err := s.deps.Push.Subscribe(body.Subscription, body.Events, label)
	switch {
	case errors.Is(err, ErrTooManySubscriptions):
		writeError(w, http.StatusConflict, err)
	case err != nil:
		writeError(w, http.StatusInternalServerError, err)
	default:
		w.WriteHeader(http.StatusNoContent)
	}
}

func (s *Server) handlePushUnsubscribe(w http.ResponseWriter, r *http.Request) {
	if !s.pushEnabled(w) {
		return
	}
	var body struct {
		Endpoint string `json:"endpoint"`
	}
	if !decodePushBody(w, r, &body) {
		return
	}
	if err := s.deps.Push.Unsubscribe(body.Endpoint); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handlePushTest sends a test notification to one device.
func (s *Server) handlePushTest(w http.ResponseWriter, r *http.Request) {
	if !s.pushEnabled(w) {
		return
	}
	var body struct {
		Endpoint string `json:"endpoint"`
	}
	if !decodePushBody(w, r, &body) {
		return
	}
	e, ok := s.deps.Push.Lookup(body.Endpoint)
	if !ok {
		writeError(w, http.StatusNotFound, errors.New("this device isn't subscribed"))
		return
	}
	err := s.deps.Push.Deliver(r.Context(), e, PushMessage{
		Kind:  "test",
		Title: "Notifications work",
		Body:  "unky-mo will tell you here when a session needs you.",
		Tag:   "mo-test",
		URL:   "/",
	})
	switch {
	case errors.Is(err, webpush.ErrGone):
		writeError(w, http.StatusGone, errors.New("the push service no longer knows this device; enable notifications again"))
	case err != nil:
		writeError(w, http.StatusBadGateway, err)
	default:
		w.WriteHeader(http.StatusNoContent)
	}
}

// handlePushPresence records that a page shows a session (visible and
// focused), so that device isn't notified about it.
func (s *Server) handlePushPresence(w http.ResponseWriter, r *http.Request) {
	if !s.pushEnabled(w) {
		return
	}
	var body struct {
		Endpoint string `json:"endpoint"`
		Window   string `json:"window"`
	}
	if !decodePushBody(w, r, &body) {
		return
	}
	if body.Window != "" && !windowIDRe.MatchString(body.Window) {
		writeError(w, http.StatusBadRequest, fmt.Errorf("invalid window id %q", body.Window))
		return
	}
	s.deps.Push.Heartbeat(body.Endpoint, body.Window)
	w.WriteHeader(http.StatusNoContent)
}

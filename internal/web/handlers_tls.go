package web

import (
	"errors"
	"net/http"
	"time"
)

// handleTLSInfo describes the local CA for the dashboard's "Trust on another
// device" dialog. {ca: false} when there's none to hand out (TLS disabled,
// or a cert brought with [web] cert_file).
func (s *Server) handleTLSInfo(w http.ResponseWriter, r *http.Request) {
	ca := s.deps.CA
	if ca == nil {
		writeJSON(w, map[string]any{"ca": false})
		return
	}
	writeJSON(w, map[string]any{
		"ca":          true,
		"name":        ca.Subject.CommonName,
		"fingerprint": Fingerprint(ca),
		"notAfter":    ca.NotAfter.UTC().Format(time.RFC3339),
	})
}

// handleCACert serves the local CA certificate (public, never its key) as
// DER, so a phone can fetch it from the dashboard. iOS Safari only offers to
// install a profile for application/x-x509-ca-cert, while Android 11+ won't
// install a CA from a download prompt at all: Chrome hands that type to the
// installer, which refuses, so ?as=file sends a plain download to pick in
// Settings instead.
func (s *Server) handleCACert(w http.ResponseWriter, r *http.Request) {
	ca := s.deps.CA
	if ca == nil {
		writeError(w, http.StatusNotFound, errors.New("no local CA: TLS is off or uses your own certificate"))
		return
	}
	ctype := "application/x-x509-ca-cert"
	if r.URL.Query().Get("as") == "file" {
		ctype = "application/octet-stream"
	}
	w.Header().Set("Content-Type", ctype)
	w.Header().Set("Content-Disposition", `attachment; filename="unky-mo-ca.crt"`)
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(ca.Raw)
}

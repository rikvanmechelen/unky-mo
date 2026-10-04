package web

import (
	"fmt"
	"net/http"

	"github.com/rvanmech/unky-mo/internal/claude"
)

// commandView is one entry of the composer's "/" completion list.
type commandView struct {
	Name         string   `json:"name"`
	Description  string   `json:"description"`
	ArgumentHint string   `json:"argument_hint,omitempty"`
	Aliases      []string `json:"aliases,omitempty"`
	Source       string   `json:"source"`
	// Dialog: the command opens an interactive dialog in the terminal
	// (when ArgumentHint is set, only when run without arguments), which
	// the browser can't drive.
	Dialog bool `json:"dialog,omitempty"`
}

// handleCommands lists the slash commands the live session in windowID can
// run. The checkout and session come from the state file; the list is
// cached per session, since it only changes when skills or commands are
// added.
func (s *Server) handleCommands(w http.ResponseWriter, r *http.Request) {
	windowID := r.PathValue("windowID")
	row, ok := s.windowRow(windowID)
	if !ok || row.SessionID == "" || row.Path == "" {
		writeError(w, http.StatusNotFound, fmt.Errorf("no live session in window %s", windowID))
		return
	}
	v, _ := s.cmdsCache.get(row.Path+"\x00"+row.SessionID, func() (any, error) {
		return s.deps.Commands.List(row.Path, row.SessionID), nil
	})
	cmds, _ := v.([]claude.SlashCommand)
	out := make([]commandView, 0, len(cmds))
	for _, c := range cmds {
		out = append(out, commandView{
			Name: c.Name, Description: c.Description, ArgumentHint: c.ArgumentHint,
			Aliases: c.Aliases, Source: c.Source, Dialog: c.Dialog,
		})
	}
	writeJSON(w, out)
}

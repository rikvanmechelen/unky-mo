package ops

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/rvanmech/unky-mo/internal/config"
)

// SuspendedSession describes one Claude session saved for later resume.
type SuspendedSession struct {
	WindowName  string `json:"window_name"`
	Cwd         string `json:"cwd"`
	SessionID   string `json:"session_id"`
	AgentKey    string `json:"agent_key,omitempty"`
	ProjectName string `json:"project_name,omitempty"`
	Parent      string `json:"parent,omitempty"`
}

// SuspendedState is the persistent file written on suspend and consumed on
// resume. Lives at ~/.config/unky-mo/suspended.json.
type SuspendedState struct {
	Sessions    []SuspendedSession `json:"sessions"`
	TmuxSession string             `json:"tmux_session"`
	SuspendedAt time.Time          `json:"suspended_at"`
}

// SessionToStop pairs a SuspendedSession with the live PID to SIGINT.
// PID <= 0 means the session is already dead and the signal phase is skipped.
type SessionToStop struct {
	SuspendedSession
	PID int
}

// SuspendParams drives SuspendAll.
type SuspendParams struct {
	Sessions    []SessionToStop
	TmuxSession string
}

// SuspendResult reports the outcome of SuspendAll.
type SuspendResult struct {
	Stopped int
	Saved   int
	Path    string
	Errors  []string
}

// RestoreParams drives ResumeAll.
type RestoreParams struct {
	Agents []config.AgentConfig
}

// RestoreResult reports the outcome of ResumeAll.
type RestoreResult struct {
	Resumed int
	Skipped int
	Errors  []string
}

// SuspendedStatePath returns the default path for the suspended state file.
func SuspendedStatePath() string {
	return filepath.Join(config.DefaultConfigDir(), "suspended.json")
}

// ReadSuspendedState reads and parses the suspended state file.
// Returns (nil, nil) when the file does not exist.
func ReadSuspendedState(path string) (*SuspendedState, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var s SuspendedState
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, fmt.Errorf("parse suspended state: %w", err)
	}
	return &s, nil
}

// WriteSuspendedState atomically writes the suspended state file (temp+rename).
func WriteSuspendedState(path string, s *SuspendedState) error {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	s.SuspendedAt = time.Now()
	data, err := json.Marshal(s)
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// RemoveSuspendedState deletes the suspended state file.
func RemoveSuspendedState(path string) {
	os.Remove(path)
	os.Remove(path + ".tmp")
}

// HasSuspendedState returns true if the suspended state file exists.
func HasSuspendedState(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// SuspendAll SIGINTs each session in parallel, waits for them to exit, then
// writes the suspended state file. Safe for concurrent use — each goroutine
// operates on a single PID with no shared mutation.
func SuspendAll(ctx *Context, path string, p SuspendParams) (*SuspendResult, error) {
	res := &SuspendResult{Path: path}

	// Signal all live sessions in parallel.
	var stopped atomic.Int32
	var wg sync.WaitGroup
	for _, s := range p.Sessions {
		if s.PID <= 0 {
			continue
		}
		wg.Add(1)
		go func(pid int) {
			defer wg.Done()
			SignalAndWaitExit(ctx, pid)
			stopped.Add(1)
		}(s.PID)
	}

	// Wait with a 5s timeout so stuck/zombie processes don't block forever.
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
	}

	res.Stopped = int(stopped.Load())

	// Build the persistent state from all sessions (dead or alive).
	saved := make([]SuspendedSession, len(p.Sessions))
	for i, s := range p.Sessions {
		saved[i] = s.SuspendedSession
	}

	state := &SuspendedState{
		Sessions:    saved,
		TmuxSession: p.TmuxSession,
	}
	if err := WriteSuspendedState(path, state); err != nil {
		return res, fmt.Errorf("write suspended state: %w", err)
	}
	res.Saved = len(saved)
	return res, nil
}

// ResumeAll reads the suspended state file, re-launches each session via
// LaunchSession with the agent's ResumeCmd, and deletes the file. Returns
// a result even on partial failure.
func ResumeAll(ctx *Context, path string, p RestoreParams) (*RestoreResult, error) {
	state, err := ReadSuspendedState(path)
	if err != nil {
		return &RestoreResult{}, fmt.Errorf("read suspended state: %w", err)
	}
	if state == nil || len(state.Sessions) == 0 {
		return &RestoreResult{}, nil
	}

	// Always clean up the state file, even on partial failure.
	defer RemoveSuspendedState(path)

	cfg := &config.Config{Agents: p.Agents}
	res := &RestoreResult{}

	for _, sess := range state.Sessions {
		// Skip if a window with this name already exists.
		if ctx.Tmux.WindowExists(sess.WindowName) {
			res.Skipped++
			continue
		}

		shellCmd := buildResumeCmd(cfg, sess)
		_, launchErr := LaunchSession(ctx, LaunchParams{
			WindowName:    sess.WindowName,
			Cwd:           sess.Cwd,
			ShellCmd:      shellCmd,
			AgentKey:      sess.AgentKey,
			AttachSidebar: true,
			SwitchFocus:   false,
		})
		if launchErr != nil {
			res.Skipped++
			res.Errors = append(res.Errors, fmt.Sprintf("%s: %v", sess.WindowName, launchErr))
			continue
		}
		res.Resumed++
	}

	return res, nil
}

// buildResumeCmd constructs the shell command for resuming a session using
// the appropriate agent's ResumeCmd. Falls back to the default agent.
func buildResumeCmd(cfg *config.Config, sess SuspendedSession) string {
	agent := cfg.AgentByKey(sess.AgentKey)
	if agent == nil {
		agent = cfg.DefaultAgent()
	}
	if agent != nil && agent.ResumeCmd != "" {
		return agent.ResumeCmd + " " + sess.SessionID
	}
	if agent != nil {
		return agent.Cmd
	}
	return "claude --resume " + sess.SessionID
}

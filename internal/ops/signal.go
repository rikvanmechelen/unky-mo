package ops

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
)

// signalClaude sends sig to pid, but only while pid is still a Claude
// process. The pids come from `claude agents --json` (or a capture of it
// moments earlier), and a pid that exited can be reused by any other
// process of this user: park, cleanup, lift, import and suspend would then
// stop that one instead.
func signalClaude(pid int, sig syscall.Signal) {
	if pid <= 0 || !isClaudeProcess(pid) {
		return
	}
	if proc, err := os.FindProcess(pid); err == nil {
		_ = proc.Signal(sig)
	}
}

// isClaudeProcess reports whether pid's command line runs claude: its
// program, or a script argument (node …/claude/cli.js), is named claude.
var isClaudeProcess = func(pid int) bool {
	var args []string
	if runtime.GOOS == "linux" {
		raw, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/cmdline")
		if err != nil {
			return false
		}
		args = strings.Split(strings.TrimRight(string(raw), "\x00"), "\x00")
	} else {
		out, err := exec.Command("ps", "-o", "command=", "-p", strconv.Itoa(pid)).Output()
		if err != nil {
			return false
		}
		args = strings.Fields(string(out))
	}
	for i, a := range args {
		if i > 2 {
			break
		}
		if base := filepath.Base(a); base == "claude" || strings.Contains(a, "/claude/") || strings.Contains(a, "claude-code") {
			return true
		}
	}
	return false
}

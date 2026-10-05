package web

import (
	"os/exec"
	"path/filepath"
	"testing"
)

// TestJavaScript runs the browser code's unit tests (jstests/*.test.js,
// node's built-in test runner) for the parts with no DOM, such as the
// Overview's entity model. Skipped when node isn't installed.
func TestJavaScript(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not available")
	}
	files, err := filepath.Glob("jstests/*.test.js")
	if err != nil || len(files) == 0 {
		t.Fatalf("no JavaScript tests found: %v", err)
	}
	out, err := exec.Command(node, append([]string{"--test"}, files...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("node --test: %v\n%s", err, out)
	}
}

package web

import "testing"

func TestParseInputBox(t *testing.T) {
	tests := []struct {
		fixture string
		want    string
	}{
		{"working-empty.txt", ""},
		{"working-typed.txt", "queued example"},
		{"working-queued.txt", ""},     // placeholder hint, not text
		{"working-queued-two.txt", ""}, // two queued messages
	}
	for _, tc := range tests {
		got, ok := parseInputBox(readQueueFixture(t, tc.fixture))
		if !ok || got != tc.want {
			t.Errorf("%s: got %q, %v; want %q", tc.fixture, got, ok, tc.want)
		}
	}
}

func TestParseInputBoxMultiLine(t *testing.T) {
	screen := "x\n────\n❯ line one\n  line two\n────\n  ⏸ manual mode on\n"
	if got, ok := parseInputBox(screen); !ok || got != "line one\nline two" {
		t.Errorf("got %q, %v", got, ok)
	}
}

func TestParseInputBoxFreshSessionPlaceholder(t *testing.T) {
	screen := "x\n────\n❯ Try \"how does <filepath> work?\"\n────\n  ⏸ manual mode on\n"
	if got, ok := parseInputBox(screen); !ok || got != "" {
		t.Errorf("got %q, %v", got, ok)
	}
}

func TestParseInputBoxNoBox(t *testing.T) {
	if _, ok := parseInputBox("just text\n────\nonly one rule"); ok {
		t.Error("want ok=false without two rules")
	}
}

func TestParseInputBoxLabelledTopRule(t *testing.T) {
	screen := "output\n──────────────────────────── app ─\n❯ hello\n────────────────────────────\n  ⏵⏵ auto mode on"
	text, ok := parseInputBox(screen)
	if !ok || text != "hello" {
		t.Fatalf("got %q, %v", text, ok)
	}
}

// An idle session suggests its next prompt in the box, drawn faint (probed
// on Claude Code 2.1.325). It isn't typed text.
func TestParseInputBoxFaintSuggestion(t *testing.T) {
	if got, ok := parseInputBox(readQueueFixture(t, "idle-suggestion.ansi")); !ok || got != "" {
		t.Errorf("got %q, %v; want empty", got, ok)
	}
}

func TestParseInputBoxStyledTypedText(t *testing.T) {
	tests := map[string]string{
		// Typed text in a colour whose numbers hold a 2 isn't faint.
		"truecolour": "x\n\x1b[38;5;244m────\x1b[39m\n❯ \x1b[38;2;2;22;2mhello\x1b[0m\n────\n",
		// Faint ends with 22; what follows was typed.
		"after faint": "x\n────\n❯ \x1b[2mhint\x1b[22m typed\n────\n",
		// The faint attribute carries across lines until reset.
		"faint carried": "x\n────\n❯ \x1b[2mone\ntwo\x1b[0m\n  three\n────\n",
	}
	want := map[string]string{"truecolour": "hello", "after faint": "typed", "faint carried": "three"}
	for name, screen := range tests {
		if got, ok := parseInputBox(screen); !ok || got != want[name] {
			t.Errorf("%s: got %q, %v; want %q", name, got, ok, want[name])
		}
	}
}

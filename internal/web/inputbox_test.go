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

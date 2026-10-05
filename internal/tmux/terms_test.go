package tmux

import (
	"reflect"
	"testing"
)

func TestParseTermPanes(t *testing.T) {
	out := "%4\tfish\t/home/me/ws/unky-mo\n%7\tgo\t/home/me/path with spaces\nbogus line\n\n"
	want := []TermPane{
		{ID: "%4", Command: "fish", Cwd: "/home/me/ws/unky-mo"},
		{ID: "%7", Command: "go", Cwd: "/home/me/path with spaces"},
	}
	if got := parseTermPanes(out); !reflect.DeepEqual(got, want) {
		t.Errorf("want %+v, got %+v", want, got)
	}
}

func TestParseScreen(t *testing.T) {
	// 2 lines of history + a 3-line screen, cursor on the screen's middle line.
	out := "old 1\nold 2\n\x1b[31m$\x1b[39m ls\n$ gi\n\nmo-screen 4 1 3 1\n"
	got, err := parseScreen(out)
	if err != nil {
		t.Fatal(err)
	}
	want := Screen{Text: "old 1\nold 2\n\x1b[31m$\x1b[39m ls\n$ gi\n", CursorLine: 3, CursorCol: 4, CursorVisible: true}
	if got != want {
		t.Errorf("want %+v, got %+v", want, got)
	}
	// A line of output that looks like the marker isn't taken for it: only
	// the last line is.
	got, err = parseScreen("mo-screen 9 9 9 1\n$\nmo-screen 1 0 2 0")
	if err != nil || got.CursorLine != 0 || got.CursorCol != 1 || got.CursorVisible {
		t.Errorf("got %+v, %v", got, err)
	}
	if _, err := parseScreen("no cursor here"); err == nil {
		t.Error("want an error without the cursor line")
	}
}

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

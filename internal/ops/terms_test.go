package ops

import (
	"errors"
	"testing"

	mock_ops "github.com/rvanmech/unky-mo/internal/ops/mocks"
	"go.uber.org/mock/gomock"
)

func TestSanitizeTermSessionSuffix(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"simple", "simple"},
		{"foo:bar", "foo-bar"},
		{"my.proj", "my-proj"},
		{"has space", "has-space"},
		{"a:b.c d", "a-b-c-d"},
		{"foo@feat", "foo@feat"}, // @ is NOT sanitized
	}
	for _, c := range cases {
		got := sanitizeTermSessionSuffix(c.in)
		if got != c.want {
			t.Errorf("sanitizeTermSessionSuffix(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestTermSessionName(t *testing.T) {
	cases := []struct {
		instance, id, name, want string
	}{
		{"a1b2c3d4e5f6", "@5", "foo", "mo-terms-a1b2c3d4e5f6"},
		{"", "@5", "foo", "mo-terms-5"},
		{"", "", "my.proj foo", "mo-terms-my-proj-foo"},
		{"", "", "", "mo-terms"},
	}
	for _, c := range cases {
		if got := TermSessionName(c.instance, c.id, c.name); got != c.want {
			t.Errorf("TermSessionName(%q, %q, %q) = %q, want %q", c.instance, c.id, c.name, got, c.want)
		}
	}
}

func TestEnsureTermSessionCreatesAndConfigures(t *testing.T) {
	ctrl := gomock.NewController(t)
	tm := mock_ops.NewMockTermSessionTmux(ctrl)
	tm.EXPECT().UnbindKey("popup-keys", gomock.Any()).Times(2)
	tm.EXPECT().SessionExistsNamed("mo-terms-x").Return(false)
	tm.EXPECT().NewDetachedSession("mo-terms-x", "/ws/foo").Return("%9", nil)
	tm.EXPECT().SetSessionOption("mo-terms-x", "key-table", "popup-keys").Return(nil)
	tm.EXPECT().SetSessionOption("mo-terms-x", "mouse", "on").Return(nil)
	tm.EXPECT().BindKey("popup-keys", "`", "detach-client").Return(nil)
	tm.EXPECT().BindKey("popup-keys", gomock.Any(), gomock.Any(), gomock.Any()).Times(3)

	ghost, err := EnsureTermSession(tm, "mo-terms-x", "/ws/foo")
	if err != nil || ghost != "%9" {
		t.Fatalf("want ghost %%9, got %q, %v", ghost, err)
	}
}

func TestEnsureTermSessionExisting(t *testing.T) {
	ctrl := gomock.NewController(t)
	tm := mock_ops.NewMockTermSessionTmux(ctrl)
	tm.EXPECT().UnbindKey("popup-keys", gomock.Any()).Times(2)
	tm.EXPECT().SessionExistsNamed("mo-terms-x").Return(true)

	if ghost, err := EnsureTermSession(tm, "mo-terms-x", "/ws/foo"); err != nil || ghost != "" {
		t.Fatalf("want no ghost, got %q, %v", ghost, err)
	}
}

func TestEnsureTermSessionCreateFails(t *testing.T) {
	ctrl := gomock.NewController(t)
	tm := mock_ops.NewMockTermSessionTmux(ctrl)
	tm.EXPECT().UnbindKey("popup-keys", gomock.Any()).Times(2)
	tm.EXPECT().SessionExistsNamed("mo-terms-x").Return(false)
	tm.EXPECT().NewDetachedSession("mo-terms-x", "/ws/foo").Return("", errors.New("boom"))

	if _, err := EnsureTermSession(tm, "mo-terms-x", "/ws/foo"); err == nil {
		t.Fatal("want error")
	}
}

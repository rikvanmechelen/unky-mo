package tmux

import "testing"

func TestSafeTargetUseIDForMatch(t *testing.T) {
	windows := []Window{
		{ID: "@1", Name: "moma.org.cubed"},
		{ID: "@2", Name: "alpha"},
	}
	got := SafeTarget("mo", "moma.org.cubed", windows)
	if got != "mo:@1" {
		t.Errorf("dotted name should resolve to ID target, got %q", got)
	}
}

func TestSafeTargetFallsBackForUnknown(t *testing.T) {
	windows := []Window{
		{ID: "@1", Name: "alpha"},
	}
	got := SafeTarget("mo", "beta", windows)
	if got != "mo:beta" {
		t.Errorf("unknown name should fall back to name-based target, got %q", got)
	}
}

func TestSafeTargetEmptyWindowList(t *testing.T) {
	got := SafeTarget("mo", "moma.org.cubed", nil)
	if got != "mo:moma.org.cubed" {
		t.Errorf("nil windows should fall back, got %q", got)
	}
}

func TestNeedsSafeTarget(t *testing.T) {
	if !NeedsSafeTarget("moma.org.cubed") {
		t.Error("dotted name should need safe target")
	}
	if NeedsSafeTarget("alpha") {
		t.Error("simple name should not need safe target")
	}
	if NeedsSafeTarget("alpha@feat") {
		t.Error("@ in name should not trigger safe target")
	}
}

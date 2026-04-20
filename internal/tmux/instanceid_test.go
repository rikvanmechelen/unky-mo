package tmux

import "testing"

func TestGenerateInstanceID_Length(t *testing.T) {
	id := GenerateInstanceID()
	if len(id) != 12 {
		t.Errorf("want 12-char hex string, got %d chars: %q", len(id), id)
	}
}

func TestGenerateInstanceID_HexOnly(t *testing.T) {
	id := GenerateInstanceID()
	for _, c := range id {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			t.Errorf("non-hex char %q in %q", string(c), id)
		}
	}
}

func TestGenerateInstanceID_Unique(t *testing.T) {
	seen := make(map[string]bool)
	for i := 0; i < 1000; i++ {
		id := GenerateInstanceID()
		if seen[id] {
			t.Fatalf("duplicate ID after %d iterations: %q", i, id)
		}
		seen[id] = true
	}
}

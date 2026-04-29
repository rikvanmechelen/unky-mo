package config

import "testing"

func TestSaveAndLoadAgentChoice(t *testing.T) {
	isolateConfig(t)
	if err := SaveAgentChoice("myproj", "main", "g"); err != nil {
		t.Fatal(err)
	}
	if err := SaveAgentChoice("myproj", "feat", "x"); err != nil {
		t.Fatal(err)
	}
	choices, err := LoadAgentChoices()
	if err != nil {
		t.Fatal(err)
	}
	if got := LookupAgentChoice(choices, "myproj", "main"); got != "g" {
		t.Errorf("main: want g, got %q", got)
	}
	if got := LookupAgentChoice(choices, "myproj", "feat"); got != "x" {
		t.Errorf("feat: want x, got %q", got)
	}
}

func TestLoadAgentChoicesMissing(t *testing.T) {
	isolateConfig(t)
	choices, err := LoadAgentChoices()
	if err != nil {
		t.Fatal(err)
	}
	if choices != nil {
		t.Errorf("want nil for missing file, got %v", choices)
	}
}

func TestLookupAgentChoiceNotFound(t *testing.T) {
	choices := map[string]string{"myproj:main": "g"}
	if got := LookupAgentChoice(choices, "myproj", "other"); got != "" {
		t.Errorf("want empty for unknown branch, got %q", got)
	}
}

func TestLookupAgentChoiceNilMap(t *testing.T) {
	if got := LookupAgentChoice(nil, "proj", "br"); got != "" {
		t.Errorf("want empty for nil map, got %q", got)
	}
}

func TestSaveAgentChoiceOverwrite(t *testing.T) {
	isolateConfig(t)
	SaveAgentChoice("proj", "main", "c")
	SaveAgentChoice("proj", "main", "g") // overwrite
	choices, _ := LoadAgentChoices()
	if got := LookupAgentChoice(choices, "proj", "main"); got != "g" {
		t.Errorf("want g after overwrite, got %q", got)
	}
}

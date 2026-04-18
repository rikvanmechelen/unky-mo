package tickets

import (
	"os"
	"testing"
)

// isolateHome points HOME at a temp dir so the companion file path resolves
// inside the test and doesn't clobber the user's real file.
func isolateHome(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("XDG_CONFIG_HOME", "")
	return dir
}

func TestLoadCompanionMapMissingIsEmpty(t *testing.T) {
	isolateHome(t)
	m, err := LoadCompanionProjectMap()
	if err != nil {
		t.Fatalf("LoadCompanionProjectMap: %v", err)
	}
	if len(m) != 0 {
		t.Errorf("want empty map, got %v", m)
	}
}

func TestSaveThenLoadRoundTrip(t *testing.T) {
	isolateHome(t)
	if err := SaveProjectMapEntry("jira", "OP", "moma-apps-rails"); err != nil {
		t.Fatalf("save: %v", err)
	}
	if err := SaveProjectMapEntry("jira", "INFRA", "moma-infra"); err != nil {
		t.Fatalf("save: %v", err)
	}
	m, err := LoadCompanionProjectMap()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if m["jira"]["OP"] != "moma-apps-rails" {
		t.Errorf("want OP → moma-apps-rails, got %v", m)
	}
	if m["jira"]["INFRA"] != "moma-infra" {
		t.Errorf("want INFRA → moma-infra, got %v", m)
	}
}

func TestSaveUpdatesExistingEntry(t *testing.T) {
	isolateHome(t)
	_ = SaveProjectMapEntry("jira", "OP", "old-name")
	_ = SaveProjectMapEntry("jira", "OP", "new-name")
	m, _ := LoadCompanionProjectMap()
	if m["jira"]["OP"] != "new-name" {
		t.Errorf("want updated value 'new-name', got %q", m["jira"]["OP"])
	}
}

func TestSaveRejectsEmptyFields(t *testing.T) {
	isolateHome(t)
	if err := SaveProjectMapEntry("", "OP", "foo"); err == nil {
		t.Error("want error for empty provider")
	}
	if err := SaveProjectMapEntry("jira", "", "foo"); err == nil {
		t.Error("want error for empty key")
	}
	if err := SaveProjectMapEntry("jira", "OP", ""); err == nil {
		t.Error("want error for empty project")
	}
}

func TestMergeProjectMapsConfigWins(t *testing.T) {
	cfg := map[string]string{"OP": "config-name"}
	comp := map[string]string{"OP": "companion-name", "INFRA": "moma-infra"}
	got := MergeProjectMaps(cfg, comp)
	if got["OP"] != "config-name" {
		t.Errorf("config should win for OP, got %q", got["OP"])
	}
	if got["INFRA"] != "moma-infra" {
		t.Errorf("companion should supply INFRA, got %q", got["INFRA"])
	}
}

func TestMergeProjectMapsEmptyValuesIgnored(t *testing.T) {
	cfg := map[string]string{"OP": ""}
	comp := map[string]string{"OP": "companion-name"}
	got := MergeProjectMaps(cfg, comp)
	if got["OP"] != "companion-name" {
		t.Errorf("empty config value shouldn't shadow companion, got %q", got["OP"])
	}
}

func TestLoadCompanionParsesEntries(t *testing.T) {
	dir := isolateHome(t)
	// Ensure the parent dir exists for the write.
	if err := os.MkdirAll(dir+"/.config/unky-mo", 0755); err != nil {
		t.Fatal(err)
	}
	body := `[[entry]]
provider = "jira"
jira_key = "OP"
mo_project = "moma-apps-rails"

[[entry]]
provider = "linear"
jira_key = "ENG"
mo_project = "moma-eng"
`
	if err := os.WriteFile(DefaultProjectMapPath(), []byte(body), 0644); err != nil {
		t.Fatal(err)
	}
	m, err := LoadCompanionProjectMap()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if m["jira"]["OP"] != "moma-apps-rails" {
		t.Errorf("jira entry wrong: %v", m)
	}
	if m["linear"]["ENG"] != "moma-eng" {
		t.Errorf("linear entry wrong: %v", m)
	}
}

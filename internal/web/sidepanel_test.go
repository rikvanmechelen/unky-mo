package web

import (
	"strings"
	"testing"
)

// panelBody is what Claude Code's changes side panel draws: a header, the
// changed files, then the open file's diff. It holds a "│" of its own (a
// wrapped diff line), which the crop must not mistake for the separator.
var panelBody = []string{
	"                                                        ✕",
	"11 files changed +184 -9                 source: Current ▾",
	"",
	"app/assets/stylesheets/application.css                +53",
	"app/controllers/status_controller.rb               +48 -7",
	"↓ 1 more below (opt+↓ to scroll)",
	"",
	"──────────────────────────────────────────────────────────",
	"app/controllers/status_controller.rb             [ ask ]",
	"──────────────────────────────────────────────────────────",
	" 165  def show",
	" 166 +  @rows = Check.all.map { |c| [c, c.state] }",
	" 167 +  render json: {ok: true} # a │ inside the panel's own text",
	" 168  end",
}

// withSidePanel redraws a capture the way tmux returns it once Claude Code
// opens its changes panel: the conversation padded out to the column the
// separator sits in, then the panel's text, with trailing blanks trimmed as
// tmux trims them. Claude Code rewraps the conversation into the narrower
// column; the separator goes past the widest line here instead, so the
// fixtures stay comparable word for word.
func withSidePanel(screen string) string {
	lines := strings.Split(screen, "\n")
	for len(lines) < len(panelBody) {
		lines = append(lines, "")
	}
	col := minSidePanelCol
	for _, l := range lines {
		if n := len([]rune(l)) + 1; n > col {
			col = n
		}
	}
	for i, l := range lines {
		r := []rune(l)
		row := string(r) + strings.Repeat(" ", col-len(r)) + string(sidePanelBar)
		if i < len(panelBody) {
			row += panelBody[i]
		}
		lines[i] = strings.TrimRight(row, " ")
	}
	return strings.Join(lines, "\n")
}

func TestCropSidePanelRestoresTheConversation(t *testing.T) {
	for _, f := range []string{"bash.txt", "edit.txt", "plan.txt", "read-outside.txt", "trust-folder.txt"} {
		t.Run(f, func(t *testing.T) {
			want := readFixture(t, f)
			got := cropSidePanel(withSidePanel(want))
			// The conversation is unchanged but for the padding tmux
			// trimmed off either version's lines.
			if trimRight(got) != trimRight(want) {
				t.Errorf("crop didn't restore the conversation:\ngot:\n%s\nwant:\n%s", got, want)
			}
		})
	}
}

func trimRight(screen string) string {
	lines := strings.Split(screen, "\n")
	for i, l := range lines {
		lines[i] = strings.TrimRight(l, " ")
	}
	return strings.TrimRight(strings.Join(lines, "\n"), "\n")
}

// Every parser must read a panelled screen exactly as it reads a plain one:
// the panel otherwise leaves a session unanswerable from the browser.
func TestParsersSeeThroughTheSidePanel(t *testing.T) {
	t.Run("permission dialog", func(t *testing.T) {
		for _, f := range []string{"bash.txt", "edit.txt", "webfetch.txt", "plan.txt", "plan-feedback.txt"} {
			plain := parsePermissionDialog(readFixture(t, f))
			panelled := parsePermissionDialog(withSidePanel(readFixture(t, f)))
			if !panelled.found {
				t.Fatalf("%s: no dialog found behind the side panel", f)
			}
			if panelled.sig() != plain.sig() {
				t.Errorf("%s: sig changed behind the panel\n got rows %+v\nwant rows %+v", f, panelled.rows, plain.rows)
			}
			if panelled.title != plain.title || panelled.question != plain.question ||
				panelled.cursor != plain.cursor || panelled.planFile != plain.planFile ||
				panelled.shown != plain.shown {
				t.Errorf("%s: got title=%q question=%q cursor=%d plan=%q shown=%q",
					f, panelled.title, panelled.question, panelled.cursor, panelled.planFile, panelled.shown)
			}
			for i, r := range panelled.rows {
				if p := plain.rows[i]; r.Text != p.Text || r.col != p.col || r.hint != p.hint {
					t.Errorf("%s: row %d = %+v, want %+v", f, r.N, r, p)
				}
			}
		}
	})

	t.Run("trust dialog", func(t *testing.T) {
		plain := parseStartupDialog(startupFixture(t, "trust-preapproved.txt"))
		got := parseStartupDialog(withSidePanel(startupFixture(t, "trust-preapproved.txt")))
		if !plain.found {
			t.Fatal("the fixture itself no longer parses")
		}
		if !got.found || got.folder != plain.folder || got.cursor != plain.cursor || got.sig() != plain.sig() {
			t.Errorf("got %+v, want %+v", got, plain)
		}
	})

	t.Run("message box", func(t *testing.T) {
		body := []string{"● done", "", "  ⎿  wrote notes.txt", "", "✻ Booping… (9s)", "", "", ""}
		idle := screen(body...)
		for name, want := range map[string]string{"empty": "", "typed": "a half-typed thought"} {
			s := idle
			if want != "" {
				s = strings.Replace(idle, "❯ ", "❯ "+want, 1)
			}
			text, ok := parseInputBox(withSidePanel(s))
			if !ok || text != want {
				t.Errorf("%s: parseInputBox = %q, %v; want %q, true", name, text, ok, want)
			}
		}
	})

	t.Run("mode footer", func(t *testing.T) {
		s := screen("● done", "", "", "", "", "", "", "")
		m := parseMode(withSidePanel(s))
		if m == nil || m.Mode != "auto" {
			t.Fatalf("parseMode = %+v, want auto", m)
		}
	})

	t.Run("spinner", func(t *testing.T) {
		s := screen("● done", "", "✻ Booping… (1m 44s · ↓ 3.3k tokens)", "", "", "", "", "")
		v := parseSpinner(withSidePanel(s))
		if v == nil || v.Verb != "Booping…" || v.ElapsedSeconds != 104 {
			t.Fatalf("parseSpinner = %+v", v)
		}
	})
}

// A screen with no panel must come back byte for byte: every parser reads
// the crop's output, so a wrong cut would break the common case.
func TestCropSidePanelLeavesPlainScreensAlone(t *testing.T) {
	for _, f := range []string{"bash.txt", "edit.txt", "plan.txt", "webfetch.txt", "trust-folder.txt"} {
		s := readFixture(t, f)
		if got := cropSidePanel(s); got != s {
			t.Errorf("%s: cropped a screen with no side panel", f)
		}
	}
	// A dialog is a small part of a tall screen; box drawing in a tool's
	// output must not look like a separator, even past the panel's column.
	row := "  " + strings.Repeat("x", 28) + " │ a column of its own\n"
	table := strings.Repeat(row, 20) + readFixture(t, "bash.txt")
	if got := cropSidePanel(table); got != table {
		t.Error("a table in the transcript was taken for a side panel")
	}
	for name, s := range map[string]string{
		"empty": "",
		"short": "a│b\nc│d\n",
	} {
		if got := cropSidePanel(s); got != s {
			t.Errorf("%s: cropped", name)
		}
	}
}

// Claude Code draws some dialogs over the whole pane, panel and all. Their
// rows reach past the separator's column without one, and cutting them
// there would lose the end of every long line.
func TestCropSidePanelKeepsFullWidthDialogs(t *testing.T) {
	const wide = "   Claude Code reads this project, your recent Claude sessions, and optionally your shell history and other repositories."
	s := withSidePanel(screen("● done", "", "", "", "", "", "", "")) + "\n" +
		strings.Repeat("▔", 200) + "\n   Teach auto mode about your environment?\n\n" + wide + "\n   Continue"
	got := cropSidePanel(s)
	if !strings.Contains(got, wide) {
		t.Errorf("the dialog's full-width line was cut:\n%s", got)
	}
	if strings.Contains(got, string(sidePanelBar)) {
		t.Errorf("the panel survived the crop:\n%s", got)
	}
}

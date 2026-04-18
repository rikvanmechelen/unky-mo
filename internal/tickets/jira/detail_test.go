package jira

import (
	"strings"
	"testing"
)

func TestStripHTMLPlainText(t *testing.T) {
	in := `<p>Hello <strong>world</strong>.</p><p>Second paragraph.</p>`
	got := StripHTML(in)
	if !strings.Contains(got, "Hello world") {
		t.Errorf("lost text: %q", got)
	}
	if !strings.Contains(got, "Second paragraph") {
		t.Errorf("lost second paragraph: %q", got)
	}
	// The two paragraphs should be separated by a blank line.
	if !strings.Contains(got, "\n\n") {
		t.Errorf("want paragraph break, got %q", got)
	}
}

func TestStripHTMLList(t *testing.T) {
	in := `<ul><li>first</li><li>second</li></ul>`
	got := StripHTML(in)
	if !strings.Contains(got, "- first") || !strings.Contains(got, "- second") {
		t.Errorf("list items not bulleted: %q", got)
	}
}

func TestStripHTMLBr(t *testing.T) {
	got := StripHTML("line one<br>line two")
	if got != "line one\nline two" {
		t.Errorf("br should become newline, got %q", got)
	}
}

func TestStripHTMLEntities(t *testing.T) {
	got := StripHTML("<p>code: &lt;script&gt; &amp; &quot;quotes&quot;</p>")
	if !strings.Contains(got, `<script>`) || !strings.Contains(got, `"quotes"`) {
		t.Errorf("entities not decoded: %q", got)
	}
}

func TestStripHTMLEmpty(t *testing.T) {
	if got := StripHTML(""); got != "" {
		t.Errorf("empty in → empty out, got %q", got)
	}
}

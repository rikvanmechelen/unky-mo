package review

import (
	"reflect"
	"strings"
	"testing"
)

func constNames(refs []ref) []string {
	var out []string
	for _, r := range refs {
		out = append(out, r.to)
	}
	return out
}

func TestRubyCode(t *testing.T) {
	src := `class Audio::Tour < ApplicationRecord # Comment mentions Ghost
  NAMES = %w[Phantom Specter]
  def go
    x = "Not #{Ghost} here" + 'Nor Ghost'
    y = <<~SQL
      SELECT * FROM Ghost
    SQL
    Real.find(1)
    :Symbol
    arr << Other
    @Ivar
    obj.Method
  end
=begin
Ghost in a block comment
=end
  ::TopLevel.call
end
`
	code := rubyCode(src)
	if strings.Count(code, "\n") != strings.Count(src, "\n") || len(code) != len(src) {
		t.Fatal("scanning changed line numbers or length")
	}
	got := constNames(rubyConstants(code))
	want := []string{"Audio::Tour", "ApplicationRecord", "NAMES", "Real", "Other", "::TopLevel"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
	for _, r := range rubyConstants(code) {
		if r.to == "Real" && r.line != 8 {
			t.Errorf("Real on line %d, want 8", r.line)
		}
	}
}

func TestTemplateCode(t *testing.T) {
	erb := "<h1>Ghost</h1>\n<%# Ghost comment %>\n<%= render Artwork::Card.new(x) %>\n<% if Current.user %>"
	if got := constNames(rubyConstants(erbCode(erb))); !reflect.DeepEqual(got, []string{"Artwork::Card", "Current"}) {
		t.Errorf("erb: %v", got)
	}
	haml := "%h1 Ghost\n- if Current.user\n  %p= Artwork.count\n  .note Ghost text"
	if got := constNames(rubyConstants(hamlCode(haml))); !reflect.DeepEqual(got, []string{"Current", "Artwork"}) {
		t.Errorf("haml: %v", got)
	}
}

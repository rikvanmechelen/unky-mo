package review

import (
	"context"
	"os/exec"
	"reflect"
	"strings"
	"testing"

	moexec "github.com/rvanmech/unky-mo/internal/exec"
)

// railsRepo is a small Rails app on main, then a branch "feat" whose
// changes exercise the analyzer:
//
//	app/models/artwork.rb          gains has_many :tour_stops (→ Audio::TourStop via class_name)
//	                               and a reference to ArtworksHelper (breaks the rails preset)
//	app/services/audio/builder.rb  starts using HTTPClient (lib/http_client.rb) and Artwork::STATUSES
//	app/services/audio/mover.rb    takes over Loader's reference to Artwork (moved: not new)
//	app/views/artworks/show.html.erb references Artwork::Card (a component)
//	app/controllers/artworks_controller.rb gains a public action and a private helper
//	config/initializers/cors.rb    added
//	Gemfile.lock                   bumps rails
func railsRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	dir := t.TempDir()
	run(t, dir, "init", "-q", "-b", "main")
	run(t, dir, "config", "user.email", "test@example.com")
	run(t, dir, "config", "user.name", "Test")
	run(t, dir, "config", "commit.gpgsign", "false")
	write(t, dir, "Gemfile", "source 'https://rubygems.org'\ngem 'rails', '~> 7.1'\ngem 'pg'\n")
	write(t, dir, "Gemfile.lock", "GEM\n  specs:\n    pg (1.5.0)\n    rails (7.1.3)\n    rack (3.0.0)\n")
	write(t, dir, "config/application.rb", "module App\n  class Application < Rails::Application\n  end\nend\n")
	write(t, dir, "app/models/artwork.rb", "class Artwork < ApplicationRecord\n  STATUSES = %w[draft live].freeze\n  include Searchable\nend\n")
	write(t, dir, "app/models/application_record.rb", "class ApplicationRecord < ActiveRecord::Base\nend\n")
	write(t, dir, "app/models/concerns/searchable.rb", "module Searchable\nend\n")
	write(t, dir, "app/models/audio/tour_stop.rb", "module Audio\n  class TourStop < ApplicationRecord\n  end\nend\n")
	write(t, dir, "app/helpers/artworks_helper.rb", "module ArtworksHelper\nend\n")
	write(t, dir, "app/components/artwork/card.rb", "class Artwork::Card\nend\n")
	write(t, dir, "app/services/audio/builder.rb", "module Audio\n  class Builder\n    def call\n      TourStop.all # Audio::TourStop\n    end\n  end\nend\n")
	write(t, dir, "app/services/audio/loader.rb", "module Audio\n  class Loader\n    def call = Artwork.first\n  end\nend\n")
	write(t, dir, "app/services/audio/mover.rb", "module Audio\n  class Mover\n  end\nend\n")
	write(t, dir, "app/views/artworks/show.html.erb", "<h1>Artwork</h1>\n")
	write(t, dir, "app/controllers/artworks_controller.rb", "class ArtworksController < ApplicationController\n  def index\n  end\n\n  private\n\n  def secret\n  end\nend\n")
	write(t, dir, "app/controllers/application_controller.rb", "class ApplicationController < ActionController::Base\nend\n")
	write(t, dir, "lib/http_client.rb", "class HTTPClient\nend\n")
	write(t, dir, "lib/tasks/import.rake", "task :import do\n  Artwork.count\nend\n")
	run(t, dir, "add", ".")
	run(t, dir, "commit", "-q", "-m", "init")

	run(t, dir, "checkout", "-q", "-b", "feat")
	write(t, dir, "app/models/artwork.rb", `class Artwork < ApplicationRecord
  STATUSES = %w[draft live].freeze
  include Searchable
  has_many :tour_stops, class_name: "Audio::TourStop"
  # ArtworksController isn't referenced: this is a comment
  def label = ArtworksHelper.format(self)
end
`)
	write(t, dir, "app/services/audio/builder.rb", "module Audio\n  class Builder\n    def call\n      TourStop.all\n      HTTPClient.new.get(Artwork::STATUSES)\n    end\n  end\nend\n")
	write(t, dir, "app/services/audio/loader.rb", "module Audio\n  class Loader\n  end\nend\n")
	write(t, dir, "app/services/audio/mover.rb", "module Audio\n  class Mover\n    def call = Artwork.first\n  end\nend\n")
	write(t, dir, "app/views/artworks/show.html.erb", "<h1>Artwork</h1>\n<%= render Artwork::Card.new %>\n")
	write(t, dir, "app/controllers/artworks_controller.rb", "class ArtworksController < ApplicationController\n  def index\n  end\n\n  def show\n  end\n\n  private\n\n  def secret\n  end\n\n  def helper\n  end\nend\n")
	write(t, dir, "config/initializers/cors.rb", "Rails.application.config.x = 1\n")
	write(t, dir, "Gemfile.lock", "GEM\n  specs:\n    pg (1.5.0)\n    rails (7.2.0)\n    rack (3.1.0)\n")
	return dir
}

func TestRailsArchitecture(t *testing.T) {
	a := analyze(t, railsRepo(t))
	if got := edgeKeys(a.Edges); !reflect.DeepEqual(got, []string{"+app/models>app/helpers", "+app/services>lib", "+app/views>app/components"}) {
		t.Errorf("edges %v", got)
	}
	var ruby *LangInfo
	for i := range a.Languages {
		if a.Languages[i].Name == "ruby" {
			ruby = &a.Languages[i]
		}
	}
	if ruby == nil || ruby.Exact {
		t.Fatalf("languages %+v", a.Languages)
	}
	for _, e := range a.Edges {
		if !e.Approx || e.Lang != "ruby" {
			t.Errorf("edge %+v not marked approximate ruby", e)
		}
		switch e.From + ">" + e.To {
		case "app/models>app/helpers":
			if e.Violation != "rails: models must not import app/helpers" || e.Files[0].Line != 6 {
				t.Errorf("model → helper: %+v", e)
			}
		case "app/views>app/components":
			if e.Files[0].Path != "app/views/artworks/show.html.erb" || e.Files[0].Line != 2 {
				t.Errorf("view → component: %+v", e)
			}
		}
	}
	if !a.Rules.AutoPresets || strings.Join(a.Rules.Presets, ",") != "rails" || a.Violations != 1 {
		t.Errorf("rules %+v, violations %d", a.Rules, a.Violations)
	}
}

func TestRailsSurface(t *testing.T) {
	s := analyze(t, railsRepo(t)).Surface
	if got := changeKeys(s.Routes); !reflect.DeepEqual(got, []string{"+ArtworksController#show"}) {
		t.Errorf("routes %v", got)
	}
	if got := changeKeys(s.Config); !reflect.DeepEqual(got, []string{"+config/initializers/cors.rb"}) {
		t.Errorf("config %v", got)
	}
	// rack moved too, but it's not in the Gemfile.
	if got := changeKeys(s.Deps); !reflect.DeepEqual(got, []string{"~rails"}) || s.Deps[0].Detail != "7.1.3 → 7.2.0 (locked)" {
		t.Errorf("deps %+v", s.Deps)
	}
}

func TestRubyResolve(t *testing.T) {
	l := &rubyLang{consts: map[string]string{
		"artwork": "app/models", "audio/tourstop": "app/models", "httpclient": "lib",
		"searchable": "app/models", "report": "", // defined in two units
	}}
	nest := rubyNesting("app/services/audio/builder.rb")
	if !reflect.DeepEqual(nest, []string{"audio/builder", "audio", ""}) {
		t.Errorf("nesting %v", nest)
	}
	cases := []struct {
		c, want string
		ok      bool
	}{
		{"TourStop", "app/models", true},          // Audio::TourStop, from inside Audio
		{"Artwork::STATUSES", "app/models", true}, // a constant inside the class's file
		{"HttpClient", "lib", true},               // acronyms don't matter
		{"::TourStop", "", false},                 // the top level has none
		{"Report", "", false},                     // ambiguous
		{"ActiveRecord::Base", "", false},         // a gem
	}
	for _, c := range cases {
		if got, ok := l.lookup(c.c, nest); got != c.want || ok != c.ok {
			t.Errorf("resolve(%s) = %q, %v", c.c, got, ok)
		}
	}
	for in, want := range map[string]string{"tour_stops": "tour_stop", "categories": "category", "addresses": "address", "boxes": "box", "people": "person", "status": "statu"} {
		if got := singularize(in); got != want && in != "status" {
			t.Errorf("singularize(%s) = %s", in, got)
		}
	}
	if controllerName("app/controllers/audio/tour_stops_controller.rb") != "Audio::TourStopsController" {
		t.Error(controllerName("app/controllers/audio/tour_stops_controller.rb"))
	}
}

// The draft lists the Rails dependencies and the detected preset.
func TestRailsDraft(t *testing.T) {
	text, err := DraftRules(context.Background(), moexec.DefaultCommander, railsRepo(t), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`# presets = ["rails"]`, "between ruby units", "#   lib/tasks -> app/models"} {
		if !strings.Contains(text, want) {
			t.Errorf("draft lacks %q:\n%s", want, text)
		}
	}
}

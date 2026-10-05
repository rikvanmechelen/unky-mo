package review

import (
	"reflect"
	"strings"
	"testing"

	"github.com/rvanmech/unky-mo/internal/gitfiles"
)

const cartControllerJS = `import { Controller } from "@hotwired/stimulus"

export default class extends Controller {
  static targets = ["item", "total"]

  add() {
    this.totalTarget.textContent = "1"
  }

  remove() {
    this.itemTarget.remove()
  }
}
`

const searchControllerJS = `import { Controller } from "@hotwired/stimulus"

export default class extends Controller {
  filter() {
    return this.element.value
  }
}
`

const ticketsIndexERB = `<div data-controller="cart search">
  <%= link_to "About", about_path %>
  <span data-cart-target="item"></span>
  <span data-cart-target="total"></span>
  <button data-action="click->cart#add">Add</button>
  <input data-action="keyup->search#filter">
</div>
`

// railsStimulusRepo: a Rails app with importmap and Stimulus on main.
func railsStimulusRepo(t *testing.T) string {
	t.Helper()
	dir := gitRepo(t)
	write(t, dir, "Gemfile", "source 'https://rubygems.org'\ngem 'rails'\ngem 'importmap-rails'\n")
	write(t, dir, "config/application.rb", "module App\n  class Application < Rails::Application\n  end\nend\n")
	write(t, dir, "config/importmap.rb", "pin \"application\"\npin_all_from \"app/javascript/controllers\", under: \"controllers\"\n")
	write(t, dir, "app/javascript/application.js", "import \"controllers\"\n")
	write(t, dir, "app/javascript/controllers/cart_controller.js", cartControllerJS)
	write(t, dir, "app/javascript/controllers/search_controller.js", searchControllerJS)
	write(t, dir, "app/controllers/application_controller.rb", "class ApplicationController < ActionController::Base\nend\n")
	write(t, dir, "app/controllers/tickets_controller.rb", `class TicketsController < ApplicationController
  def index
  end

  def show
    redirect_to about_path
  end

  def refund
  end
end
`)
	write(t, dir, "app/controllers/pages_controller.rb", `class PagesController < ApplicationController
  def about
  end

  def questions
  end

  def legacy
  end
end
`)
	write(t, dir, "app/views/tickets/index.html.erb", ticketsIndexERB)
	write(t, dir, "app/views/pages/faq.html.erb", "<h1>FAQ</h1>\n")
	write(t, dir, "config/routes.rb", `Rails.application.routes.draw do
  resources :tickets, only: [:index, :show] do
    post :refund, on: :member
  end
  get "about", to: "pages#about"
  get "faq", to: "pages#faq"
  get "legacy", to: "pages#legacy"
end
`)
	run(t, dir, "add", ".")
	run(t, dir, "commit", "-q", "-m", "init")
	run(t, dir, "checkout", "-q", "-b", "feat")
	return dir
}

func TestRailsRouteAndStimulusCalls(t *testing.T) {
	dir := railsStimulusRepo(t)
	// Routes: about removed (its helper still used), help added to an action
	// that doesn't exist, faq retargeted, contact added; legacy's action
	// removed while still routed.
	write(t, dir, "config/routes.rb", `Rails.application.routes.draw do
  resources :tickets, only: [:index, :show] do
    post :refund, on: :member
  end
  get "faq", to: "pages#questions"
  get "legacy", to: "pages#legacy"
  get "help", to: "pages#help"
  get "contact", to: "pages#contact"
end
`)
	write(t, dir, "app/controllers/pages_controller.rb", `class PagesController < ApplicationController
  def about
  end

  def questions
  end

  def contact
  end
end
`)
	// JS: add renamed (and changed) while the view still binds it, the
	// total target removed.
	write(t, dir, "app/javascript/controllers/cart_controller.js", strings.NewReplacer(
		`static targets = ["item", "total"]`, `static targets = ["item"]`,
		"  add() {\n    this.totalTarget.textContent = \"1\"", "  addItem() {\n    this.itemTarget.textContent = \"2\"",
	).Replace(cartControllerJS))
	// A new binding to an existing method.
	write(t, dir, "app/views/pages/faq.html.erb", "<h1>FAQ</h1>\n<div data-controller=\"search\">\n  <input data-action=\"input->search#filter\">\n</div>\n")

	cg := callGraphOf(t, dir, gitfiles.ModeBranch)
	if len(cg.Errors) > 0 || len(cg.Unparsed) > 0 {
		t.Fatalf("errors %v, unparsed %v", cg.Errors, cg.Unparsed)
	}
	st := statuses(cg)
	for id, want := range map[string]string{
		"route:GET /about":        FuncRemoved,
		"route:about":             FuncRemoved,
		"route:GET /help":         FuncAdded,
		"route:help":              FuncAdded,
		"route:GET /contact":      FuncAdded,
		"route:GET /faq":          FuncChanged,
		"PagesController#legacy":  FuncRemoved,
		"PagesController#contact": FuncAdded,
		"app/javascript/controllers/cart_controller#default.add":     FuncRemoved,
		"app/javascript/controllers/cart_controller#default.addItem": FuncAdded,
		"view:app/views/pages/faq.html.erb":                          FuncChanged,
	} {
		if st[id] != want {
			t.Errorf("status of %s = %q, want %q", id, st[id], want)
		}
	}
	calls := callKeys(cg)
	for _, w := range []string{
		"+route:GET /contact>PagesController#contact@approx",
		"+route:GET /faq>PagesController#questions@approx",
		"-route:GET /faq>view:app/views/pages/faq.html.erb@approx", // an action without a method renders its template
		"+route:contact>route:GET /contact@approx",                 // a helper names its route
		"-route:GET /about>PagesController#about@approx",
		// A view binding a JS method, and an unchanged view binding a
		// changed controller class.
		"+view:app/views/pages/faq.html.erb>app/javascript/controllers/search_controller#default.filter@approx",
		"view:app/views/tickets/index.html.erb>app/javascript/controllers/cart_controller#default@approx",
	} {
		if !contains(calls, w) {
			t.Errorf("missing call %s in %v", w, calls)
		}
	}
	findings := findingKeys(cg)
	for _, w := range []string{
		"removed-called:route:about app/controllers/tickets_controller.rb app/views/tickets/index.html.erb",
		"removed-called:PagesController#legacy config/routes.rb",
		"route-without-action:route:GET /help config/routes.rb",
		"removed-called:app/javascript/controllers/cart_controller#default.add app/views/tickets/index.html.erb",
		"removed-called:target:cart.total app/views/tickets/index.html.erb",
		"stimulus-unbound:view:app/views/tickets/index.html.erb app/views/tickets/index.html.erb app/views/tickets/index.html.erb",
	} {
		if !contains(findings, w) {
			t.Errorf("missing finding %s in\n%s", w, strings.Join(findings, "\n"))
		}
	}
	for _, f := range findings {
		if strings.HasPrefix(f, "route-without-action:route:GET /legacy") {
			t.Errorf("a removed action still routed is removed-called only: %s", f)
		}
		if strings.HasPrefix(f, "untested:route:") {
			t.Errorf("routes are entry points: %s", f)
		}
	}
	for _, f := range cg.Funcs {
		switch f.ID {
		case "app/javascript/controllers/search_controller#default.filter":
			if f.Path != "app/javascript/controllers/search_controller.js" || f.Lang != "node" || f.Line != 4 || f.Name != "search_controller.filter" {
				t.Errorf("cross-language callee %+v", f)
			}
		case "route:GET /help":
			if f.Name != "GET /help" || f.Path != "config/routes.rb" || f.Line != 7 || f.Lang != "ruby" {
				t.Errorf("route node %+v", f)
			}
		case "route:about":
			if f.Name != "about_path" || !f.Before {
				t.Errorf("helper node %+v", f)
			}
		case "target:cart.total":
			if f.Status != FuncRemoved || f.Path != "app/javascript/controllers/cart_controller.js" {
				t.Errorf("target node %+v", f)
			}
		}
	}
	seen := map[string]int{}
	for _, f := range cg.Funcs {
		seen[f.ID]++
		if seen[f.ID] > 1 {
			t.Errorf("node %s twice", f.ID)
		}
	}
	// Serious findings first.
	if len(cg.Findings) > 0 && cg.Findings[0].Kind != FindingRemovedCalled {
		t.Errorf("first finding %+v", cg.Findings[0])
	}
}

// A change to the JS alone: the Ruby side is built for the cross-language
// pass, and a view still binding a renamed method is caught.
func TestStimulusRenameOnlyJS(t *testing.T) {
	dir := railsStimulusRepo(t)
	write(t, dir, "app/javascript/controllers/search_controller.js", strings.Replace(searchControllerJS, "filter()", "search()", 1))
	cg := callGraphOf(t, dir, gitfiles.ModeBranch)
	if len(cg.Errors) > 0 {
		t.Fatalf("errors %v", cg.Errors)
	}
	// The method's header is part of its body: removed and added, not renamed.
	if got := statuses(cg); !reflect.DeepEqual(got, map[string]string{
		"app/javascript/controllers/search_controller#default.filter": FuncRemoved,
		"app/javascript/controllers/search_controller#default.search": FuncAdded,
	}) {
		t.Errorf("statuses %v", got)
	}
	for _, w := range []string{
		"removed-called:app/javascript/controllers/search_controller#default.filter app/views/tickets/index.html.erb",
		"stimulus-unbound:view:app/views/tickets/index.html.erb app/views/tickets/index.html.erb",
	} {
		if !contains(findingKeys(cg), w) {
			t.Errorf("missing %s in %v", w, findingKeys(cg))
		}
	}
	found := false
	for _, f := range cg.Funcs {
		if f.ID == "view:app/views/tickets/index.html.erb" {
			found = f.Path == "app/views/tickets/index.html.erb" && f.Lang == "ruby"
		}
	}
	if !found {
		t.Errorf("the view isn't a node: %+v", cg.Funcs)
	}
	if len(cg.Languages) != 1 || cg.Languages[0].Name != "node" {
		t.Errorf("languages %+v", cg.Languages)
	}
}

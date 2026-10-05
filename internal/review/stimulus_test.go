package review

import (
	"reflect"
	"testing"
)

func TestStimulusIdentifier(t *testing.T) {
	for p, want := range map[string]string{
		"app/javascript/controllers/cart_controller.js":                "cart",
		"app/javascript/controllers/admin/user_list_controller.js":     "admin--user-list",
		"app/javascript/controllers/date-picker-controller.ts":         "date-picker",
		"app/javascript/admin/controllers/nav_controller.js":           "nav",
		"engines/shop/app/javascript/controllers/basket_controller.js": "basket",
		"app/components/nav_component/nav_component_controller.js":     "nav-component--nav-component",
		"app/components/dropdown_controller.js":                        "dropdown",
	} {
		if got, ok := stimulusIdentifier(p); !ok || got != want {
			t.Errorf("%s: %q %v, want %q", p, got, ok, want)
		}
	}
	for _, p := range []string{
		"app/javascript/controllers/index.js",
		"app/javascript/controllers/application.js",
		"app/javascript/lib/cart_controller.js",
		"app/controllers/cart_controller.rb",
		"app/javascript/controllers/_controller.js",
	} {
		if got, ok := stimulusIdentifier(p); ok {
			t.Errorf("%s: %q, want none", p, got)
		}
	}
}

func bindingKeys(bs []stimBinding) []string {
	var out []string
	for _, b := range bs {
		out = append(out, b.ident+" "+b.name+" "+itoa(b.line))
	}
	return out
}

func itoa(n int) string {
	if n < 10 {
		return string(rune('0' + n))
	}
	return itoa(n/10) + string(rune('0'+n%10))
}

func TestStimulusBindings(t *testing.T) {
	erb := `<div data-controller="cart admin--user-list" class="x">
  <button data-action="click->cart#add keyup.enter->cart#update:prevent cart#reset">Add</button>
  <span data-cart-target="item total"></span>
  <%= tag.div data: { controller: "search", action: "input->search#filter", "search-target": "field", search_target: "list" } %>
  <%= button_tag "Go", data_action: "click->search#go" %>
  <div data-action="<%= dynamic %>#x" data-controller="#{nope}"></div>
  <%= link_to "x", url_for(controller: "tickets", action: "show") %>
  <a data-action="click->bad#">x</a>
</div>
`
	got := bindingKeys(stimulusBindings(erb))
	want := []string{
		"cart <controller> 1",
		"admin--user-list <controller> 1",
		"cart action:add 2",
		"cart action:update 2",
		"cart action:reset 2",
		"cart target:item 3",
		"cart target:total 3",
		"search <controller> 4",
		"search action:filter 4",
		"search target:field 4",
		"search target:list 4",
		"search action:go 5",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("bindings:\n%v\nwant\n%v", got, want)
	}

	haml := `%div{ data: { controller: 'modal', action: 'keydown@window->modal#close' } }
  %button(data-action="modal#open" data-modal-target="dialog") Open
  %span{ "data-controller" => "tooltip", :data => { :action => "mouseover->tooltip#show" } }
`
	got = bindingKeys(stimulusBindings(haml))
	want = []string{
		"modal <controller> 1",
		"modal action:close 1",
		"modal action:open 2",
		"modal target:dialog 2",
		"tooltip <controller> 3",
		"tooltip action:show 3",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("haml bindings:\n%v\nwant\n%v", got, want)
	}
}

func TestRbStimulusOwners(t *testing.T) {
	src := `class NavComponent < ViewComponent::Base
  def call
    tag.nav(data: { controller: "nav" }) { content }
  end

  def toggle_button
    tag.button "Menu", data: { action: "nav#toggle" }
  end
end
`
	f := (&rbCalls{}).scanFile("app/components/nav_component.rb", src)
	got := map[string][]string{}
	for _, d := range f.Defs {
		for _, c := range d.Calls {
			if c.Recv == "stimulus:nav" {
				got[d.Name] = append(got[d.Name], c.Name)
			}
		}
	}
	if want := map[string][]string{"call": {"<controller>"}, "toggle_button": {"action:toggle"}}; !reflect.DeepEqual(got, want) {
		t.Errorf("owners %v, want %v", got, want)
	}
	v := (&rbCalls{}).scanFile("app/views/x/show.html.erb", "<p data-controller=\"x\"><%= y %></p>\n")
	if len(v.Defs) != 1 || len(v.Defs[0].Calls) != 2 || v.Defs[0].Calls[1].Recv != "stimulus:x" {
		t.Errorf("view calls %+v", v.Defs)
	}
}

func TestStimulusStaticTargets(t *testing.T) {
	src := `import { Controller } from "@hotwired/stimulus"

export default class extends Controller {
  static targets = ["item", 'total']
  static values = { url: String }

  add() {}
}

class Helper {
  static targets = ["nope"]
}
`
	f := (&jsCalls{}).scanFile("app/javascript/controllers/cart_controller.js", src)
	if want := map[string][]string{"default": {"item", "total"}, "Helper": {"nope"}}; !reflect.DeepEqual(f.Targets, want) {
		t.Errorf("targets %v, want %v", f.Targets, want)
	}
	if c := stimClass(&f); c != "default" {
		t.Errorf("class %q", c)
	}
	named := (&jsCalls{}).scanFile("x.js", "export default class CartController extends Controller {\n  add() {}\n}\n")
	if c := stimClass(&named); c != "CartController" {
		t.Errorf("named class %q", c)
	}
}

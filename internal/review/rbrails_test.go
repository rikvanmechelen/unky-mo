package review

import (
	"context"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"

	moexec "github.com/rvanmech/unky-mo/internal/exec"
	"github.com/rvanmech/unky-mo/internal/gitfiles"
)

func TestRbParseChain(t *testing.T) {
	locals := map[string]string{"t": "Ticket.find", "x": "", "rel": "Ticket.where"}
	for src, want := range map[string]string{
		"@ticket.event.venue":                "@ticket.event.venue",
		"Ticket.where(a: 1).order(:b).first": "Ticket.where.order.first",
		"t.event":                            "Ticket.find.event",
		"x.event":                            "", // a local nothing typed
		"items.first":                        "self.items.first",
		"@tickets[0].event":                  "@tickets.[].event",
		"@tickets.select { |t| t.ok }.first": "@tickets.select.first",
		"policy_scope(Ticket).active":        "Ticket.all.active",
		"Admin::Ticket.new(x)&.save":         "Admin::Ticket.new.save",
		"raise Foo":                          "",
		"rel.each do |r|":                    "rel.each", // a local's own name isn't substituted twice
	} {
		got, _ := rbParseChain(src, 0, locals, nil)
		if src == "rel.each do |r|" {
			want = "Ticket.where.each"
		}
		if got != want {
			t.Errorf("rbParseChain(%q) = %q, want %q", src, got, want)
		}
	}
	// Assignments type a local only when the chain is the whole value.
	for src, want := range map[string]string{
		"a = Ticket.find(params[:id])":  "Ticket.find",
		"a ||= @ticket.event":           "@ticket.event",
		"a = Shift.new excluded, other": "Shift.new",
		"a = Ticket.new(\n":             "Ticket.new",
		"a = b + 1":                     "",
		"a = Ticket.find(1) if ok":      "Ticket.find",
		"a = foo ? Ticket.first : nil":  "",
		"a = @tickets.map { |t| t.id }": "@tickets.map",
	} {
		ls := map[string]string{}
		rbAddLocals(src, ls)
		if ls["a"] != want {
			t.Errorf("%q types a as %q, want %q", src, ls["a"], want)
		}
	}
	// Iterators type their block's first parameter.
	for src, want := range map[string]string{
		"@tickets.each do |t|":                          "@tickets.[]",
		"@event.tickets.each_with_index { |t, i| t.x }": "@event.tickets.[]",
		"Ticket.active.find_each do |t|":                "Ticket.active.[]",
		"items.map { |t| t }":                           "self.items.[]",
		"@tickets.group_by(&:x).each do |t|":            "@tickets.group_by.[]", // typed later: group_by gives no collection
		"foo do |t|":                                    "",
	} {
		ls := map[string]string{}
		rbAddLocals(src, ls)
		if ls["t"] != want {
			t.Errorf("%q types t as %q, want %q", src, ls["t"], want)
		}
	}
}

func TestRbScanRails(t *testing.T) {
	l := &rbCalls{}
	model := `class Ticket < ApplicationRecord
  include AASM
  belongs_to :event
  belongs_to :owner, class_name: "Admin::User"
  belongs_to :holder, polymorphic: true
  has_many :seats, -> { order(:row) }, dependent: :destroy
  has_many :buyers, through: :orders, source: :user
  has_one_attached :pdf
  enum status: { active: 0, archived: 1 }
  enum :kind, %i[adult child], prefix: true
  enum :tier, [:gold, :silver], suffix: :level
  delegate :name, :city, to: :venue, prefix: true
  delegate :title, to: :event
  after_commit -> { notify }, on: :create
  validates :kind, inclusion: { in: kinds_list }
  before_save do
    normalize
  end

  aasm column: :state do
    state :reserved, :held, initial: true
    event :pay, after: :send_receipt do
      transitions from: :reserved, to: :paid, guard: [:payable?, :open?]
    end
  end
end
`
	f := l.scanFile("app/models/ticket.rb", model)
	if !f.OK {
		t.Fatal("not OK")
	}
	var names []string
	for _, d := range f.Defs {
		if _, _, _, ok := rbSynName(d.Name); ok {
			names = append(names, qualify(d.Owner, d.Name))
		}
	}
	want := []string{
		"Ticket.<belongs_to>event", "Ticket.<belongs_to:Admin::User>owner", "Ticket.<belongs_to:?>holder",
		"Ticket.<has_many>seats", "Ticket.<has_many:~orders/user>buyers", "Ticket.<attachment>pdf",
		"Ticket.<enum>active?", "Ticket.<enum>archived?", "Ticket.self.<enum_values>statuses",
		"Ticket.<enum>kind_adult?", "Ticket.<enum>kind_child?", "Ticket.self.<enum_values>kinds",
		"Ticket.<enum>gold_level?", "Ticket.<enum>silver_level?", "Ticket.self.<enum_values>tiers",
		"Ticket.<delegate:venue>venue_name", "Ticket.<delegate:venue>venue_city", "Ticket.<delegate:event>title",
		"Ticket.<aasm_state>reserved?", "Ticket.<aasm_state>held?", "Ticket.<aasm_event>pay",
	}
	if !reflect.DeepEqual(names, want) {
		t.Errorf("synthetic defs\n%v\nwant\n%v", names, want)
	}
	got := rbDefsOf(f)
	for k, w := range map[string]string{
		"Ticket.<delegate:venue>venue_name": "venue self.venue.name [12-12]",
		"Ticket.<aasm_event>pay":            "self.new.send_receipt@ref self.new.payable?@ref self.new.open?@ref [22-22]",
	} {
		if got[k] != w {
			t.Errorf("%s = %q, want %q", k, got[k], w)
		}
	}
	// A callback's lambda and block run on the record; its other
	// arguments in the class.
	cls := got["Ticket"]
	for _, w := range []string{"self.new.notify", "kinds_list", "self.new.normalize"} {
		if !strings.Contains(" "+cls+" ", " "+w+" ") {
			t.Errorf("class calls %q lack %s", cls, w)
		}
	}
	if strings.Contains(cls, "self.new.kinds_list") {
		t.Errorf("a macro argument moved to the instance: %q", cls)
	}

	ctrl := `class TicketsController < ApplicationController
  before_action :set_ticket

  def index
    @tickets = policy_scope(Ticket)
  end

  def show
    authorize @ticket
    render "tickets/card" if x
  end

  def edit
    authorize @ticket, :update?
    respond_to do |format|
      format.html
      format.json { render json: @ticket }
    end
  end

  def create
    redirect_to tickets_path
  end

  private

  def set_ticket
    @ticket = Ticket.find(params[:id])
  end
end
`
	got = rbDefsOf(l.scanFile("app/controllers/tickets_controller.rb", ctrl))
	tpl := " <template>.0 <template>.1 <template>.2 <template>.3"
	for k, w := range map[string]string{
		"TicketsController.index":      "policy_scope <ivar>@tickets.Ticket.all@ref <policy_scope>Ticket.<resolve" + tpl + " [4-6]",
		"TicketsController.show":       "authorize <authorize>@ticket.<show? render x <render>.s:tickets/card [8-11]",
		"TicketsController.edit":       "authorize <authorize>@ticket.<update? respond_to ?.html ?.json render" + tpl + " [13-19]",
		"TicketsController.create":     "redirect_to tickets_path [21-23]",
		"TicketsController.set_ticket": "Ticket.find params <ivar>@ticket.Ticket.find@ref [27-29]",
	} {
		if strings.Join(strings.Fields(got[k]), " ") != w {
			t.Errorf("%s = %q\nwant %q", k, got[k], w)
		}
	}

	view := `<%= render "shared/card" %>
<%= render partial: "row", collection: @tickets %>
<%= render @tickets %>
<%= render(BadgeComponent.new(t: 1)) %>
<% if policy(@ticket).edit? %><%= render :x %><% end %>
<%= json.partial! "tickets/ticket", ticket: t %>
`
	got = rbDefsOf(l.scanFile("app/views/tickets/index.html.erb", view))
	if w := "render <render>.s:shared/card render <render>.p:row render <render>.e:@tickets render BadgeComponent.new policy <policy>@ticket.<edit? render <render>.a:x json self.json.partial! t <render>.p:tickets/ticket [1-6]"; got["<view>"] != w {
		t.Errorf("view = %q\nwant %q", got["<view>"], w)
	}

	tests := `require "test_helper"

class TicketsControllerTest < ActionController::TestCase
  setup do
    @ticket = create(:ticket)
  end

  test "shows a ticket" do
    get :show, params: { id: @ticket }
    assert_response :success
  end

  def test_old_style
    post :create
  end
end

describe "x" do
  it 'works' do
    run
  end
end
`
	got = rbDefsOf(l.scanFile("test/controllers/tickets_controller_test.rb", tests))
	for k, w := range map[string]string{
		"TicketsControllerTest.setup":                 "setup create <ivar>@ticket.self.create@ref <factory>.<ticket@ref [4-6]",
		`TicketsControllerTest.test "shows a ticket"`: "test get <action>.<show assert_response [8-11]",
		"TicketsControllerTest.test_old_style":        "post <action>.<create [13-15]",
		`it 'works'`:                                  "it run [19-21]",
	} {
		if got[k] != w {
			t.Errorf("%s = %q\nwant %q", k, got[k], w)
		}
	}

	factories := "FactoryBot.define do\n  factory :ticket do\n    title { \"x\" }\n  end\n  factory :buyer, class: \"Admin::User\" do\n  end\nend\n"
	got = rbDefsOf(l.scanFile("test/factories/tickets.rb", factories))
	if w := "FactoryBot.define factory <factory>ticket.Ticket@ref title factory <factory>buyer.Admin::User@ref [1-5]"; got["<main>"] != w {
		t.Errorf("factories = %q\nwant %q", got["<main>"], w)
	}
}

func TestRbSchemaScan(t *testing.T) {
	src := `ActiveRecord::Schema[7.1].define(version: 2024_01_01_000000) do
  create_table "artworks", force: :cascade do |t|
    t.string "title", null: false
    t.references "artist"
    t.index ["title"], name: "index_artworks_on_title"
  end

  create_table "museum_locations" do |t|
    t.text "summary"
  end
end
`
	f := (&rbCalls{}).scanFile("db/schema.rb", src)
	var got []string
	for _, d := range f.Defs {
		got = append(got, d.Owner+"."+d.Name+rangeOf(d))
	}
	want := []string{"<table>artworks.<column>title [3-3]", "<table>artworks.<column>artist_id [4-4]", "<table>museum_locations.<column>summary [9-9]"}
	if !f.OK || !reflect.DeepEqual(got, want) {
		t.Errorf("schema defs %v, want %v", got, want)
	}
	l := &rbCalls{tables: map[string]string{"legacy_art": "Artwork"}}
	for table, model := range map[string]string{"artworks": "Artwork", "museum_locations": "MuseumLocation", "legacy_art": "Artwork", "people": "Person"} {
		if got := l.tableModel(table); got != model {
			t.Errorf("tableModel(%s) = %s, want %s", table, got, model)
		}
	}
	if got := l.id("db/schema.rb", &f.Defs[2]); got != "MuseumLocation#summary" {
		t.Errorf("column id %s", got)
	}
	if got := l.display("db/schema.rb", &f.Defs[0]); got != "Artwork.title (column)" {
		t.Errorf("column display %s", got)
	}
}

// rbRailsApp writes a small Rails app covering each typing source and
// each implicit call.
func rbRailsApp(t *testing.T) string {
	t.Helper()
	dir := gitRepo(t)
	files := map[string]string{
		"Gemfile":                          "source 'https://rubygems.org'\ngem 'rails'\n",
		"config/application.rb":            "module App\n  class Application < Rails::Application\n  end\nend\n",
		"app/models/application_record.rb": "class ApplicationRecord < ActiveRecord::Base\n  self.abstract_class = true\nend\n",
		"app/models/venue.rb": `class Venue < ApplicationRecord
  has_many :events

  def label
    name
  end
end
`,
		"app/models/event.rb": `class Event < ApplicationRecord
  belongs_to :venue
  has_many :tickets
  has_many :buyers, through: :tickets, source: :owner
  scope :upcoming, -> { where(upcoming: true) }
  delegate :label, to: :venue, prefix: true

  def headline
    venue.label
  end

  def top_buyer
    buyers.first.greet
  end
end
`,
		"app/models/user.rb": `class User < ApplicationRecord
  has_many :owned, class_name: "Ticket"

  def greet
    :hi
  end
end
`,
		"app/models/ticket.rb": `class Ticket < ApplicationRecord
  include AASM
  belongs_to :event
  belongs_to :owner, class_name: "User"
  belongs_to :holder, polymorphic: true
  enum status: { active: 0, archived: 1 }
  enum :kind, [:adult, :child], prefix: true
  after_commit -> { notify }, on: :create
  broadcasts_to ->(t) { :tickets }

  aasm column: :state do
    state :reserved, initial: true
    state :paid
    event :pay, after: :send_receipt do
      transitions from: :reserved, to: :paid, guard: :payable?
    end
  end

  def notify; end

  def send_receipt; end

  def payable?
    true
  end

  def summary
    event.venue.label
    owner.greet
    holder.anything
    self.title = title
    active?
    kind_child?
    may_pay?
    paid?
    Ticket.statuses
    event.venue_label
  end
end
`,
		"db/schema.rb": `ActiveRecord::Schema[7.1].define(version: 1) do
  create_table "tickets" do |t|
    t.string "title"
    t.references "event"
  end

  create_table "events" do |t|
    t.string "name"
  end
end
`,
		"app/controllers/application_controller.rb": "class ApplicationController < ActionController::Base\nend\n",
		"app/controllers/tickets_controller.rb": `class TicketsController < ApplicationController
  before_action :set_ticket, only: %i[show edit]

  def index
    @tickets = policy_scope(Ticket)
    @tickets.each { |t| t.summary }
    Ticket.where(x: 1).upcoming_missing
    Event.upcoming.first.headline
  end

  def show
    authorize @ticket
    @ticket.event.headline
    @ticket.unknown_thing.top_buyer
  end

  def edit
    authorize @ticket, :update?
    render :form
  end

  def create
    TicketJob.perform_later(1)
    TicketJob.set(wait: 1).perform_later(2)
    TicketMailer.with(t: 1).receipt.deliver_later
    redirect_to "/"
  end

  private

  def set_ticket
    @ticket = Ticket.find(params[:id])
  end
end
`,
		"app/views/tickets/index.html.erb": "<% @tickets.each do |t| %><%= t.summary %><% end %>\n",
		"app/views/tickets/show.html.erb": `<h1><%= @ticket.event.headline %></h1>
<%= render "shared/card" %>
<%= render @ticket %>
<%= render partial: "row" %>
<% if policy(@ticket).edit? %>edit<% end %>
<%= render(BadgeComponent.new(t: 1)) %>
`,
		"app/views/tickets/form.html.erb":          "<p>form</p>\n",
		"app/views/tickets/_ticket.html.erb":       "<p>ticket</p>\n",
		"app/views/tickets/_row.html.erb":          "<p>row</p>\n",
		"app/views/shared/_card.html.erb":          "<p>card</p>\n",
		"app/views/ticket_mailer/receipt.html.erb": "<p>receipt</p>\n",
		"app/views/ticket_mailer/receipt.text.erb": "receipt\n",
		"app/policies/application_policy.rb": `class ApplicationPolicy
  def show?
    true
  end

  class Scope
    def resolve
      :all
    end
  end
end
`,
		"app/policies/ticket_policy.rb": `class TicketPolicy < ApplicationPolicy
  def update?
    true
  end

  def edit?
    update?
  end

  class Scope < ApplicationPolicy::Scope
    def resolve
      :mine
    end
  end
end
`,
		"app/jobs/application_job.rb":             "class ApplicationJob < ActiveJob::Base\nend\n",
		"app/jobs/ticket_job.rb":                  "class TicketJob < ApplicationJob\n  def perform(id)\n    id\n  end\nend\n",
		"app/mailers/application_mailer.rb":       "class ApplicationMailer < ActionMailer::Base\nend\n",
		"app/mailers/ticket_mailer.rb":            "class TicketMailer < ApplicationMailer\n  def receipt\n    mail(to: \"x\")\n  end\nend\n",
		"app/components/badge_component.rb":       "class BadgeComponent < ViewComponent::Base\n  def initialize(t:)\n    @t = t\n  end\nend\n",
		"app/components/badge_component.html.erb": "<span><%= @t %></span>\n",
		"test/controllers/tickets_controller_test.rb": `require "test_helper"

class TicketsControllerTest < ActionController::TestCase
  setup do
    @ticket = create(:ticket)
  end

  test "shows" do
    get :show, params: { id: @ticket }
    assert_response :success
  end
end
`,
		"test/factories/tickets.rb": "FactoryBot.define do\n  factory :ticket do\n    title { \"x\" }\n  end\n\n  factory :buyer, class: \"User\" do\n  end\nend\n",
	}
	for p, src := range files {
		write(t, dir, p, src)
	}
	run(t, dir, "add", ".")
	run(t, dir, "commit", "-q", "-m", "init")
	return dir
}

// rbResolveAll resolves every Ruby call of a checkout: "from > to@line" for
// each resolved call, "from ? recv.name@line" for each counted unresolved
// one (framework calls, metadata and quiet sites left out).
func rbResolveAll(t *testing.T, dir string) (edges, unresolved []string) {
	t.Helper()
	r := &repo{ctx: context.Background(), cmd: moexec.DefaultCommander, root: dir}
	idx := newIndex(r)
	l := &rbCalls{}
	if !l.setup(idx) {
		t.Fatal("not a Rails app")
	}
	hr := newHResolver(l, idx)
	for _, p := range hr.paths {
		f := hr.files[p]
		for i := range f.Defs {
			d := &f.Defs[i]
			from := l.id(p, d)
			for _, c := range d.Calls {
				to, want := l.resolve(hr, p, d, c)
				switch {
				case to == hExternal:
				case to != "":
					edges = append(edges, from+" > "+to+"@"+strconv.Itoa(c.Line))
				case !c.Ref:
					counted := hr.byName[c.Name] != nil
					for _, w := range want {
						counted = counted || !strings.HasPrefix(w, "~")
					}
					if counted {
						unresolved = append(unresolved, from+" ? "+c.Recv+"."+c.Name+"@"+strconv.Itoa(c.Line))
					}
				}
			}
		}
	}
	sort.Strings(edges)
	sort.Strings(unresolved)
	return edges, unresolved
}

func TestRbRailsResolution(t *testing.T) {
	edges, unresolved := rbResolveAll(t, rbRailsApp(t))
	for _, w := range []string{
		// Associations and a chain through two of them.
		"Ticket#summary > Ticket#event@28",
		"Ticket#summary > Event#venue@28",
		"Ticket#summary > Venue#label@28",
		"Ticket#summary > Ticket#owner@29", // class_name
		"Ticket#summary > User#greet@29",
		"Ticket#summary > Ticket#holder@30", // polymorphic: the reader only
		"Event#top_buyer > Event#buyers@13", // through: source
		"Event#top_buyer > User#greet@13",
		// Columns, enums, AASM, delegate.
		"Ticket#summary > Ticket#title@31",
		"Ticket#summary > Ticket#active?@32",
		"Ticket#summary > Ticket#kind_child?@33",
		"Ticket#summary > Ticket#pay@34",
		"Ticket#summary > Ticket#paid?@35",
		"Ticket#summary > Ticket.statuses@36",
		"Ticket#summary > Event#venue_label@37",
		"Event#venue_label > Event#venue@6",
		"Event#venue_label > Venue#label@6",
		"Ticket#pay > Ticket#send_receipt@14",
		"Ticket#pay > Ticket#payable?@15",
		// A callback lambda runs on the record; broadcasts render its partial.
		"Ticket > Ticket#notify@8",
		"Ticket > view:app/views/tickets/_ticket.html.erb@9",
		// Finders, scopes, policy_scope and block parameters.
		"TicketsController#index > TicketPolicy::Scope#resolve@5",
		"TicketsController#index > Ticket#summary@6",
		"TicketsController#index > Event.upcoming@8",
		"TicketsController#index > Event#headline@8",
		"TicketsController#index > view:app/views/tickets/index.html.erb@4",
		// An instance variable from a before_action, in the action and its view.
		"TicketsController#show > Ticket#event@13",
		"TicketsController#show > Event#headline@13",
		"TicketsController#show > ApplicationPolicy#show?@12",
		"TicketsController#show > view:app/views/tickets/show.html.erb@11",
		"view:app/views/tickets/show.html.erb > Event#headline@1",
		"view:app/views/tickets/index.html.erb > Ticket#summary@1",
		// render, partials, policy(x), components.
		"TicketsController#edit > TicketPolicy#update?@18",
		"TicketsController#edit > view:app/views/tickets/form.html.erb@19",
		"view:app/views/tickets/show.html.erb > view:app/views/shared/_card.html.erb@2",
		"view:app/views/tickets/show.html.erb > view:app/views/tickets/_ticket.html.erb@3",
		"view:app/views/tickets/show.html.erb > view:app/views/tickets/_row.html.erb@4",
		"view:app/views/tickets/show.html.erb > TicketPolicy#edit?@5",
		"view:app/views/tickets/show.html.erb > BadgeComponent#initialize@6",
		"BadgeComponent#initialize > view:app/components/badge_component.html.erb@2",
		// Jobs and mailers.
		"TicketsController#create > TicketJob#perform@23",
		"TicketsController#create > TicketJob#perform@24",
		"TicketsController#create > TicketMailer#receipt@25",
		"TicketMailer#receipt > view:app/views/ticket_mailer/receipt.html.erb@2",
		"TicketMailer#receipt > view:app/views/ticket_mailer/receipt.text.erb@2",
		// Tests: a functional test's action, factories.
		`TicketsControllerTest#test "shows" > TicketsController#show@9`,
		"TicketsControllerTest#setup > Ticket@5",
		"main:test/factories/tickets.rb > Ticket@2",
		"main:test/factories/tickets.rb > User@6",
	} {
		if !contains(edges, w) {
			t.Errorf("missing %s", w)
		}
	}
	for _, e := range edges {
		for _, bad := range []string{
			"> anything", // holder.anything: polymorphic
			"TicketsController#show > User#greet",
			"TicketsController#show > Event#top_buyer", // after an unknown step
			"TicketsController#edit > view:app/views/tickets/edit",
			"TicketsController#create > view:",
			"upcoming_missing",
		} {
			if strings.Contains(e+" ", bad) {
				t.Errorf("unexpected edge %s", e)
			}
		}
	}
	// Framework calls on known types aren't counted; repo names after an
	// unknown step are.
	for _, u := range unresolved {
		for _, bad := range []string{".find@", ".where@", ".each@", ".perform_later@", ".deliver_later@", ".set@", ".with@", "authorize", "assert"} {
			if strings.Contains(u, bad) {
				t.Errorf("framework call counted: %s", u)
			}
		}
	}
	if !contains(unresolved, "TicketsController#show ? @ticket.unknown_thing.top_buyer@14") {
		t.Errorf("an unknown step's call should stay unresolved: %v", unresolved)
	}
}

// TestRbRailsFindings changes the app above on a branch so that a rename
// and removals break calls only the Rails conventions show.
func TestRbRailsFindings(t *testing.T) {
	dir := rbRailsApp(t)
	run(t, dir, "checkout", "-q", "-b", "feat")
	// A method renamed while a view still calls it through two receivers.
	write(t, dir, "app/models/event.rb", `class Event < ApplicationRecord
  belongs_to :venue
  has_many :tickets
  has_many :buyers, through: :tickets, source: :owner
  scope :upcoming, -> { where(upcoming: true) }
  delegate :label, to: :venue, prefix: true

  def title_line
    venue.label.upcase
  end

  def top_buyer
    buyers.first.greet
  end
end
`)
	// A partial removed while still rendered; a policy method removed while
	// still authorized.
	run(t, dir, "rm", "-q", "app/views/shared/_card.html.erb")
	write(t, dir, "app/policies/ticket_policy.rb", `class TicketPolicy < ApplicationPolicy
  def edit?
    true
  end

  class Scope < ApplicationPolicy::Scope
    def resolve
      :mine
    end
  end
end
`)
	// A new job and mailer chain.
	write(t, dir, "app/models/venue.rb", `class Venue < ApplicationRecord
  has_many :events

  def label
    name
  end

  def announce
    TicketJob.set(wait: 1).perform_later(id)
    TicketMailer.receipt(id).deliver_now
  end
end
`)
	cg := callGraphOf(t, dir, gitfiles.ModeBranch)
	if len(cg.Errors) > 0 || len(cg.Unparsed) > 0 {
		t.Fatalf("errors %v, unparsed %v", cg.Errors, cg.Unparsed)
	}
	sites := map[string]string{} // removed-called function → its sites' files
	for _, f := range cg.Findings {
		if f.Kind == FindingRemovedCalled {
			var ps []string
			for _, s := range f.Sites {
				ps = append(ps, s.Path)
			}
			sites[f.Func] = strings.Join(sortedUnique(ps), " ")
		}
	}
	for id, w := range map[string]string{
		"Event#headline":                       "app/controllers/tickets_controller.rb app/views/tickets/show.html.erb",
		"view:app/views/shared/_card.html.erb": "app/views/tickets/show.html.erb",
		"TicketPolicy#update?":                 "app/controllers/tickets_controller.rb",
	} {
		if sites[id] != w {
			t.Errorf("removed-called %s at %q, want %q (findings %v)", id, sites[id], w, findingKeys(cg))
		}
	}
	calls := callKeys(cg)
	for _, w := range []string{
		"+Venue#announce>TicketJob#perform@approx",
		"+Venue#announce>TicketMailer#receipt@approx",
	} {
		if !contains(calls, w) {
			t.Errorf("missing call %s in %v", w, calls)
		}
	}
	if st := statuses(cg); st["Event#title_line"] != FuncAdded || st["Event#headline"] != FuncRemoved {
		t.Errorf("statuses %v", st)
	}
}

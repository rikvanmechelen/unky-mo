package review

import (
	"reflect"
	"strings"
	"testing"

	"github.com/rvanmech/unky-mo/internal/gitfiles"
)

// rbDefsOf lists a scan's definitions as "owner.name: calls [line-end]".
func rbDefsOf(f hFile) map[string]string {
	got := map[string]string{}
	for _, d := range f.Defs {
		var calls []string
		for _, c := range d.Calls {
			k := c.Name
			if c.Recv != "" {
				k = c.Recv + "." + k
			}
			if c.Ref {
				k += "@ref"
			}
			calls = append(calls, k)
		}
		got[qualify(d.Owner, d.Name)] = strings.Join(calls, " ") + rangeOf(d)
	}
	return got
}

func TestRbScanDefs(t *testing.T) {
	src := `module Admin
  class Report < Base
    include Trackable, Exportable
    before_action :load, only: :show, if: :ready?
    validates :title, presence: true, unless: :draft?
    scope :recent, -> { where(fresh) }

    # def commented; end
    def self.build(a, b = 1)
      new(a).run if a
    end

    class << self
      def cached
        build(1)
      end
    end

    def run
      return unless ready?
      if ready?
        helper(1)
      end
      items.each do |item|
        item.process
      end
      x = compute
      x.go
      r = Report.new
      r.deliver
      text = "def fake; end if x"
      sql = <<~SQL
        def nope
        end
      SQL
      re = /end do/
      super
    end

    def label = Report.build(1)

    def ready? = true
  end
end

class Admin::Thing
  def call; perform; end
end
`
	f := (&rbCalls{}).scanFile("app/models/admin/report.rb", src)
	if !f.OK {
		t.Fatal("not OK")
	}
	want := map[string]string{
		"Admin::Report":             "self.new.load@ref self.new.ready?@ref self.new.draft?@ref [2-43]",
		"Admin::Report.self.recent": "where fresh [6-6]",
		"Admin::Report.self.build":  "new self.new.run [9-11]",
		"Admin::Report.self.cached": "build [14-16]",
		"Admin::Report.run":         "ready? ready? helper items self.items.each self.items.[].process compute self.compute.go Report.new Report.new.deliver super.run [19-38]",
		"Admin::Report.label":       "Report.build [40-40]",
		"Admin::Report.ready?":      " [42-42]",
		"Admin::Thing.call":         "perform [47-47]",
	}
	if got := rbDefsOf(f); !reflect.DeepEqual(got, want) {
		t.Errorf("defs:\n%v\nwant\n%v", got, want)
	}
	var classes []string
	for _, c := range f.Classes {
		classes = append(classes, c.Name+"<"+strings.Join(c.Bases, ","))
	}
	wantClasses := []string{
		"Admin<", "Admin.self<",
		"Admin::Report<i:Admin::Report Trackable,i:Admin::Report Exportable,i:Admin Base",
		"Admin::Report.self<s:Admin::Report Trackable,s:Admin::Report Exportable,s:Admin Base",
		"Admin::Thing<", "Admin::Thing.self<",
	}
	if !reflect.DeepEqual(classes, wantClasses) {
		t.Errorf("classes %v\nwant %v", classes, wantClasses)
	}
	for _, d := range f.Defs {
		if (d.Name == "build" || d.Name == "cached" || d.Name == "recent") != d.Static {
			t.Errorf("%s: static %v", d.Name, d.Static)
		}
	}
}

func TestRbScanEdges(t *testing.T) {
	l := &rbCalls{}
	// Modifiers, while … do, x = if, one-liners and multi-line arguments
	// all balance.
	ok := `class A
  def a(x,
        y)
    return 1 if x
    y = 2 unless x
    while x do
      x -= 1
    end
    z = if y
          1
        else
          2
        end
    w = case z
    when 1 then :a
    end
    foo(z,
        w) do |q|
      q
    end
    [1].map { |v| v if v }
    h = { if: 1, end: 2, class: 3 }
    h[:if]
    self.class.name
    begin; x; end until x
  end
  def b; end
end
`
	f := l.scanFile("a.rb", ok)
	if !f.OK {
		t.Fatal("balanced file not OK")
	}
	if got := rbDefsOf(f); got["A.a"] != "foo ?.map ?.name [2-26]" || !strings.HasSuffix(got["A.b"], "[27-27]") {
		t.Errorf("defs %v", got)
	}
	// An end too few, an end too many, and an end that the indentation
	// says belongs elsewhere.
	for _, bad := range []string{
		"class A\n  def a\n    if x\n      y\n  end\nend\n",
		"class A\n  def a\n  end\n  end\nend\n",
		"class A\n  def a\n    foo do\n  end\n    end\nend\n",
	} {
		if l.scanFile("a.rb", bad).OK {
			t.Errorf("unbalanced file OK:\n%s", bad)
		}
	}
	// Valid files that once read as unbalanced.
	for name, c := range map[string]struct{ src, want string }{
		// A line starting with a string: its end lines up with the key,
		// not with the blanked code after it.
		"string key": {"class A\n  M = {\n    \"k\" => lambda do |r|\n      next [] if r.nil?\n      r\n    end,\n  }.freeze\n\n  def a = { k:, v: }\nend\n", "A.a [9-9]"},
		// A modifier after a string: x += "s" if y.
		"modifier after a string": {"class A\n  def a(y)\n    x = \"a\"\n    x += \", b\" if y\n    x += \")\" unless y\n    y.any? ? x : \"none\"\n  end\nend\n", "A.a [2-7]"},
		// A quote inside a regexp: /it's/ isn't a string.
		"regexp with a quote": {"class A\n  def a\n    match(text: /it's here\\?/)\n    b\n  end\n\n  def b; end\nend\n", "A.b [7-7]"},
	} {
		f := l.scanFile("a.rb", c.src)
		if !f.OK {
			t.Errorf("%s: not OK", name)
			continue
		}
		found := false
		for k, v := range rbDefsOf(f) {
			if strings.HasSuffix(c.want, v[strings.LastIndex(v, " ["):]) && strings.HasPrefix(c.want, k+" ") {
				found = true
			}
		}
		if !found {
			t.Errorf("%s: defs %v, want %s", name, rbDefsOf(f), c.want)
		}
	}
	// Strings, comments, heredocs and =begin blocks hold no code.
	quiet := "x = \"#{fake(1)}\" # nope(2)\ny = <<~EOS\n  hidden(3)\nEOS\n=begin\nburied(4)\n=end\n"
	for _, d := range l.scanFile("a.rb", quiet).Defs {
		if len(d.Calls) > 0 {
			t.Errorf("calls from strings or comments: %+v", d.Calls)
		}
	}
	// A view is one definition; block parameters are locals.
	v := l.scanFile("app/views/a/show.html.erb", "<h1><%= title(@a) %></h1>\n<%# hidden(1) %>\n<% @a.each do |x| %><%= x.name %><%= x %><% end %>\n")
	if got := rbDefsOf(v); !reflect.DeepEqual(got, map[string]string{"<view>": "title @a.each @a.[].name [1-3]"}) {
		t.Errorf("view %v", got)
	}
}

// rbCallRepo: a Rails app with a concern, callbacks, a helper and a view,
// a class hierarchy and a spec, changed on a branch.
func rbCallRepo(t *testing.T) string {
	t.Helper()
	dir := gitRepo(t)
	write(t, dir, "Gemfile", "source 'https://rubygems.org'\ngem 'rails'\n")
	write(t, dir, "config/application.rb", "module App\n  class Application < Rails::Application\n  end\nend\n")
	write(t, dir, "app/models/application_record.rb", "class ApplicationRecord < ActiveRecord::Base\n  self.abstract_class = true\nend\n")
	write(t, dir, "app/models/concerns/trackable.rb", `module Trackable
  extend ActiveSupport::Concern

  def track(event)
    event
  end
end
`)
	write(t, dir, "app/models/user.rb", `class User < ApplicationRecord
  include Trackable
  before_save :normalize

  def initialize(name)
    super()
    @name = name
  end

  def full_name
    @name
  end

  def legacy_name
    full_name
  end

  private

  def normalize
    @name
  end
end
`)
	write(t, dir, "app/models/report.rb", `class Report
  def self.generate(from)
    new(from).build
  end

  def initialize(from)
    @from = from
  end

  def build
    @from
  end
end
`)
	write(t, dir, "app/services/notifier.rb", "class Notifier\n  def call\n    :sent\n  end\nend\n")
	write(t, dir, "app/services/exporter.rb", "class Exporter\n  def call\n    :done\n  end\nend\n")
	write(t, dir, "app/controllers/application_controller.rb", `class ApplicationController < ActionController::Base
  helper_method :current_user

  def current_user
    @current_user
  end
end
`)
	write(t, dir, "app/controllers/users_controller.rb", `class UsersController < ApplicationController
  before_action :load_user, only: :show

  def show
    @user.legacy_name
    Report.generate(1)
  end

  private

  def load_user
    @user = User.new("x")
  end
end
`)
	write(t, dir, "app/helpers/users_helper.rb", "module UsersHelper\n  def title_for(user)\n    user.full_name\n  end\nend\n")
	write(t, dir, "app/views/users/show.html.erb", "<p><%= current_user %></p>\n")
	write(t, dir, "spec/models/report_spec.rb", "require \"rails_helper\"\n\nRSpec.describe Report do\n  it \"generates\" do\n    Report.generate(1)\n  end\nend\n")
	run(t, dir, "add", ".")
	run(t, dir, "commit", "-q", "-m", "init")
	run(t, dir, "checkout", "-q", "-b", "feat")
	write(t, dir, "app/models/user.rb", `class User < ApplicationRecord
  include Trackable
  before_save :normalize
  after_initialize :prepare, if: :new_record?

  def initialize(name)
    super()
    @name = name
  end

  def full_name
    track(:full_name)
    @name
  end

  private

  def normalize
    @name
  end

  def prepare
    normalize
  end
end
`)
	write(t, dir, "app/models/admin_user.rb", `class AdminUser < User
  def full_name
    super
  end

  def self.build(name)
    AdminUser.new(name)
  end
end
`)
	write(t, dir, "app/models/report.rb", `class Report
  def self.generate(from, to = nil)
    new(from).build
  end

  def initialize(from)
    @from = from
  end

  def build
    @from
  end

  def deliver(job)
    Notifier.new.call
    job.call
  end
end
`)
	write(t, dir, "app/views/users/show.html.erb", "<h1><%= title_for(@user) %></h1>\n<p><%= current_user %></p>\n")
	return dir
}

func TestRbCalls(t *testing.T) {
	cg := callGraphOf(t, rbCallRepo(t), gitfiles.ModeBranch)
	if len(cg.Errors) > 0 || len(cg.Unparsed) > 0 {
		t.Fatalf("errors %v, unparsed %v", cg.Errors, cg.Unparsed)
	}
	want := map[string]string{
		"User":                               FuncChanged, // a new callback
		"User#full_name":                     FuncChanged,
		"User#legacy_name":                   FuncRemoved,
		"User#prepare":                       FuncAdded,
		"AdminUser#full_name":                FuncAdded,
		"AdminUser.build":                    FuncAdded,
		"Report.generate":                    FuncSignature,
		"Report#deliver":                     FuncAdded,
		"view:app/views/users/show.html.erb": FuncChanged,
	}
	if got := statuses(cg); !reflect.DeepEqual(got, want) {
		t.Errorf("statuses = %v\nwant %v", got, want)
	}
	calls := callKeys(cg)
	for _, w := range []string{
		"+User>User#prepare@ref",                                                       // a callback
		"User>User#normalize@ref",                                                      // an unchanged callback of a changed class
		"+User#full_name>Trackable#track@approx",                                       // through the concern
		"+User#prepare>User#normalize@approx",                                          // a bare call without parens
		"+AdminUser#full_name>User#full_name@approx",                                   // super
		"+AdminUser.build>User#initialize@approx",                                      // Const.new, initialize inherited
		"+Report#deliver>Notifier#call@approx",                                         // Const.new.m
		"Report.generate>Report#initialize@approx",                                     // new(…) in a class method
		"Report.generate>Report#build@approx",                                          // new(…).m
		"UsersController#show>Report.generate@approx",                                  // a caller in an unchanged file
		`test:spec/models/report_spec.rb#it "generates">Report.generate@approx`,        // a spec example
		"+view:app/views/users/show.html.erb>UsersHelper#title_for@approx",             // a helper
		"view:app/views/users/show.html.erb>ApplicationController#current_user@approx", // a helper_method
	} {
		if !contains(calls, w) {
			t.Errorf("missing call %s", w)
		}
	}
	for _, c := range calls {
		if strings.Contains(c, "Exporter#call") {
			t.Errorf("a call on an unknown receiver was guessed: %s", c)
		}
	}
	findings := findingKeys(cg)
	for _, w := range []string{
		"removed-called:User#legacy_name app/controllers/users_controller.rb",
		"signature-callers:Report.generate app/controllers/users_controller.rb spec/models/report_spec.rb",
		"untested:Report#deliver",
	} {
		if !contains(findings, w) {
			t.Errorf("missing finding %s in %v", w, findings)
		}
	}
	if contains(findings, "untested:Report.generate") {
		t.Error("Report.generate is reached by the spec")
	}
	for _, f := range cg.Funcs {
		switch f.ID {
		case "Report#deliver":
			if f.Unresolved != 1 || f.Name != "Report#deliver" {
				t.Errorf("deliver = %+v, want job.call unresolved", f)
			}
		case "view:app/views/users/show.html.erb":
			if f.Name != "users/show.html.erb" {
				t.Errorf("view name %q", f.Name)
			}
		case `test:spec/models/report_spec.rb#it "generates"`:
			if !f.Test {
				t.Errorf("spec not a test: %+v", f)
			}
		}
	}
	if len(cg.Languages) != 1 || cg.Languages[0].Name != "ruby" || cg.Languages[0].Exact {
		t.Errorf("languages %+v", cg.Languages)
	}
}

package review

import (
	"reflect"
	"testing"

	"github.com/rvanmech/unky-mo/internal/gitfiles"
)

func TestRbTestsFor(t *testing.T) {
	l := &rbCalls{}
	for p, want := range map[string][]string{
		"app/models/ticket.rb": {"test/models/ticket_test.rb", "spec/models/ticket_spec.rb"},
		"app/controllers/admin/tickets_controller.rb": {
			"test/controllers/admin/tickets_controller_test.rb", "spec/controllers/admin/tickets_controller_spec.rb",
			"test/integration/admin/tickets_test.rb", "spec/requests/admin/tickets_spec.rb",
			"test/system/admin/tickets_test.rb", "spec/system/admin/tickets_spec.rb",
		},
		"app/models/concerns/trackable.rb": {"test/models/concerns/trackable_test.rb", "spec/models/concerns/trackable_spec.rb", "test/models/trackable_test.rb"},
		"lib/pricing/rule.rb":              {"test/lib/pricing/rule_test.rb", "spec/lib/pricing/rule_spec.rb"},
		"app/views/tickets/show.html.erb":  nil,
		"test/models/ticket_test.rb":       nil,
		"config/routes.rb":                 nil,
	} {
		if got := l.testsFor(p); !reflect.DeepEqual(got, want) {
			t.Errorf("testsFor(%q) = %v, want %v", p, got, want)
		}
	}
}

// A changed file whose conventional test exists and didn't change gets a
// hint; changing the test with it doesn't.
func TestCallDeltaTestNotUpdated(t *testing.T) {
	after := func() *callSet {
		s := set(tf("Ticket#price", "app/models/ticket.rb", 3, "b2", "s"))
		s.testFiles["app/models/ticket.rb"] = []string{"test/models/ticket_test.rb"}
		return s
	}
	before := set(tf("Ticket#price", "app/models/ticket.rb", 3, "b1", "s"))
	cg := delta(before, after(), []string{"app/models/ticket.rb"}, []string{"app/models/ticket.rb"}, nil)
	if got := findingKeys(cg); !contains(got, "test-not-updated:Ticket#price test/models/ticket_test.rb") {
		t.Errorf("findings = %v", got)
	}
	cg = delta(before, after(), []string{"app/models/ticket.rb", "test/models/ticket_test.rb"},
		[]string{"app/models/ticket.rb", "test/models/ticket_test.rb"}, nil)
	for _, k := range findingKeys(cg) {
		if k == "test-not-updated:Ticket#price test/models/ticket_test.rb" {
			t.Errorf("hint despite the test changing: %v", findingKeys(cg))
		}
	}
}

// A Rails test that calls a model method by name covers it, even though the
// graph can't follow the call (a fixture-loaded record's type is unknown).
func TestRbConventionalCoverage(t *testing.T) {
	dir := railsRepo(t)
	write(t, dir, "app/models/ticket.rb", "class Ticket < ApplicationRecord\n  def price\n    1\n  end\n\n  def tax\n    0\n  end\nend\n")
	write(t, dir, "test/models/ticket_test.rb", "require \"test_helper\"\n\nclass TicketTest < ActiveSupport::TestCase\n  test \"price\" do\n    assert_equal 1, tickets(:one).price\n  end\nend\n")
	run(t, dir, "add", ".")
	run(t, dir, "commit", "-q", "-m", "tickets")
	write(t, dir, "app/models/ticket.rb", "class Ticket < ApplicationRecord\n  def price\n    2\n  end\n\n  def tax\n    1\n  end\nend\n")
	cg := callGraphOf(t, dir, gitfiles.ModeHead)
	got := findingKeys(cg)
	if contains(got, "untested:Ticket#price") {
		t.Errorf("price is called by its test file: %v", got)
	}
	if !contains(got, "untested:Ticket#tax") {
		t.Errorf("tax isn't named by any test: %v", got)
	}
	if !contains(got, "test-not-updated:Ticket#price test/models/ticket_test.rb") {
		t.Errorf("missing test-not-updated: %v", got)
	}
}

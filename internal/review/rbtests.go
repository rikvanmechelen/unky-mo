package review

import (
	"path"
	"strings"
)

// testsFor lists the test files Rails conventions pair with a source file,
// for Minitest (test/…_test.rb) and RSpec (spec/…_spec.rb):
//
//	app/models/ticket.rb                → test/models/ticket_test.rb, spec/models/ticket_spec.rb
//	app/controllers/tickets_controller.rb → also test/integration/tickets_test.rb, spec/requests/tickets_spec.rb
//	lib/pricing/rule.rb                 → test/lib/pricing/rule_test.rb, spec/lib/pricing/rule_spec.rb
//
// Views and other non-Ruby files have none.
func (l *rbCalls) testsFor(p string) []string {
	if !strings.HasSuffix(p, ".rb") || isTest(p) {
		return nil
	}
	var rel string
	switch {
	case strings.HasPrefix(p, "app/"):
		rel = strings.TrimPrefix(p, "app/")
	case strings.HasPrefix(p, "lib/"):
		rel = p
	default:
		return nil
	}
	rel = strings.TrimSuffix(rel, ".rb")
	out := []string{"test/" + rel + "_test.rb", "spec/" + rel + "_spec.rb"}
	if c, ok := strings.CutPrefix(rel, "controllers/"); ok && strings.HasSuffix(c, "_controller") {
		// Request-level tests are named after the resource, without
		// "_controller".
		res := strings.TrimSuffix(c, "_controller")
		out = append(out, "test/integration/"+res+"_test.rb", "spec/requests/"+res+"_spec.rb",
			"test/system/"+res+"_test.rb", "spec/system/"+res+"_spec.rb")
	}
	if dir, base := path.Split(rel); strings.HasPrefix(dir, "models/concerns/") || strings.HasPrefix(dir, "controllers/concerns/") {
		// Concerns are often tested through a dummy class in test/models.
		out = append(out, "test/"+strings.TrimSuffix(dir, "concerns/")+base+"_test.rb")
	}
	return out
}

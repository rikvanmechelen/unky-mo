package review

import (
	"os/exec"
	"reflect"
	"strings"
	"testing"
)

func TestSwiftScan(t *testing.T) {
	src := "import SwiftUI\n@testable import MoMA\n// HiddenType in a comment\nstruct TicketView: View {\n  let s = \"Ghost \\(name.count) here\"\n  let raw = #\"Phantom \"quoted\" \"#\n  let ml = \"\"\"\n  Specter\n  \"\"\"\n  var body: some View { Colors.primary }\n}\n"
	got := swiftRefs(src)
	if len(got.Imports) != 1 || got.Imports[0].to != "SwiftUI" {
		t.Errorf("imports %+v", got.Imports)
	}
	var types []string
	for _, r := range got.Types {
		types = append(types, r.to)
	}
	if strings.Contains(strings.Join(types, ","), "Ghost") || strings.Contains(strings.Join(types, ","), "Phantom") ||
		strings.Contains(strings.Join(types, ","), "Specter") || strings.Contains(strings.Join(types, ","), "HiddenType") {
		t.Errorf("names from comments/strings: %v", types)
	}
	if !strings.Contains(strings.Join(types, ","), "Colors") {
		t.Errorf("types %v", types)
	}
	if d := swiftDecls("public final class A {}\nstruct B { enum C {} }\nextension D {}\nprotocol E {}\n"); !reflect.DeepEqual(d, []string{"A", "B", "C", "E"}) {
		t.Errorf("decls %v", d)
	}
}

// swiftRepo is a single-target app with feature layers, a design system and
// a local Swift package; branch "feat" adds layer-breaking references and
// iOS surface changes.
func swiftRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	dir := t.TempDir()
	run(t, dir, "init", "-q", "-b", "main")
	run(t, dir, "config", "user.email", "test@example.com")
	run(t, dir, "config", "user.name", "Test")
	run(t, dir, "config", "commit.gpgsign", "false")
	write(t, dir, "MoMA.xcodeproj/project.pbxproj", "// placeholder\nIPHONEOS_DEPLOYMENT_TARGET = 17.0;\n")
	write(t, dir, "project.yml", "name: MoMA\npackages:\n  Kingfisher:\n    url: https://github.com/onevcat/Kingfisher\n    exactVersion: \"8.1.0\"\n")
	write(t, dir, "App/App.entitlements", "<dict>\n<key>aps-environment</key>\n</dict>\n")
	write(t, dir, "Features/Tickets/Domain/Ticket.swift", "struct Ticket {\n  let id: String\n}\n")
	write(t, dir, "Features/Tickets/Data/TicketAPI.swift", "struct TicketAPI {\n  func load() -> [Ticket] { [] }\n}\n")
	write(t, dir, "Features/Tickets/Presentation/TicketView.swift", "import SwiftUI\nstruct TicketView: View {\n  var body: some View { Text(\"x\") }\n}\n")
	write(t, dir, "DesignSystem/Tokens/Colors.swift", "enum Colors {\n  static let primary = 1\n}\n")
	write(t, dir, "Core/Networking/HTTP.swift", "struct HTTPClient {}\n")
	write(t, dir, "Packages/Analytics/Package.swift", "// swift-tools-version:5.9\nlet package = Package(\n  name: \"Analytics\",\n  targets: [\n    .target(name: \"Analytics\"),\n    .testTarget(name: \"AnalyticsTests\", dependencies: [\"Analytics\"]),\n  ]\n)\n")
	write(t, dir, "Packages/Analytics/Sources/Analytics/Tracker.swift", "public struct Tracker {}\n")
	write(t, dir, "Tests/TicketTests.swift", "@testable import MoMA\nlet v = TicketView()\n")
	run(t, dir, "add", ".")
	run(t, dir, "commit", "-q", "-m", "init")

	run(t, dir, "checkout", "-q", "-b", "feat")
	write(t, dir, "Features/Tickets/Domain/Ticket.swift", "struct Ticket {\n  let id: String\n  var preview: TicketView { TicketView() }\n}\n")
	write(t, dir, "Features/Tickets/Presentation/TicketView.swift", "import SwiftUI\nimport Analytics\n// HTTPClient isn't used: comment\nstruct TicketView: View {\n  var body: some View { Text(\"HTTPClient\").foregroundColor(Colors.primary) }\n}\n")
	write(t, dir, "DesignSystem/Tokens/Colors.swift", "enum Colors {\n  static let primary = 1\n  static func sample() -> Any { TicketView() }\n}\n")
	write(t, dir, "project.yml", "name: MoMA\npackages:\n  Kingfisher:\n    url: https://github.com/onevcat/Kingfisher\n    exactVersion: \"8.11.0\"\ntargets:\n  MoMA:\n    info:\n      properties:\n        NSCameraUsageDescription: Scan tickets\n")
	write(t, dir, "App/App.entitlements", "<dict>\n<key>aps-environment</key>\n<key>com.apple.developer.healthkit</key>\n</dict>\n")
	write(t, dir, "MoMA.xcodeproj/project.pbxproj", "// placeholder\nIPHONEOS_DEPLOYMENT_TARGET = 18.0;\n")
	return dir
}

func TestSwiftArchitecture(t *testing.T) {
	a := analyze(t, swiftRepo(t))
	want := []string{
		"+DesignSystem/Tokens>Features/Tickets/Presentation",
		"+Features/Tickets/Domain>Features/Tickets/Presentation",
		"+Features/Tickets/Presentation>DesignSystem/Tokens",
		"+Features/Tickets/Presentation>Packages/Analytics/Sources/Analytics",
	}
	if got := edgeKeys(a.Edges); !reflect.DeepEqual(got, want) {
		t.Errorf("edges %v\nwant  %v", got, want)
	}
	viol := map[string]string{}
	for _, e := range a.Edges {
		if e.Lang != "swift" || !e.Approx {
			t.Errorf("edge %+v", e)
		}
		if e.Violation != "" {
			viol[e.From] = e.Violation
		}
	}
	if viol["Features/Tickets/Domain"] != "ios: models and domain must not import Features/Tickets/Presentation" ||
		viol["DesignSystem/Tokens"] != "ios: design system must not import Features/Tickets/Presentation" || a.Violations != 2 {
		t.Errorf("violations %v (%d)", viol, a.Violations)
	}
}

func TestIOSSurface(t *testing.T) {
	s := analyze(t, swiftRepo(t)).Surface
	if got := changeKeys(s.Permissions); !reflect.DeepEqual(got, []string{"+NSCameraUsageDescription", "+com.apple.developer.healthkit"}) {
		t.Errorf("permissions %v", got)
	}
	if got := changeKeys(s.Deps); !reflect.DeepEqual(got, []string{"~IPHONEOS_DEPLOYMENT_TARGET", "~Kingfisher"}) {
		t.Errorf("deps %+v", s.Deps)
	}
}

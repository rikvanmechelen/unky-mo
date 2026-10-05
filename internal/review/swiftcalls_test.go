package review

import (
	"reflect"
	"strings"
	"testing"

	"github.com/rvanmech/unky-mo/internal/gitfiles"
)

// swiftDefsOf scans src and lists each definition's calls and range, by
// owner-qualified name.
func swiftDefsOf(t *testing.T, src string) (hFile, map[string]string) {
	t.Helper()
	f := (&swiftCalls{}).scanFile("x.swift", src)
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
	return f, got
}

func TestSwiftScanDefs(t *testing.T) {
	src := `import Foundation
@testable import Store

/* a /* nested */ comment: fake() */
protocol Loading: AnyObject {
    func load() -> [Item]
    var count: Int { get }
}

final class Shop<T>: Base, Loading where T: Hashable {
    let store = Store()
    init?(name: String) {
        super.init(name: name)
    }

    static func make<U: Codable>(_ u: U) -> Shop {
        Shop(name: "x")!
    }

    func load() -> [Item] {
        let s = "fake() \(helper()) in a string"
        // nope() in a comment
        return self.items.map(transform).filter { $0.ok }
    }

    struct Inner {
        func deep() { outer() }
    }

    subscript(i: Int) -> Item { item(at: i) }

    deinit { cleanup() }
}

extension Shop: Hashable {
    var title: String {
        format(name)
    }

    class func reset() {
        let raw = #"raw "fake()" \#(x())"#
        let multi = """
            fake()
            \(nope())
            """
        DispatchQueue.main.async {
            Shop.make(1)
        }
        perform(#selector(Shop.load))
        let v: Shop = .init(x: 1)
    }
}

var body: some View {
    VStack {
        header
    }
}

func operate() {
    if ready {
        go()
    }
    for item in items {
        use(item)
    }
}

main()
`
	f, got := swiftDefsOf(t, src)
	if !f.OK {
		t.Fatal("not OK")
	}
	want := map[string]string{
		"Loading":         " [5-8]",
		"Loading.load":    " [6-6]",
		"Loading.count":   " [7-7]",
		"Shop":            "Store [10-33]",
		"Shop.init":       "super.init [12-14]",
		"Shop.make":       "Shop [16-18]",
		"Shop.load":       "self.items.map ?.filter self.items@ref transform@ref [20-24]",
		"Shop.Inner":      " [26-28]",
		"Shop.Inner.deep": "outer [27-27]",
		"Shop.subscript":  "item [30-30]",
		"Shop.deinit":     "cleanup [32-32]",
		"Shop.title":      "format name@ref [36-38]",
		"Shop.reset":      "DispatchQueue.main.async Shop.make perform ..init Shop.load@ref [40-51]",
		"body":            "VStack header@ref [54-58]",
		"operate":         "go use ready@ref items@ref [60-67]",
		"<top>":           "main [69-69]",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("defs:\n%v\nwant\n%v", got, want)
	}
	for _, d := range f.Defs {
		switch qualify(d.Owner, d.Name) {
		case "Shop.make", "Shop.reset":
			if !d.Static {
				t.Errorf("%s not static", d.Name)
			}
		case "Shop", "Loading":
			if !d.IsClass {
				t.Errorf("%s not a type", d.Name)
			}
		case "Shop.title", "body":
			if d.Sig != "" {
				t.Errorf("property %s has a signature", d.Name)
			}
		}
		if d.Class == "" && d.Owner != "" {
			t.Errorf("%s.%s has no class", d.Owner, d.Name)
		}
	}
	bases := map[string][]string{}
	for _, c := range f.Classes {
		bases[c.Name] = c.Bases
	}
	if want := map[string][]string{"Shop": {"Base", "Loading", "Hashable"}}; !reflect.DeepEqual(bases, want) {
		t.Errorf("bases = %v, want %v", bases, want)
	}
	var imps []string
	for _, i := range f.Imports {
		imps = append(imps, i.Local)
	}
	if want := []string{"Foundation", "Store"}; !reflect.DeepEqual(imps, want) {
		t.Errorf("imports = %v, want %v", imps, want)
	}
}

// A comment or a reformat isn't a change; a string is.
func TestSwiftScanBodies(t *testing.T) {
	body := func(src string) string {
		f := (&swiftCalls{}).scanFile("x.swift", src)
		for _, d := range f.Defs {
			if d.Name == "f" {
				return d.Body
			}
		}
		t.Fatalf("no f in %q", src)
		return ""
	}
	base := body("struct A {\n  func f() {\n    g(\"a\")\n  }\n  func h() {}\n}\n")
	if b := body("struct A {\n  func f() {\n    // note\n    g(\"a\")   /* x */\n  }\n  func h() { x() }\n}\n"); b != base {
		t.Error("a comment or a sibling changed the body hash")
	}
	if b := body("struct A {\n  func f() {\n    g(\"b\")\n  }\n}\n"); b == base {
		t.Error("a changed string kept the body hash")
	}
	// A type's own hash ignores its methods' bodies.
	typ := func(src string) string { return (&swiftCalls{}).scanFile("x.swift", src).Defs[0].Body }
	if typ("struct A {\n  func f() { a() }\n}\n") != typ("struct A {\n  func f() { b() }\n}\n") {
		t.Error("a method's body changed its type's hash")
	}
}

func TestSwiftScanUnbalanced(t *testing.T) {
	if f := (&swiftCalls{}).scanFile("x.swift", "struct A {\n  func f() {\n"); f.OK {
		t.Error("unclosed braces scanned as OK")
	}
	if f := (&swiftCalls{}).scanFile("x.swift", "let s = \"}\"\nstruct A { func f() { \"{\" } }\n"); !f.OK {
		t.Error("braces in strings counted")
	}
}

// swiftCallRepo: an app module (Features/…, named after the .xcodeproj)
// with an extension in another file and a protocol, a SwiftPM package with
// a target and a test target, and app tests, changed on a branch.
func swiftCallRepo(t *testing.T) string {
	t.Helper()
	dir := gitRepo(t)
	write(t, dir, "MoMA.xcodeproj/project.pbxproj", "// placeholder\n")
	write(t, dir, "Packages/Analytics/Package.swift", "// swift-tools-version:5.9\nlet package = Package(\n  name: \"Analytics\",\n  targets: [\n    .target(name: \"Analytics\"),\n    .testTarget(name: \"AnalyticsTests\", dependencies: [\"Analytics\"]),\n  ]\n)\n")
	write(t, dir, "Packages/Analytics/Sources/Analytics/Tracker.swift", `public class Tracker {
    public init() {}

    public func track(_ event: String) {
        send(event)
    }

    func send(_ e: String) {}
}
`)
	write(t, dir, "Packages/Analytics/Tests/AnalyticsTests/TrackerTests.swift", `import XCTest
@testable import Analytics

final class TrackerTests: XCTestCase {
    func testTrack() {
        Tracker().track("x")
    }
}
`)
	write(t, dir, "Features/Tickets/Domain/Model.swift", `class Model {
    func save() -> Bool {
        validate()
    }

    func validate() -> Bool { true }
}

struct Ticket {
    let id: String
}

protocol Loading {
    func load() -> [Ticket]
}
`)
	write(t, dir, "Features/Tickets/Domain/TicketStore.swift", `final class TicketStore: Model {
    func load() -> [Ticket] {
        []
    }

    func legacy() {}
}

extension TicketStore: Loading {}
`)
	write(t, dir, "Features/Tickets/Domain/TicketStore+Format.swift", `extension TicketStore {
    func title(for t: Ticket) -> String {
        format(t.id)
    }
}
`)
	write(t, dir, "Features/Tickets/Data/Format.swift", `func format(_ s: String) -> String {
    s.uppercased() // fake() in a comment
}
`)
	write(t, dir, "Features/Tickets/Presentation/TicketView.swift", `import SwiftUI
import Analytics

struct TicketView: View {
    let store = TicketStore()

    var body: some View {
        Text(store.title(for: Ticket(id: "fake()")))
    }

    func refresh() {
        store.legacy()
    }
}
`)
	write(t, dir, "Tests/TicketTests.swift", `import XCTest
@testable import MoMA

final class TicketTests: XCTestCase {
    func testLoad() {
        let s: Loading = TicketStore()
        _ = s.load()
    }
}
`)
	run(t, dir, "add", ".")
	run(t, dir, "commit", "-q", "-m", "init")
	run(t, dir, "checkout", "-q", "-b", "feat")
	write(t, dir, "Features/Tickets/Domain/TicketStore.swift", `final class TicketStore: Model {
    func load() -> [Ticket] {
        _ = save()
        _ = self.title(for: Ticket(id: "1"))
        return []
    }
}

extension TicketStore: Loading {}
`)
	write(t, dir, "Features/Tickets/Data/Format.swift", `func format(_ s: String, upper: Bool) -> String {
    s.uppercased() // fake() in a comment
}
`)
	write(t, dir, "Features/Tickets/Presentation/TicketView.swift", `import SwiftUI
import Analytics

struct TicketView: View {
    let store = TicketStore()

    var header: String { "Tickets" }

    var body: some View {
        VStack {
            Text(header)
            Text(store.title(for: Ticket(id: "fake()")))
        }
    }

    func refresh() {
        Tracker().track("refresh")
        store.legacy()
    }
}
`)
	return dir
}

func TestSwiftCalls(t *testing.T) {
	cg := callGraphOf(t, swiftCallRepo(t), gitfiles.ModeBranch)
	if len(cg.Errors) > 0 || len(cg.Unparsed) > 0 {
		t.Fatalf("errors %v, unparsed %v", cg.Errors, cg.Unparsed)
	}
	want := map[string]string{
		"MoMA.TicketStore.load":   FuncChanged,
		"MoMA.TicketStore.legacy": FuncRemoved,
		"MoMA.TicketView.header":  FuncAdded,
		"MoMA.TicketView.body":    FuncChanged,
		"MoMA.TicketView.refresh": FuncChanged,
		"MoMA.format":             FuncSignature,
	}
	if got := statuses(cg); !reflect.DeepEqual(got, want) {
		t.Errorf("statuses = %v\nwant %v", got, want)
	}
	calls := callKeys(cg)
	for _, w := range []string{
		"+MoMA.TicketStore.load>MoMA.Model.save@approx",           // the superclass, bare
		"+MoMA.TicketStore.load>MoMA.TicketStore.title@approx",    // an extension in another file, through self
		"+MoMA.TicketStore.load>MoMA.Ticket@approx",               // a struct's memberwise init
		"+MoMA.TicketView.refresh>Analytics.Tracker.init@approx",  // an imported module's initializer
		"+MoMA.TicketView.refresh>Analytics.Tracker.track@approx", // the only track() in the repo
		"+MoMA.TicketView.body>MoMA.TicketView.header@approx",     // a computed property read
		"MoMA.TicketStore.title>MoMA.format@approx",               // same module, no import
		"MoMA.Loading.load>MoMA.TicketStore.load@impl",            // the protocol's implementation
		"MoMA.TicketView.body>MoMA.TicketStore.title@approx",      // context
	} {
		if !contains(calls, w) {
			t.Errorf("missing call %s in\n%v", w, calls)
		}
	}
	for _, c := range calls {
		if strings.Contains(c, "fake") {
			t.Errorf("call from a string or comment: %s", c)
		}
	}
	findings := findingKeys(cg)
	for _, w := range []string{
		"removed-called:MoMA.TicketStore.legacy Features/Tickets/Presentation/TicketView.swift",
		"signature-callers:MoMA.format Features/Tickets/Domain/TicketStore+Format.swift",
		"untested:MoMA.TicketView.refresh",
	} {
		if !contains(findings, w) {
			t.Errorf("missing finding %s in %v", w, findings)
		}
	}
	// The test reaches load through the protocol, one free impl step.
	for _, f := range findings {
		if f == "untested:MoMA.TicketStore.load" {
			t.Error("load flagged untested though a test calls it through Loading")
		}
	}
	if cg.Languages[0].Name != "swift" || cg.Languages[0].Exact {
		t.Errorf("languages = %+v", cg.Languages)
	}
}

package review

import (
	"reflect"
	"strings"
	"testing"

	"github.com/rvanmech/unky-mo/internal/gitfiles"
)

// jsCallRepo: a TypeScript project with a tsconfig paths alias, a barrel
// index.ts re-exporting a module and a default-exported class, a class
// hierarchy, two classes with a save() method, React components and a
// test, changed on a branch.
func jsCallRepo(t *testing.T) string {
	t.Helper()
	dir := gitRepo(t)
	write(t, dir, "package.json", `{"name": "shop", "private": true}`)
	write(t, dir, "tsconfig.json", `{
  // comments are allowed here
  "compilerOptions": {"baseUrl": ".", "paths": {"@lib/*": ["src/lib/*"]}},
}`)
	write(t, dir, "src/lib/format.ts", `export function fmt(n: number): string {
  return n.toFixed(2)
}

export function legacyFmt(n: number) {
  return fmt(n)
}

export function fake() {}
export function nope() {}
`)
	write(t, dir, "src/cart/index.ts", "export * from \"./total\"\nexport { default as Cart } from \"./Cart\"\n")
	write(t, dir, "src/cart/total.ts", `import { fmt as format } from "@lib/format"

export function sum(xs: number[]) {
  return xs.reduce((a, b) => a + b, 0)
}

export const total = (xs: number[]) => format(sum(xs))
`)
	write(t, dir, "src/cart/Cart.ts", `import * as fmtlib from "../lib/format"

class Base {
  save() {
    return this.validate()
  }

  validate() {
    return true
  }
}

export default class Cart extends Base {
  constructor(public items: number[]) {
    super()
  }

  print() {
    return fmtlib.legacyFmt(1)
  }
}
`)
	write(t, dir, "src/store/Draft.ts", "export class Draft {\n  save() {\n    return 1\n  }\n}\n")
	write(t, dir, "src/ui/Button.tsx", `export function Button({ onClick }) {
  return <button onClick={onClick}>ok</button>
}
`)
	write(t, dir, "src/ui/Panel.tsx", `import { Button } from "./Button"

export default function () {
  return <Button />
}
`)
	write(t, dir, "src/report.ts", "import { sum } from \"./cart\"\n\nexport function report() {\n  return sum([1])\n}\n")
	write(t, dir, "src/app.tsx", `import { total } from "./cart"

export function App() {
  return total([1])
}
`)
	write(t, dir, "src/cart/total.test.ts", `import { total } from "./total"

test("total", () => {
  expect(total([1])).toBe("1.00")
})
`)
	run(t, dir, "add", ".")
	run(t, dir, "commit", "-q", "-m", "init")
	run(t, dir, "checkout", "-q", "-b", "feat")
	// legacyFmt is gone, though Cart.print still calls it.
	write(t, dir, "src/lib/format.ts", `export function fmt(n: number): string {
  return n.toFixed(2)
}

export function fake() {}
export function nope() {}
`)
	write(t, dir, "src/cart/total.ts", `import { fmt as format } from "@lib/format"

export function sum(xs: number[], start = 0) {
  return xs.reduce((a, b) => a + b, start)
}

export function average(xs: number[]) {
  return sum(xs) / xs.length
}

export const total = (xs: number[]) => format(sum(xs, 0))
`)
	write(t, dir, "src/cart/Cart.ts", `import * as fmtlib from "../lib/format"

class Base {
  save() {
    return this.validate()
  }

  validate() {
    return true
  }
}

export default class Cart extends Base {
  constructor(public items: number[]) {
    super()
  }

  print() {
    this.save()
    fmtlib.fmt(2)
    return fmtlib.legacyFmt(1)
  }
}
`)
	write(t, dir, "src/app.tsx", `import { total, Cart } from "./cart"
import Panel from "./ui/Panel"
import { Button } from "./ui/Button"
import { fake, nope } from "@lib/format"

function handle() {
  return 1
}

export function App() {
  const c = new Cart([1])
  c.print()
  c.save()
  setTimeout(handle, 1)
  // nope()
  const s = "fake()" + `+"`${nope.name}`"+`
  if (/fake()/.test(s)) {
    return null
  }
  return (
    <Panel>
      <Button onClick={handle}>{total([1])}</Button>
    </Panel>
  )
}
`)
	return dir
}

func TestJSCalls(t *testing.T) {
	cg := callGraphOf(t, jsCallRepo(t), gitfiles.ModeBranch)
	if len(cg.Errors) > 0 || len(cg.Unparsed) > 0 {
		t.Fatalf("errors %v, unparsed %v", cg.Errors, cg.Unparsed)
	}
	want := map[string]string{
		"src/lib/format#legacyFmt": FuncRemoved,
		"src/cart/total#sum":       FuncSignature,
		"src/cart/total#average":   FuncAdded,
		"src/cart/total#total":     FuncChanged,
		"src/cart/Cart#Cart.print": FuncChanged,
		"src/app#App":              FuncChanged,
		"src/app#handle":           FuncAdded,
	}
	if got := statuses(cg); !reflect.DeepEqual(got, want) {
		t.Errorf("statuses = %v\nwant %v", got, want)
	}
	calls := callKeys(cg)
	for _, w := range []string{
		"+src/cart/Cart#Cart.print>src/cart/Cart#Base.save@approx", // this, up the extends chain
		"+src/cart/Cart#Cart.print>src/lib/format#fmt@approx",      // a namespace import
		"+src/cart/total#average>src/cart/total#sum@approx",        // the same file
		"src/cart/total#total>src/lib/format#fmt@approx",           // an aliased import through a tsconfig path
		"+src/app#App>src/cart/Cart#Cart.constructor@approx",       // new, through index.ts's default re-export
		"+src/app#App>src/cart/Cart#Cart.print@approx",             // the only print() in the repo
		"+src/app#App>src/ui/Panel#default@approx",                 // an anonymous default export, as JSX
		"+src/app#App>src/ui/Button#Button@approx",                 // a JSX component
		"+src/app#App>src/app#handle@ref",                          // passed as a value, twice
		"src/app#App>src/cart/total#total@approx",                  // through index.ts's export *
		"src/report#report>src/cart/total#sum@approx",              // a caller of the changed signature
		"src/cart/total.test#<module>>src/cart/total#total@approx",
	} {
		if !contains(calls, w) {
			t.Errorf("missing call %s", w)
		}
	}
	for _, c := range calls {
		if strings.Contains(c, "#fake") || strings.Contains(c, "#nope") {
			t.Errorf("call from a comment, string, template or regex: %s", c)
		}
		if strings.HasPrefix(c, "+src/app#App>") && strings.Contains(c, ".save") {
			t.Errorf("c.save() resolved though two classes define save: %s", c)
		}
	}
	findings := findingKeys(cg)
	for _, w := range []string{
		"removed-called:src/lib/format#legacyFmt src/cart/Cart.ts", // fmtlib.legacyFmt()
		"signature-callers:src/cart/total#sum src/report.ts",
		"untested:src/cart/total#average",
	} {
		if !contains(findings, w) {
			t.Errorf("missing finding %s in %v", w, findings)
		}
	}
	if contains(findings, "untested:src/cart/total#total") {
		t.Error("total is called by a test")
	}
	if cg.Languages[0].Name != "node" || cg.Languages[0].Exact {
		t.Errorf("languages = %+v", cg.Languages)
	}
	for _, f := range cg.Funcs {
		if f.ID == "src/cart/Cart#Cart.print" && (f.Name != "Cart.print" || f.Unit != "src/cart" || f.Line != 18) {
			t.Errorf("print = %+v", f)
		}
	}
}

// A function deleted from a module that's still imported by name is
// called from where it's imported, even through a barrel file.
func TestJSRemovedImported(t *testing.T) {
	dir := gitRepo(t)
	write(t, dir, "package.json", `{"name": "x"}`)
	write(t, dir, "src/util/strings.js", "export function slug(s) {\n  return s\n}\nexport function keep() {}\n")
	write(t, dir, "src/util/index.js", "export { slug, keep } from './strings'\n")
	write(t, dir, "src/page.js", "import { slug } from './util'\n\nexport function page() {\n  return slug('a')\n}\n")
	write(t, dir, "src/cjs.js", "const { slug } = require('./util/strings')\nmodule.exports = function () { return slug('b') }\n")
	run(t, dir, "add", ".")
	run(t, dir, "commit", "-q", "-m", "init")
	run(t, dir, "checkout", "-q", "-b", "feat")
	write(t, dir, "src/util/strings.js", "export function keep() {}\n")
	cg := callGraphOf(t, dir, gitfiles.ModeBranch)
	if got := statuses(cg); !reflect.DeepEqual(got, map[string]string{"src/util/strings#slug": FuncRemoved}) {
		t.Errorf("statuses = %v", got)
	}
	if w := "removed-called:src/util/strings#slug src/cjs.js src/page.js"; !contains(findingKeys(cg), w) {
		t.Errorf("missing %s in %v", w, findingKeys(cg))
	}
}

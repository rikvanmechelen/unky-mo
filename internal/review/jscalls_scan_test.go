package review

import (
	"reflect"
	"strings"
	"testing"
)

// jsDefsOf scans src as path p and lists each definition as
// "qualified: calls [line-end]", calls written recv.name, @ref for
// references.
func jsDefsOf(t *testing.T, p, src string) (map[string]string, hFile) {
	t.Helper()
	f := (&jsCalls{}).scanFile(p, src)
	if !f.OK {
		t.Fatal("not OK")
	}
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
		k := qualify(d.Owner, d.Name)
		if d.IsClass {
			k += "/class"
		}
		if d.Static {
			k += "/static"
		}
		got[k] = strings.Join(calls, " ") + rangeOf(d)
	}
	return got, f
}

func TestJSScanDefs(t *testing.T) {
	src := `import { helper as h } from "./util"

// nope() in a comment isn't a call
export function top(a, b = 1) {
  const s = "fake()" + ` + "`${fake()}`" + `
  const re = /fake()/g
  function inner() {
    return other()
  }
  return inner(h(a))
}

export const arrow = async (x: number): Promise<number> => {
  return x.toFixed(2)
}

const short = (x) => twice(x)
  .then(done)
const one = y => y + 1

export function over(a: string): string;
export function over(a: number): number;
export function over(a: any) {
  return a
}

class Base {}

export class Cart extends Base implements Thing {
  static make(): Cart {
    return new Cart()
  }

  #secret() {
    return this.#secret
  }

  get total() {
    return sum(this.items)
  }

  async load<T>(id: string) {
    await fetch(id)
  }

  handle = (e) => {
    this.save(e)
  }

  abstract later(): void;

  constructor() {
    super()
    if (x) {
      run()
    }
  }
}

main()
`
	got, f := jsDefsOf(t, "src/x.ts", src)
	// Arguments show up as references (a@ref); the resolver drops those
	// that aren't functions.
	want := map[string]string{
		"top":              "inner h a@ref [4-11]",
		"top.inner":        "other [7-9]",
		"arrow":            "x.toFixed [13-15]",
		"short":            "twice x@ref ?.then done@ref [17-18]",
		"one":              " [19-19]",
		"over":             " [23-25]", // the overloads are neither definitions nor calls
		"Base/class":       " [27-27]",
		"Cart/class":       " [29-58]",
		"Cart.make/static": "Cart [30-32]",
		"Cart.#secret":     " [34-36]",
		"Cart.total":       "sum this.items@ref [38-40]",
		"Cart.load":        "fetch id@ref [42-44]",
		"Cart.handle":      "this.save e@ref [46-48]",
		"Cart.constructor": "super.constructor x@ref run [52-57]",
		"<module>":         "main [60-60]",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("defs:\n%v\nwant\n%v", got, want)
	}
	if len(f.Classes) != 2 || !reflect.DeepEqual(f.Classes[1].Bases, []string{"Base"}) {
		t.Errorf("classes = %+v", f.Classes)
	}
	var imps []string
	for _, i := range f.Imports {
		imps = append(imps, i.Local+"="+i.Spec+":"+i.Name)
	}
	if want := []string{"h=./util:helper"}; !reflect.DeepEqual(imps, want) {
		t.Errorf("imports = %v, want %v", imps, want)
	}
}

func TestJSScanJSXAndExports(t *testing.T) {
	src := `import Panel, { Row as R } from "./Panel"
import * as ui from "./ui"
const { a: alias, b } = require("./cjs")
const lib = require("./lib")
export * from "./more"
export * as more from "./more"
export { x as y, default as Z } from "./z"

function save() {}

export default function App({ items }) {
  const s = "<Fake />"
  return (
    <Panel onSave={save}>
      {items.map(fmt)}
      <ui.Card title="a" />
      <R />
      <div>{b ? <Inner/> : null}</div>
      {/* <Commented /> */}
    </Panel>
  )
}

const list: Array<Item> = []
export { save as store }
`
	got, f := jsDefsOf(t, "src/App.tsx", src)
	want := map[string]string{
		"save": " [9-9]",
		"App":  "Panel save@ref items.map fmt@ref ui.Card R Inner [11-22]",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("defs:\n%v\nwant\n%v", got, want)
	}
	var imps []string
	for _, i := range f.Imports {
		imps = append(imps, i.Local+"="+i.Spec+":"+i.Name)
	}
	if want := []string{"Panel=./Panel:default", "R=./Panel:Row", "ui=./ui:", "alias=./cjs:a", "b=./cjs:b", "lib=./lib:"}; !reflect.DeepEqual(imps, want) {
		t.Errorf("imports = %v, want %v", imps, want)
	}
	var re []string
	for _, i := range f.Reexports {
		re = append(re, i.Local+"="+i.Spec+":"+i.Name)
	}
	if want := []string{"*=./more:", "more=./more:", "y=./z:x", "Z=./z:default"}; !reflect.DeepEqual(re, want) {
		t.Errorf("reexports = %v, want %v", re, want)
	}
	if want := map[string]string{"default": "App", "store": "save"}; !reflect.DeepEqual(f.Exports, want) {
		t.Errorf("exports = %v, want %v", f.Exports, want)
	}
}

func TestJSScanDefaultExports(t *testing.T) {
	for _, tc := range []struct{ src, want string }{
		{"function Button() {}\nexport default Button\n", "Button"},
		{"export default class Cart {}\n", "Cart"},
		{"export default function save() {}\n", "save"},
		{"function List() {}\nexport default memo(List);\n", "List"},
		{"function run() {}\nmodule.exports = run\n", "run"},
		{"function run() {}\nexport { run as default }\n", "run"},
	} {
		f := (&jsCalls{}).scanFile("a.js", tc.src)
		if got := f.Exports["default"]; got != tc.want {
			t.Errorf("%q: default = %q, want %q", tc.src, got, tc.want)
		}
	}
	// Anonymous: the definition is called "default".
	got, _ := jsDefsOf(t, "a.ts", "export default function () {\n  go()\n}\n")
	if want := map[string]string{"default": "go [1-3]"}; !reflect.DeepEqual(got, want) {
		t.Errorf("anonymous default = %v, want %v", got, want)
	}
	got, _ = jsDefsOf(t, "a.ts", "export default class {\n  m() {}\n}\n")
	if want := map[string]string{"default/class": " [1-3]", "default.m": " [2-2]"}; !reflect.DeepEqual(got, want) {
		t.Errorf("anonymous default class = %v, want %v", got, want)
	}
	got, _ = jsDefsOf(t, "a.js", "exports.save = function (x) {\n  write(x)\n}\nmodule.exports.load = (x) => read(x)\n")
	if want := map[string]string{"save": "write x@ref [1-3]", "load": "read x@ref [4-4]"}; !reflect.DeepEqual(got, want) {
		t.Errorf("commonjs = %v, want %v", got, want)
	}
}

func TestJSScanComponents(t *testing.T) {
	vue := `<template>
  <!-- <Ghost /> -->
  <Card @click="open" @close="close(1)">{{ format(total) }}</Card>
</template>

<script>
import Card from "./Card.vue"
export default {
  components: { Card },
  methods: {
    open() {
      this.close(2)
    },
    close: function (n) {
      emit(n)
    },
  },
}
</script>
`
	got, _ := jsDefsOf(t, "src/Cart.vue", vue)
	want := map[string]string{
		"default/class": "Card open@ref close format [8-18]",
		"default.open":  "this.close [11-13]",
		"default.close": "emit n@ref [14-16]",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("vue defs:\n%v\nwant\n%v", got, want)
	}
	svelte := `<script lang="ts">
  import Row from "./Row.svelte"
  function pick(x) { return x }
  init()
</script>

<Row on:click={pick} />
{#each rows as r}<Row />{/each}
`
	got, _ = jsDefsOf(t, "src/List.svelte", svelte)
	want = map[string]string{
		"pick":    " [3-3]",
		"default": "init Row pick@ref Row [1-8]",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("svelte defs:\n%v\nwant\n%v", got, want)
	}
}

func TestJSScanNoCallsInLiterals(t *testing.T) {
	src := "function f() {\n" +
		"  // a() in a comment\n" +
		"  /* b() in a block\n" +
		"     comment */\n" +
		"  const s = \"c()\" + 'd()'\n" +
		"  const t = `e() ${g(1)}`\n" +
		"  const r = /h()/.test(s)\n" +
		"  return ok()\n" +
		"}\n"
	got, _ := jsDefsOf(t, "x.js", src)
	if want := map[string]string{"f": "?.test s@ref ok [1-9]"}; !reflect.DeepEqual(got, want) {
		t.Errorf("defs = %v, want %v", got, want)
	}
}

func TestJSScanClassExpressionsAndTypes(t *testing.T) {
	src := `export const Store = class extends Base<Item> {
  save() { persist() }
}

interface Repo {
  save(x: Item): void
  load(): Item
}

type Props = {
  onSave: (x: Item) => void
}

declare function external(x: number): void;
`
	got, f := jsDefsOf(t, "x.ts", src)
	want := map[string]string{"Store/class": " [1-3]", "Store.save": "persist [2-2]"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("defs = %v, want %v", got, want)
	}
	if len(f.Classes) != 1 || !reflect.DeepEqual(f.Classes[0].Bases, []string{"Base"}) {
		t.Errorf("classes = %+v", f.Classes)
	}
}

func TestJSScanUnbalanced(t *testing.T) {
	if f := (&jsCalls{}).scanFile("x.ts", "function f() {\n  if (x) {\n"); f.OK {
		t.Error("an unclosed block parsed")
	}
}

func TestJSBodyHash(t *testing.T) {
	body := func(src string) string { return (&jsCalls{}).scanFile("x.ts", src).Defs[0].Body }
	a := body("function f() {\n  return 1 // one\n}\n")
	if b := body("function f() {\n  // a comment\n  return   1\n}\n"); a != b {
		t.Error("a comment or spacing edit changed the body hash")
	}
	if c := body("function f() {\n  return `2`\n}\n"); a == c {
		t.Error("a change in a template literal kept the hash")
	}
	outer := func(inner string) string { return body("class A {\n  m() {\n    " + inner + "\n  }\n}\n") }
	if outer("a()") != outer("b()") {
		t.Error("a method's change changed its class's hash")
	}
}

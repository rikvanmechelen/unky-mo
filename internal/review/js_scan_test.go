package review

import (
	"reflect"
	"testing"
)

func specs(refs []ref) []string {
	var out []string
	for _, r := range refs {
		out = append(out, r.to)
	}
	return out
}

func TestJSImports(t *testing.T) {
	src := `import React from "react";
import { a,
  b } from './multi';
import type { T } from "../types";
import './side-effect.css';
export * from "./reexport";
export { x as y } from './named';
// import ghost from './comment';
/* import ghost2 from './block' */
const s = "import fake from './in-string'";
const t = ` + "`import tmpl from './template' ${`nested`}`" + `;
const re = /import r from '.\/regex'/g;
const lazy = () => import('./lazy');
const cjs = require("./cjs");
const div = total / count / 2; import('./after-division');
`
	got := specs(jsImports(jsCode(src)))
	want := []string{"react", "./multi", "../types", "./side-effect.css", "./reexport", "./named", "./lazy", "./cjs", "./after-division"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v\nwant %v", got, want)
	}
	for _, r := range jsImports(jsCode(src)) {
		if r.to == "./lazy" && r.line != 13 {
			t.Errorf("lazy on line %d, want 13", r.line)
		}
	}
}

func TestComponentScript(t *testing.T) {
	vue := "<template><div>import x from './no'</div></template>\n<script setup>\nimport Card from './Card.vue'\n</script>\n"
	if got := specs(jsImports(jsCode(componentScript("a.vue", vue)))); !reflect.DeepEqual(got, []string{"./Card.vue"}) {
		t.Errorf("vue: %v", got)
	}
	astro := "---\nimport Base from '../layouts/Base.astro';\n---\n<Base>import y from './no'</Base>\n<script>import './client.ts'</script>\n"
	if got := specs(jsImports(jsCode(componentScript("p.astro", astro)))); !reflect.DeepEqual(got, []string{"../layouts/Base.astro", "./client.ts"}) {
		t.Errorf("astro: %v", got)
	}
}

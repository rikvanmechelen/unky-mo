package review

import (
	"encoding/json"
	"os/exec"
	"reflect"
	"strings"
	"testing"
)

// nodeRepo is a TypeScript app with a workspace package, tsconfig paths
// (through extends), package.json imports and a Rails importmap; the branch
// "feat" adds one import of each kind.
func nodeRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	dir := t.TempDir()
	run(t, dir, "init", "-q", "-b", "main")
	run(t, dir, "config", "user.email", "test@example.com")
	run(t, dir, "config", "user.name", "Test")
	run(t, dir, "config", "commit.gpgsign", "false")
	write(t, dir, "package.json", `{"name": "app", "workspaces": ["packages/*"], "imports": {"#lib/*": "./src/lib/*"}}`)
	write(t, dir, "tsconfig.base.json", "{\n  // shared\n  \"compilerOptions\": {\"baseUrl\": \".\", \"paths\": {\"@/*\": [\"src/*\"],}},\n}\n")
	write(t, dir, "tsconfig.json", `{"extends": "./tsconfig.base.json"}`)
	write(t, dir, "src/components/Button.tsx", "export const Button = () => null\n")
	write(t, dir, "src/lib/format.ts", "export const fmt = (x: number) => String(x)\n")
	write(t, dir, "src/utils/math.ts", "export const add = (a: number, b: number) => a + b\n")
	write(t, dir, "src/hooks/useX.ts", "export const useX = () => 1\n")
	write(t, dir, "src/pages/index.tsx", "export const Page = () => null\n")
	write(t, dir, "packages/ui/package.json", `{"name": "@acme/ui"}`)
	write(t, dir, "packages/ui/src/index.ts", "export * from './Card'\n")
	write(t, dir, "packages/ui/src/Card.tsx", "export const Card = () => null\n")
	write(t, dir, "config/importmap.rb", "pin \"application\"\npin_all_from \"app/javascript/controllers\", under: \"controllers\"\npin \"@hotwired/stimulus\", to: \"stimulus.min.js\"\n")
	write(t, dir, "app/javascript/application.js", "import '@hotwired/stimulus'\n")
	write(t, dir, "app/javascript/controllers/hello_controller.js", "export default class {}\n")
	run(t, dir, "add", ".")
	run(t, dir, "commit", "-q", "-m", "init")

	run(t, dir, "checkout", "-q", "-b", "feat")
	write(t, dir, "src/components/Button.tsx", "import { Page } from '@/pages/index'\nexport const Button = () => Page\n")
	write(t, dir, "src/hooks/useX.ts", "import { fmt } from '#lib/format'\nexport const useX = () => fmt(1)\n")
	write(t, dir, "src/lib/format.ts", "import { add } from '../utils/math.js'\nimport React from 'react'\nexport const fmt = (x: number) => String(add(x, 0))\n")
	write(t, dir, "src/pages/index.tsx", "import { Card } from '@acme/ui'\nexport const Page = () => Card\n")
	write(t, dir, "app/javascript/application.js", "import '@hotwired/stimulus'\nimport 'controllers/hello_controller'\n")
	return dir
}

func TestNodeArchitecture(t *testing.T) {
	a := analyze(t, nodeRepo(t))
	want := []string{
		"+app/javascript>app/javascript/controllers",
		"+src/components>src/pages",
		"+src/hooks>src/lib",
		"+src/lib>src/utils",
		"+src/pages>packages/ui/src",
	}
	if got := edgeKeys(a.Edges); !reflect.DeepEqual(got, want) {
		t.Errorf("edges %v\nwant  %v", got, want)
	}
	for _, e := range a.Edges {
		if e.Lang != "node" || e.Approx {
			t.Errorf("edge %+v: want exact node", e)
		}
		if e.From == "src/components" && e.Violation != "node: shared code must not import src/pages" {
			t.Errorf("components → pages: %+v", e)
		}
	}
	if a.Violations != 1 || !strings.Contains(strings.Join(a.Rules.Presets, ","), "node") {
		t.Errorf("violations %d, rules %+v", a.Violations, a.Rules)
	}
}

func TestNodeUnits(t *testing.T) {
	l := &nodeLang{}
	for p, want := range map[string]string{
		"src/components/Button.tsx":             "src/components",
		"src/features/cart/hooks/useCart.ts":    "src/features",
		"src/main.ts":                           "src",
		"app/javascript/controllers/kiosk/x.js": "app/javascript/controllers",
		"app/javascript/application.js":         "app/javascript",
		"packages/ui/src/Card.tsx":              "packages/ui/src",
		"packages/ui/Button.tsx":                "packages/ui",
		"vite.config.ts":                        "",
		"scripts/build.mjs":                     "scripts",
	} {
		if got := l.unit(p); got != want {
			t.Errorf("unit(%s) = %q, want %q", p, got, want)
		}
	}
	for _, c := range []struct{ pat, target, spec, want string }{
		{"@/*", "src/*", "@/pages/index", "src/pages/index"},
		{"@/*", "src/*", "react", ""},
		{"#config", "./src/config.ts", "#config", "./src/config.ts"},
		{"#lib/*.js", "./src/lib/*.ts", "#lib/a.js", "./src/lib/a.ts"},
	} {
		if got := matchStar(c.pat, c.target, c.spec); got != c.want {
			t.Errorf("matchStar(%s, %s, %s) = %q", c.pat, c.target, c.spec, got)
		}
	}
	var doc struct {
		A string `json:"a"`
		C []int  `json:"c"`
	}
	if err := json.Unmarshal([]byte(stripJSONC("{\n // c\n \"a\": \"http://x\", /* b */ \"c\": [1,],\n}")), &doc); err != nil || doc.A != "http://x" || len(doc.C) != 1 {
		t.Errorf("stripJSONC: %+v, %v", doc, err)
	}
}

// The node preset judges node units only: a Ruby lib → app dependency in
// the same repo isn't flagged by its "lib" layer.
func TestPresetScopedToLanguage(t *testing.T) {
	rs := &ruleSet{layers: []layer{{Name: "node: shared", Paths: []string{"lib"}, Deny: []string{"app"}, lang: "node"}}}
	if got := rs.check("lib", "app/models", "ruby"); got != "" {
		t.Errorf("ruby judged by node preset: %q", got)
	}
	if got := rs.check("lib", "app", "node"); got == "" {
		t.Error("node dependency not judged")
	}
}

func TestNodeSurface(t *testing.T) {
	for p, want := range map[string]string{
		"src/pages/blog/[slug].astro":   "/blog/[slug]",
		"pages/index.tsx":               "/",
		"app/dashboard/page.tsx":        "/dashboard",
		"app/page.tsx":                  "/",
		"app/api/users/route.ts":        "/api/users",
		"src/routes/about/+page.svelte": "/about",
		"app/views/pages/home.html.erb": "",
		"src/components/Page.tsx":       "",
	} {
		if got := fileRoute(p); got != want {
			t.Errorf("fileRoute(%s) = %q, want %q", p, got, want)
		}
	}
	dir := nodeRepo(t)
	write(t, dir, "src/pages/about.tsx", "export default () => null\n")
	write(t, dir, "package.json", `{"name": "app", "workspaces": ["packages/*"], "imports": {"#lib/*": "./src/lib/*"}, "engines": {"node": ">=20"}, "scripts": {"build": "vite build"}}`)
	write(t, dir, "src/lib/env.ts", "export const key = import.meta.env.VITE_API_KEY\n")
	s := analyze(t, dir).Surface
	if got := changeKeys(s.Routes); !reflect.DeepEqual(got, []string{"+/about"}) {
		t.Errorf("routes %v", got)
	}
	if got := changeKeys(s.Deps); !reflect.DeepEqual(got, []string{"+engines.node"}) {
		t.Errorf("deps %v", got)
	}
	if got := changeKeys(s.Config); !reflect.DeepEqual(got, []string{"+scripts.build"}) {
		t.Errorf("config %v", got)
	}
	if got := changeKeys(s.Env); !reflect.DeepEqual(got, []string{"+VITE_API_KEY"}) {
		t.Errorf("env %v", got)
	}
}

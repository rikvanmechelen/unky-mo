package review

import (
	"encoding/json"
	"path"
	"regexp"
	"strings"

	"github.com/rvanmech/unky-mo/internal/gitfiles"
)

// The Node part of the contract surface: file-based routes (pages added or
// removed in Next, Nuxt, Astro, SvelteKit, Remix), and package.json's
// engines, scripts and bin.

var fileRouteRes = []*regexp.Regexp{
	regexp.MustCompile(`(?:^|/)(?:src/)?pages/(.+)\.(?:tsx?|jsx?|vue|astro|svelte|mdx?)$`),              // Next, Nuxt, Astro
	regexp.MustCompile(`(?:^|/)(?:src/)?app/(?:(.+)/)?(?:page|route)\.(?:tsx?|jsx?)$`),                  // Next app router
	regexp.MustCompile(`(?:^|/)src/routes/(?:(.+)/)?\+(?:page|server)(?:\.server)?\.(?:svelte|ts|js)$`), // SvelteKit
	regexp.MustCompile(`(?:^|/)app/routes/(.+)\.(?:tsx?|jsx?)$`),                                        // Remix
}

// fileRoute is the route a page file serves ("/blog/[slug]"), or "".
func fileRoute(p string) string {
	if strings.HasPrefix(p, "app/views/") || strings.Contains(p, "/node_modules/") {
		return "" // Rails views live in app/, too
	}
	for _, re := range fileRouteRes {
		if m := re.FindStringSubmatch(p); m != nil {
			r := "/" + strings.TrimSuffix(m[1], "/index")
			if r == "/index" {
				r = "/"
			}
			return r
		}
	}
	return ""
}

func nodeSurface(r *repo, s *Surface, all []gitfiles.OverviewFile, files []*file) {
	for _, f := range all {
		route := fileRoute(f.Path)
		if route == "" || isTest(f.Path) {
			continue
		}
		switch f.Status {
		case "A", "?":
			s.Routes = append(s.Routes, Change{Op: OpAdded, Name: route, Detail: "page " + path.Base(f.Path), Path: f.Path})
		case "D":
			s.Routes = append(s.Routes, Change{Op: OpRemoved, Name: route, Detail: "page " + path.Base(f.Path), Path: f.Path})
		}
	}
	for _, f := range files {
		if path.Base(f.Path) != "package.json" {
			continue
		}
		b, a := packageFields(f.before), packageFields(f.after)
		for _, field := range []struct {
			key  string
			into *[]Change
		}{{"engines", &s.Deps}, {"scripts", &s.Config}, {"bin", &s.Config}} {
			bm, am := b[field.key], a[field.key]
			for k, v := range am {
				switch old, had := bm[k]; {
				case !had:
					*field.into = append(*field.into, Change{Op: OpAdded, Name: field.key + "." + k, Detail: v, Path: f.Path})
				case old != v:
					*field.into = append(*field.into, Change{Op: OpChanged, Name: field.key + "." + k, Detail: old + " → " + v, Path: f.Path})
				}
			}
			for k, v := range bm {
				if _, has := am[k]; !has {
					*field.into = append(*field.into, Change{Op: OpRemoved, Name: field.key + "." + k, Detail: v, Path: f.Path})
				}
			}
		}
	}
}

// packageFields reads package.json's string maps (engines, scripts, bin;
// a string bin counts as one entry named after the package).
func packageFields(src *string) map[string]map[string]string {
	out := map[string]map[string]string{}
	if src == nil {
		return out
	}
	var doc map[string]json.RawMessage
	if json.Unmarshal([]byte(*src), &doc) != nil {
		return out
	}
	for _, k := range []string{"engines", "scripts", "bin"} {
		var m map[string]string
		if json.Unmarshal(doc[k], &m) == nil {
			out[k] = m
			continue
		}
		var one, name string
		if json.Unmarshal(doc[k], &one) == nil && one != "" {
			_ = json.Unmarshal(doc["name"], &name)
			out[k] = map[string]string{name: one}
		}
	}
	return out
}

package review

import (
	"path"
	"regexp"
	"strings"

	"github.com/rvanmech/unky-mo/internal/gitfiles"
)

// The Rails part of the contract surface: controller actions (the HTTP
// surface behind the routes), initializers, and the locked versions of
// the gems the Gemfile names.

// railsSurface adds the Rails items to s.
func railsSurface(r *repo, s *Surface, all []gitfiles.OverviewFile, files []*file) {
	for _, f := range files {
		if strings.HasPrefix(f.Path, "app/controllers/") && strings.HasSuffix(f.Path, "_controller.rb") && !isTest(f.Path) {
			s.Routes = append(s.Routes, actionChanges(f)...)
		}
		if path.Base(f.Path) == "Gemfile.lock" {
			s.Deps = append(s.Deps, lockChanges(r, f)...)
		}
	}
	for _, f := range all {
		if !strings.HasPrefix(f.Path, "config/initializers/") {
			continue
		}
		switch f.Status {
		case "A", "?":
			s.Config = append(s.Config, Change{Op: OpAdded, Name: f.Path, Detail: "initializer", Path: f.Path})
		case "D":
			s.Config = append(s.Config, Change{Op: OpRemoved, Name: f.Path, Detail: "initializer", Path: f.Path})
		}
	}
}

// controllerName is the constant a controller file defines, by Rails'
// naming convention: app/controllers/audio/tours_controller.rb is
// Audio::ToursController.
func controllerName(p string) string {
	rel := strings.TrimSuffix(strings.TrimPrefix(p, "app/controllers/"), ".rb")
	segs := strings.Split(rel, "/")
	for i, s := range segs {
		parts := strings.Split(s, "_")
		for j, w := range parts {
			if w != "" {
				parts[j] = strings.ToUpper(w[:1]) + w[1:]
			}
		}
		segs[i] = strings.Join(parts, "")
	}
	return strings.Join(segs, "::")
}

var (
	defRe        = regexp.MustCompile(`^\s*def\s+([a-z_][A-Za-z0-9_]*[!?]?)\s*(?:\(|$|\s)`)
	visibilityRe = regexp.MustCompile(`^\s*(private|protected)\s*$`)
)

// controllerActions lists a controller's public instance methods (its
// actions), up to the first bare private/protected.
func controllerActions(src string) map[string]int {
	out := map[string]int{}
	for i, line := range strings.Split(rubyCode(src), "\n") {
		if visibilityRe.MatchString(line) {
			break
		}
		if m := defRe.FindStringSubmatch(line); m != nil {
			out[m[1]] = i + 1
		}
	}
	return out
}

func actionChanges(f *file) []Change {
	before, after := map[string]int{}, map[string]int{}
	if f.before != nil {
		before = controllerActions(*f.before)
	}
	if f.after != nil {
		after = controllerActions(*f.after)
	}
	name := controllerName(f.Path)
	var out []Change
	for a, line := range after {
		if _, had := before[a]; !had {
			out = append(out, Change{Op: OpAdded, Name: name + "#" + a, Detail: "controller action", Path: f.Path, Line: line})
		}
	}
	for a, line := range before {
		if _, has := after[a]; !has {
			out = append(out, Change{Op: OpRemoved, Name: name + "#" + a, Detail: "controller action", Path: f.Path, Line: line})
		}
	}
	return out
}

var lockSpecRe = regexp.MustCompile(`(?m)^    ([A-Za-z0-9_.-]+) \(([^)]+)\)$`)

// lockVersions reads a Gemfile.lock's resolved gem versions.
func lockVersions(src string) map[string]depEntry {
	out := map[string]depEntry{}
	for _, m := range lockSpecRe.FindAllStringSubmatchIndex(src, -1) {
		out[src[m[2]:m[3]]] = depEntry{version: src[m[4]:m[5]], line: 1 + strings.Count(src[:m[0]], "\n")}
	}
	return out
}

// lockChanges reports version changes of the gems the Gemfile names
// directly (the rest is churn underneath them).
func lockChanges(r *repo, f *file) []Change {
	gemfile := r.readAfter(path.Join(path.Dir(f.Path), "Gemfile"))
	if gemfile == nil {
		return nil
	}
	direct := gemfileGems(*gemfile)
	before, after := map[string]depEntry{}, map[string]depEntry{}
	if f.before != nil {
		before = lockVersions(*f.before)
	}
	if f.after != nil {
		after = lockVersions(*f.after)
	}
	var out []Change
	for name := range direct {
		b, hadB := before[name]
		a, hasA := after[name]
		if hadB && hasA && b.version != a.version {
			out = append(out, Change{Op: OpChanged, Name: name, Detail: b.version + " → " + a.version + " (locked)", Path: f.Path, Line: a.line})
		}
	}
	return out
}

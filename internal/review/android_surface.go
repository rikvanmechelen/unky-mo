package review

import (
	"path"
	"regexp"
	"strings"

	"github.com/BurntSushi/toml"
)

// The Android/Gradle part of the contract surface: manifest permissions
// and exported components, SDK levels, Gradle dependencies (build files
// and the version catalog), Retrofit endpoints and Room schema versions.

var (
	permissionRe  = regexp.MustCompile(`<uses-permission(?:-sdk-23)?\b[^>]*android:name="([^"]+)"`)
	componentRe   = regexp.MustCompile(`(?s)<(activity|activity-alias|service|receiver|provider)\b([^>]*)>`)
	attrNameRe    = regexp.MustCompile(`android:name="([^"]+)"`)
	exportedRe    = regexp.MustCompile(`android:exported="true"`)
	sdkRe         = regexp.MustCompile(`\b(minSdk|targetSdk|compileSdk)(?:Version)?\s*(?:=|\s)\s*(\d+)`)
	gradleDepRe   = regexp.MustCompile(`\b(implementation|api|kapt|ksp|compileOnly|runtimeOnly|annotationProcessor)\s*\(?\s*["']([\w.-]+:[\w.-]+)(?::([^"']+))?["']`)
	retrofitRe    = regexp.MustCompile(`@(GET|POST|PUT|PATCH|DELETE|HEAD)\(\s*(?:value\s*=\s*)?"([^"]*)"`)
	roomVersionRe = regexp.MustCompile(`@Database\((?s:[^)]*?)\bversion\s*=\s*(\d+)`)
)

// manifestItems reads a manifest's permissions and exported components.
func manifestItems(src string) (perms, exported map[string]int) {
	perms, exported = map[string]int{}, map[string]int{}
	lineOf := func(i int) int { return 1 + strings.Count(src[:i], "\n") }
	for _, m := range permissionRe.FindAllStringSubmatchIndex(src, -1) {
		perms[src[m[2]:m[3]]] = lineOf(m[0])
	}
	for _, m := range componentRe.FindAllStringSubmatchIndex(src, -1) {
		attrs := src[m[4]:m[5]]
		if !exportedRe.MatchString(attrs) {
			continue
		}
		if n := attrNameRe.FindStringSubmatch(attrs); n != nil {
			exported["exported "+src[m[2]:m[3]]+" "+n[1]] = lineOf(m[0])
		}
	}
	return perms, exported
}

// setChanges compares two name → line sets.
func setChanges(before, after map[string]int, p, detail string) []Change {
	var out []Change
	for n, line := range after {
		if _, had := before[n]; !had {
			out = append(out, Change{Op: OpAdded, Name: n, Detail: detail, Path: p, Line: line})
		}
	}
	for n, line := range before {
		if _, has := after[n]; !has {
			out = append(out, Change{Op: OpRemoved, Name: n, Detail: detail, Path: p, Line: line})
		}
	}
	return out
}

// valueChanges compares two name → (value, line) maps.
func valueChanges(before, after map[string]depEntry, p, detail string) []Change {
	var out []Change
	for n, a := range after {
		b, had := before[n]
		switch {
		case !had:
			out = append(out, Change{Op: OpAdded, Name: n, Detail: strings.TrimSpace(a.version + " " + detail), Path: p, Line: a.line})
		case b.version != a.version:
			out = append(out, Change{Op: OpChanged, Name: n, Detail: b.version + " → " + a.version, Path: p, Line: a.line})
		}
	}
	for n, b := range before {
		if _, has := after[n]; !has {
			out = append(out, Change{Op: OpRemoved, Name: n, Detail: strings.TrimSpace(b.version + " " + detail), Path: p, Line: b.line})
		}
	}
	return out
}

func matchEntries(re *regexp.Regexp, src string, name func(m []string) string, value func(m []string) string) map[string]depEntry {
	out := map[string]depEntry{}
	for _, idx := range re.FindAllStringSubmatchIndex(src, -1) {
		m := make([]string, len(idx)/2)
		for i := range m {
			if idx[2*i] >= 0 {
				m[i] = src[idx[2*i]:idx[2*i+1]]
			}
		}
		out[name(m)] = depEntry{version: value(m), line: 1 + strings.Count(src[:idx[0]], "\n")}
	}
	return out
}

// catalogLibraries reads gradle/libs.versions.toml: library alias →
// module and resolved version.
func catalogLibraries(src string) map[string]depEntry {
	var doc struct {
		Versions  map[string]any `toml:"versions"`
		Libraries map[string]any `toml:"libraries"`
		Plugins   map[string]any `toml:"plugins"`
	}
	out := map[string]depEntry{}
	if _, err := toml.Decode(src, &doc); err != nil {
		return out
	}
	version := func(v any) string {
		switch t := v.(type) {
		case string:
			return t
		case map[string]any:
			if ref, ok := t["ref"].(string); ok {
				s, _ := doc.Versions[ref].(string)
				return s
			}
		}
		return ""
	}
	add := func(kind string, entries map[string]any) {
		for alias, v := range entries {
			name, ver := alias, ""
			switch t := v.(type) {
			case string: // "group:artifact:version"
				parts := strings.Split(t, ":")
				name = strings.Join(parts[:min(2, len(parts))], ":")
				if len(parts) > 2 {
					ver = parts[2]
				}
			case map[string]any:
				if m, ok := t["module"].(string); ok {
					name = m
				} else if g, ok := t["group"].(string); ok {
					a, _ := t["name"].(string)
					name = g + ":" + a
				} else if id, ok := t["id"].(string); ok {
					name = id
				}
				ver = version(t["version"])
			}
			line := 0
			if i := strings.Index(src, alias); i >= 0 {
				line = 1 + strings.Count(src[:i], "\n")
			}
			if kind == "plugin" {
				name += " (plugin)"
			}
			out[name] = depEntry{version: ver, line: line}
		}
	}
	add("library", doc.Libraries)
	add("plugin", doc.Plugins)
	return out
}

func androidSurface(s *Surface, files []*file) {
	text := func(p *string) string {
		if p == nil {
			return ""
		}
		return *p
	}
	for _, f := range files {
		b, a := text(f.before), text(f.after)
		switch base := path.Base(f.Path); {
		case base == "AndroidManifest.xml":
			bp, be := manifestItems(b)
			ap, ae := manifestItems(a)
			s.Permissions = append(s.Permissions, setChanges(bp, ap, f.Path, "permission")...)
			s.Permissions = append(s.Permissions, setChanges(be, ae, f.Path, "")...)
		case base == "build.gradle" || base == "build.gradle.kts":
			sdk := func(m []string) string { return m[1] }
			val := func(m []string) string { return m[2] }
			s.Deps = append(s.Deps, valueChanges(matchEntries(sdkRe, b, sdk, val), matchEntries(sdkRe, a, sdk, val), f.Path, "")...)
			name := func(m []string) string { return m[2] }
			ver := func(m []string) string { return strings.TrimSpace(m[3] + " (" + m[1] + ")") }
			s.Deps = append(s.Deps, valueChanges(matchEntries(gradleDepRe, b, name, ver), matchEntries(gradleDepRe, a, name, ver), f.Path, "")...)
		case base == "libs.versions.toml":
			s.Deps = append(s.Deps, valueChanges(catalogLibraries(b), catalogLibraries(a), f.Path, "")...)
		case strings.HasSuffix(base, ".kt") || strings.HasSuffix(base, ".java"):
			if isTest(f.Path) {
				continue
			}
			route := func(m []string) string { return m[1] + " " + m[2] }
			none := func([]string) string { return "" }
			bc, ac := jvmScan(b, true), jvmScan(a, true)
			for _, c := range valueChanges(matchEntries(retrofitRe, bc, route, none), matchEntries(retrofitRe, ac, route, none), f.Path, "Retrofit") {
				s.Routes = append(s.Routes, c)
			}
			room := func([]string) string { return "Room database version" }
			v := func(m []string) string { return m[1] }
			s.Migrations = append(s.Migrations, valueChanges(matchEntries(roomVersionRe, bc, room, v), matchEntries(roomVersionRe, ac, room, v), f.Path, "")...)
		}
	}
}

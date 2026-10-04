package review

import (
	"path"
	"regexp"
	"sort"
	"strings"
)

// swiftLang analyzes Swift (iOS and other Apple platforms). Units are:
//
//   - SwiftPM targets (Package.swift anywhere in the repo, so an app's local
//     packages count): the target's directory. `import Target` between them
//     is explicit.
//   - Otherwise folders inside the app: two levels (Core/Networking,
//     DesignSystem/Tokens), three under a container (Features/Tickets/Domain),
//     so the layers inside a feature are units of their own.
//
// Within one target nothing is imported, so references are type names
// matched to the unit that declares them, which makes the language's edges
// approximate.
type swiftLang struct {
	idx     *index
	targets map[string]string // SwiftPM target name → its directory
	dirs    []string          // target directories, longest first
	types   map[string]string // declared type → unit ("" when declared in two units)
}

func (l *swiftLang) name() string { return "swift" }
func (l *swiftLang) exact() bool  { return false }
func (l *swiftLang) owns(p string) bool {
	return strings.HasSuffix(p, ".swift") && path.Base(p) != "Package.swift"
}

// swiftContainers hold modules rather than being one.
var swiftContainers = map[string]bool{"Features": true, "Modules": true, "Packages": true, "Sources": true}

var (
	spmTargetRe = regexp.MustCompile(`\.(?:target|executableTarget|macro)\(\s*name:\s*"([^"]+)"([^)]*)`)
	spmPathRe   = regexp.MustCompile(`\bpath:\s*"([^"]+)"`)
)

func (l *swiftLang) detect(idx *index) bool {
	found := false
	for _, p := range idx.paths {
		if l.owns(p) {
			found = true
			break
		}
	}
	if !found {
		return false
	}
	l.idx = idx
	l.targets = map[string]string{}
	for _, p := range idx.paths {
		if path.Base(p) != "Package.swift" {
			continue
		}
		src := idx.read(p)
		if src == nil {
			continue
		}
		pkg := path.Dir(p)
		for _, m := range spmTargetRe.FindAllStringSubmatch(swiftCodeKeepStrings(*src), -1) {
			dir := path.Join(pkg, "Sources", m[1])
			if pm := spmPathRe.FindStringSubmatch(m[2]); pm != nil {
				dir = path.Join(pkg, pm[1])
			}
			l.targets[m[1]] = dir
			l.dirs = append(l.dirs, dir)
		}
	}
	sort.Slice(l.dirs, func(i, j int) bool { return len(l.dirs[i]) > len(l.dirs[j]) })

	l.types = map[string]string{}
	for _, p := range idx.paths {
		if !l.owns(p) || isTest(p) {
			continue
		}
		u := l.unit(p)
		if u == "" {
			continue
		}
		decls, _ := idx.symbols("swiftdecl", p, func(src string) any { return swiftDecls(src) }).([]string)
		for _, d := range decls {
			if prev, ok := l.types[d]; ok && prev != u {
				l.types[d] = ""
				continue
			}
			l.types[d] = u
		}
	}
	return true
}

// swiftCodeKeepStrings is Package.swift as code: only comments blanked
// (target names are strings).
func swiftCodeKeepStrings(src string) string {
	lines := strings.Split(src, "\n")
	for i, l := range lines {
		if c := strings.Index(l, "//"); c >= 0 && strings.Count(l[:c], `"`)%2 == 0 {
			lines[i] = l[:c]
		}
	}
	return strings.Join(lines, "\n")
}

func (l *swiftLang) unit(p string) string {
	for _, d := range l.dirs {
		if strings.HasPrefix(p, d+"/") {
			return d
		}
	}
	segs := strings.Split(path.Dir(p), "/")
	if segs[0] == "." {
		return ""
	}
	n := 2
	if swiftContainers[segs[0]] {
		n = 3
	}
	if len(segs) < n {
		n = len(segs)
	}
	return strings.Join(segs[:n], "/")
}

func (l *swiftLang) refs(p, src string) ([]ref, bool) {
	return l.resolve(p, l.scan(p, src)), true
}

func (l *swiftLang) scan(_, src string) any { return swiftRefs(src) }

func (l *swiftLang) resolve(_ string, scanned any) []ref {
	s, _ := scanned.(swiftScanned)
	var out []ref
	for _, imp := range s.Imports {
		if dir, ok := l.targets[imp.to]; ok {
			out = append(out, ref{to: dir, line: imp.line})
		}
	}
	for _, t := range s.Types {
		if u, ok := l.types[t.to]; ok && u != "" {
			out = append(out, ref{to: u, line: t.line})
		}
	}
	return out
}

func init() {
	views := []string{"**/Views", "**/View", "**/Presentation", "**/ViewControllers", "**/UI", "**/Screens"}
	presets["ios"] = preset{
		lang: "swift",
		detect: func(idx *index) bool {
			for _, p := range idx.paths {
				if strings.HasSuffix(p, ".xcodeproj/project.pbxproj") || path.Base(p) == "Package.swift" {
					return true
				}
			}
			return false
		},
		layers: []layer{
			{Name: "models and domain", Paths: []string{"**/Models", "**/Model", "**/Domain"}, Deny: append([]string{"**/Data"}, views...)},
			{Name: "data", Paths: []string{"**/Data", "**/Networking", "**/Services"}, Deny: views},
			{Name: "design system", Paths: []string{"DesignSystem", "**/DesignSystem"}, Deny: []string{"Features"}},
		},
	}
}

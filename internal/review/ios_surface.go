package review

import (
	"path"
	"regexp"
	"strings"
)

// The iOS part of the contract surface: privacy usage descriptions
// (Info.plist, or XcodeGen's project.yml), entitlements, Swift package and
// CocoaPods dependencies, and the deployment target.

var (
	privacyKeyRe     = regexp.MustCompile(`\b(NS[A-Za-z]+UsageDescription)\b`)
	entitlementRe    = regexp.MustCompile(`<key>((?:com\.apple|aps)[\w.-]*)</key>`)
	spmPackageRe     = regexp.MustCompile(`\.package\(\s*(?:name:\s*"[^"]*"\s*,\s*)?url:\s*"([^"]+)"\s*,\s*([^)]*)\)`)
	xcodegenPkgRe    = regexp.MustCompile(`(?m)^  ([A-Za-z][\w-]*):\s*\n(?:    .*\n)*?    (?:exactVersion|from|version|minorVersion|majorVersion):\s*"?([^"\n]+)"?`)
	podRe            = regexp.MustCompile(`(?m)^\s*pod\s+['"]([^'"]+)['"](?:\s*,\s*['"]([^'"]+)['"])?`)
	resolvedPinRe    = regexp.MustCompile(`"identity"\s*:\s*"([^"]+)"(?s:.*?)"version"\s*:\s*"([^"]+)"`)
	deployTargetRe   = regexp.MustCompile(`\b(IPHONEOS_DEPLOYMENT_TARGET|MACOSX_DEPLOYMENT_TARGET)\s*[=:]\s*"?([\d.]+)`)
	spmPlatformRe    = regexp.MustCompile(`\.(iOS|macOS|watchOS|tvOS|visionOS)\(\s*\.v([\d_]+)\s*\)`)
	xcodegenTargetRe = regexp.MustCompile(`(?m)^\s+(iOS|macOS|watchOS|tvOS|visionOS):\s*"?([\d.]+)"?`)
)

func iosSurface(s *Surface, files []*file) {
	text := func(p *string) string {
		if p == nil {
			return ""
		}
		return *p
	}
	name1 := func(m []string) string { return m[1] }
	val2 := func(m []string) string { return strings.TrimSpace(m[2]) }
	none := func([]string) string { return "" }
	for _, f := range files {
		b, a := text(f.before), text(f.after)
		base := path.Base(f.Path)
		switch {
		case base == "Info.plist" || base == "project.yml" || strings.HasSuffix(base, ".plist") && !strings.HasSuffix(base, ".entitlements"):
			s.Permissions = append(s.Permissions, valueChanges(matchEntries(privacyKeyRe, b, name1, none), matchEntries(privacyKeyRe, a, name1, none), f.Path, "privacy usage description")...)
			if base == "project.yml" {
				s.Deps = append(s.Deps, valueChanges(matchEntries(xcodegenPkgRe, b, name1, val2), matchEntries(xcodegenPkgRe, a, name1, val2), f.Path, "")...)
				s.Deps = append(s.Deps, valueChanges(matchEntries(xcodegenTargetRe, b, func(m []string) string { return m[1] + " deployment target" }, val2), matchEntries(xcodegenTargetRe, a, func(m []string) string { return m[1] + " deployment target" }, val2), f.Path, "")...)
			}
		case strings.HasSuffix(base, ".entitlements"):
			s.Permissions = append(s.Permissions, valueChanges(matchEntries(entitlementRe, b, name1, none), matchEntries(entitlementRe, a, name1, none), f.Path, "entitlement")...)
		case base == "Package.swift":
			s.Deps = append(s.Deps, valueChanges(matchEntries(spmPackageRe, b, name1, val2), matchEntries(spmPackageRe, a, name1, val2), f.Path, "")...)
			plat := func(m []string) string { return m[1] + " platform" }
			ver := func(m []string) string { return strings.ReplaceAll(m[2], "_", ".") }
			s.Deps = append(s.Deps, valueChanges(matchEntries(spmPlatformRe, b, plat, ver), matchEntries(spmPlatformRe, a, plat, ver), f.Path, "")...)
		case base == "Package.resolved":
			s.Deps = append(s.Deps, valueChanges(matchEntries(resolvedPinRe, b, name1, val2), matchEntries(resolvedPinRe, a, name1, val2), f.Path, "(resolved)")...)
		case base == "Podfile":
			s.Deps = append(s.Deps, valueChanges(matchEntries(podRe, b, name1, val2), matchEntries(podRe, a, name1, val2), f.Path, "")...)
		case strings.HasSuffix(base, ".xcconfig") || base == "project.pbxproj":
			s.Deps = append(s.Deps, valueChanges(matchEntries(deployTargetRe, b, name1, val2), matchEntries(deployTargetRe, a, name1, val2), f.Path, "")...)
		}
	}
}

package project

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// ScanWorkspace discovers projects in the given directories.
// A project is any subdirectory containing a .git directory or file.
func ScanWorkspace(dirs []string) ([]Project, error) {
	var projects []Project
	seen := make(map[string]bool)

	for _, dir := range dirs {
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			if !entry.IsDir() || entry.Name()[0] == '.' || strings.HasSuffix(entry.Name(), ".worktrees") {
				continue
			}
			fullPath := filepath.Join(dir, entry.Name())
			if seen[fullPath] {
				continue
			}
			if !isGitRepo(fullPath) {
				continue
			}
			seen[fullPath] = true
			projects = append(projects, Project{
				Name:     entry.Name(),
				Path:     fullPath,
				Language: detectLanguage(fullPath),
			})
		}
	}

	sort.Slice(projects, func(i, j int) bool {
		return projects[i].Name < projects[j].Name
	})
	return projects, nil
}

func isGitRepo(path string) bool {
	info, err := os.Stat(filepath.Join(path, ".git"))
	if err != nil {
		return false
	}
	// .git can be a directory (normal repo) or a file (worktree)
	return info.IsDir() || info.Mode().IsRegular()
}

func detectLanguage(path string) string {
	markers := []struct {
		file string
		lang string
	}{
		{"Gemfile", "ruby"},
		{"go.mod", "go"},
		{"package.json", "node"},
		{"requirements.txt", "python"},
		{"pyproject.toml", "python"},
		{"Pipfile", "python"},
		{"Cargo.toml", "rust"},
		{"Podfile", "ios"},
		{"build.gradle", "java"},
		{"pom.xml", "java"},
	}
	for _, m := range markers {
		if _, err := os.Stat(filepath.Join(path, m.file)); err == nil {
			return m.lang
		}
	}
	return ""
}

// MergeWithManual merges auto-discovered projects with manually configured ones.
// Manual entries take precedence (matched by path).
func MergeWithManual(discovered, manual []Project) []Project {
	manualByPath := make(map[string]Project)
	for _, p := range manual {
		manualByPath[p.Path] = p
	}

	merged := make(map[string]Project)

	for _, p := range discovered {
		if mp, ok := manualByPath[p.Path]; ok {
			// Manual entry overrides, but fill in blanks from discovered
			if mp.Language == "" {
				mp.Language = p.Language
			}
			if mp.Name == "" {
				mp.Name = p.Name
			}
			merged[p.Path] = mp
		} else {
			merged[p.Path] = p
		}
	}

	// Add manual entries that weren't in discovered
	for _, p := range manual {
		if _, ok := merged[p.Path]; !ok {
			merged[p.Path] = p
		}
	}

	result := make([]Project, 0, len(merged))
	for _, p := range merged {
		result = append(result, p)
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i].Name < result[j].Name
	})
	return result
}

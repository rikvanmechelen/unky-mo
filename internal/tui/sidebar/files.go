package sidebar

import (
	"fmt"
	"sort"
	"strings"
)

// refreshChangedFiles reads the checkout's changes (the same list the web
// Files panel shows): each changed file with its status letter, and the
// lines added and removed against HEAD, untracked files included.
func (m *Model) refreshChangedFiles() {
	m.changedFiles, m.gitStatusMap = nil, nil
	m.changedAdded, m.changedRemoved = 0, 0
	if m.windowPath == "" {
		return
	}
	ch, err := m.gitReader().Changes(m.windowPath)
	if err != nil {
		return
	}
	m.gitStatusMap = make(map[string]string, len(ch.Files))
	for _, f := range ch.Files {
		m.gitStatusMap[f.Path] = f.Status
		m.changedFiles = append(m.changedFiles, f.Path)
	}
	sort.Strings(m.changedFiles) // so the tree renders deterministically
	m.changedAdded, m.changedRemoved = ch.Added, ch.Removed
}

func (m *Model) gitReader() GitReader {
	if m.git == nil {
		return defaultGitReader{}
	}
	return m.git
}

// refreshDirTree refreshes the full project directory tree on a 5s cadence
// (every 5th tick). It reads the checkout's tree (tracked + untracked, not ignored) and rebuilds it only when
// the output has changed, preserving expand/collapse state across rebuilds.
func (m *Model) refreshDirTree() {
	if m.windowPath == "" {
		m.dirTree = nil
		return
	}
	m.dirTreeRefreshTick++
	if m.dirTree != nil && m.dirTreeRefreshTick%5 != 0 {
		return // use cached tree
	}
	// Tracked and untracked-but-not-ignored files, from the repo root.
	files, err := m.gitReader().Tree(m.windowPath)
	if err != nil {
		return
	}
	raw := strings.Join(files, "\n")
	if raw == m.dirTreeRaw && m.dirTree != nil {
		return // unchanged
	}
	files = append([]string(nil), files...)
	sort.Strings(files)

	// Preserve expand state from old tree
	var oldState map[string]bool
	if m.dirTree != nil {
		oldState = preserveExpandedState(m.dirTree)
	}
	m.dirTree = buildDirTree(files)
	if oldState != nil {
		restoreExpandedState(m.dirTree, oldState, 0)
	} else {
		// First load: all directories collapsed
		restoreExpandedState(m.dirTree, map[string]bool{}, 0)
	}
	m.dirTreeRaw = raw
}

// fileTreeMode selects which file listing to show in the sidebar.
type fileTreeMode int

const (
	fileTreeModeChanged fileTreeMode = iota // current behavior: git status only
	fileTreeModeFull                        // full project tree (tracked + untracked files)
)

// dirNode represents a directory or file in the full project tree.
type dirNode struct {
	name     string
	path     string // relative path from repo root
	children map[string]*dirNode
	order    []string // insertion-order child names
	isDir    bool
	expanded bool
}

// buildDirTree creates a persistent directory tree from a list of file paths.
// All directories default to expanded=false (collapsed).
func buildDirTree(files []string) *dirNode {
	root := &dirNode{
		children: make(map[string]*dirNode),
		isDir:    true,
	}
	for _, file := range files {
		parts := strings.Split(file, "/")
		cur := root
		for i, part := range parts {
			if _, ok := cur.children[part]; !ok {
				isDir := i < len(parts)-1
				path := strings.Join(parts[:i+1], "/")
				child := &dirNode{
					name:     part,
					path:     path,
					children: make(map[string]*dirNode),
					isDir:    isDir,
				}
				cur.children[part] = child
				cur.order = append(cur.order, part)
			} else if i < len(parts)-1 {
				// Ensure intermediate nodes are marked as directories
				cur.children[part].isDir = true
			}
			cur = cur.children[part]
		}
	}
	return root
}

// preserveExpandedState snapshots the expanded state of all directories
// in a tree as a map of path → bool.
func preserveExpandedState(root *dirNode) map[string]bool {
	state := make(map[string]bool)
	var walk func(n *dirNode)
	walk = func(n *dirNode) {
		for _, name := range n.order {
			child := n.children[name]
			if child.isDir {
				state[child.path] = child.expanded
				walk(child)
			}
		}
	}
	walk(root)
	return state
}

// restoreExpandedState restores expanded state from a snapshot. Directories
// present in the snapshot get their saved state. New directories (not in
// snapshot) get expanded=true if their depth is <= defaultDepth.
func restoreExpandedState(root *dirNode, state map[string]bool, defaultDepth int) {
	var walk func(n *dirNode, depth int)
	walk = func(n *dirNode, depth int) {
		for _, name := range n.order {
			child := n.children[name]
			if child.isDir {
				if saved, ok := state[child.path]; ok {
					child.expanded = saved
				} else if depth <= defaultDepth {
					child.expanded = true
				}
				walk(child, depth+1)
			}
		}
	}
	walk(root, 1)
}

// flattenDirTree walks a dirNode tree respecting expanded flags and produces
// renderable fileTreeLine entries. Collapsed directories appear as a single
// line; their children are hidden. A running file index is assigned to visible
// file nodes (dirs get -1).
func flattenDirTree(root *dirNode) []fileTreeLine {
	var lines []fileTreeLine
	fileIdx := 0
	var walk func(n *dirNode, indent string)
	walk = func(n *dirNode, indent string) {
		for _, name := range n.order {
			child := n.children[name]
			if child.isDir {
				lines = append(lines, fileTreeLine{
					display:   child.name,
					indent:    indent,
					fileIndex: -1,
					isDir:     true,
					path:      child.path,
				})
				if child.expanded {
					walk(child, indent+"  ")
				}
			} else {
				lines = append(lines, fileTreeLine{
					display:   child.name,
					indent:    indent,
					fileIndex: fileIdx,
					isDir:     false,
					path:      child.path,
				})
				fileIdx++
			}
		}
	}
	walk(root, " ")
	return lines
}

// renderGitStatusMarker returns a styled single-char git status indicator.
func renderGitStatusMarker(status string) string {
	switch status {
	case "M":
		return gitStatusM.Render("M")
	case "A":
		return gitStatusA.Render("A")
	case "D":
		return gitStatusD.Render("D")
	case "?":
		return gitStatusU.Render("?")
	case "R":
		return gitStatusR.Render("R")
	case "●":
		return gitDirDot.Render("●")
	default:
		return status
	}
}

// ensureFileCursorVisible adjusts fileViewStart so that fileCursor is within
// the visible window of maxLines height.
func (m *Model) ensureFileCursorVisible(maxLines int) {
	if maxLines <= 0 {
		return
	}
	if m.fileCursor < m.fileViewStart {
		m.fileViewStart = m.fileCursor
	}
	if m.fileCursor >= m.fileViewStart+maxLines {
		m.fileViewStart = m.fileCursor - maxLines + 1
	}
}

// visibleFileLines returns the flattened+annotated lines for the full tree mode.
// Used by both View() and Update() to stay in sync.
func (m *Model) visibleFileLines() []fileTreeLine {
	if m.dirTree == nil {
		return nil
	}
	lines := flattenDirTree(m.dirTree)
	return annotateTreeWithStatus(lines, m.gitStatusMap)
}

// fileLineCount returns the number of navigable lines in the current file mode.
func (m *Model) fileLineCount() int {
	switch m.fileTreeMode {
	case fileTreeModeFull:
		return len(m.visibleFileLines())
	default:
		return len(m.changedFiles)
	}
}

// resolveFilePath returns the file path at the current fileCursor position.
// In changed-files mode this indexes into changedFiles. In full-tree mode
// this indexes into the flattened lines.
func (m *Model) resolveFilePath() (path string, isDir bool, hasGitStatus bool) {
	switch m.fileTreeMode {
	case fileTreeModeFull:
		lines := m.visibleFileLines()
		if m.fileCursor < 0 || m.fileCursor >= len(lines) {
			return "", false, false
		}
		tl := lines[m.fileCursor]
		return tl.path, tl.isDir, tl.gitStatus != ""
	default:
		if m.fileCursor < 0 || m.fileCursor >= len(m.changedFiles) {
			return "", false, false
		}
		return m.changedFiles[m.fileCursor], false, true
	}
}

// findDirNode locates a dirNode by its full path within a subtree.
func findDirNode(root *dirNode, path string) *dirNode {
	if root.path == path {
		return root
	}
	for _, name := range root.order {
		child := root.children[name]
		if child.isDir {
			if child.path == path {
				return child
			}
			if strings.HasPrefix(path, child.path+"/") {
				if found := findDirNode(child, path); found != nil {
					return found
				}
			}
		}
	}
	return nil
}

// findDirNodeFromRoot locates a dirNode by path starting from the tree root.
func findDirNodeFromRoot(root *dirNode, path string) *dirNode {
	for _, name := range root.order {
		child := root.children[name]
		if child.path == path {
			return child
		}
		if child.isDir && strings.HasPrefix(path, child.path+"/") {
			if found := findDirNode(child, path); found != nil {
				return found
			}
		}
	}
	return nil
}

// annotateTreeWithStatus overlays git status markers onto flattened tree
// lines. File lines get their exact status (M/A/D/?/R). Directory lines get
// a "●" marker if any file in the statusMap has the directory's path as a prefix.
func annotateTreeWithStatus(lines []fileTreeLine, statusMap map[string]string) []fileTreeLine {
	if lines == nil {
		return nil
	}
	result := make([]fileTreeLine, len(lines))
	copy(result, lines)
	for i := range result {
		if result[i].isDir {
			// Check if any changed file is under this directory
			prefix := result[i].path + "/"
			for path := range statusMap {
				if strings.HasPrefix(path, prefix) {
					result[i].gitStatus = "●"
					break
				}
			}
		} else if s, ok := statusMap[result[i].path]; ok {
			result[i].gitStatus = s
		}
	}
	return result
}

// fileTreeLine is a single rendered line of the file tree.
type fileTreeLine struct {
	display   string // the text to show (filename or directory name)
	indent    string // leading spaces
	fileIndex int    // index into changedFiles (-1 for directory nodes)
	isDir     bool   // true for directory nodes (expand/collapse target)
	path      string // full relative path from repo root
	gitStatus string // "M", "A", "D", "?", "R", or "" for clean files
}

// buildFileTreeLines creates an indented tree from file paths.
// Directory nodes that have a single child directory are collapsed.
func buildFileTreeLines(files []string) []fileTreeLine {
	type treeNode struct {
		name     string
		children map[string]*treeNode
		order    []string
		fileIdx  int // index into files, -1 for dirs
	}

	root := &treeNode{children: make(map[string]*treeNode), fileIdx: -1}

	for i, file := range files {
		parts := strings.Split(file, "/")
		cur := root
		for j, part := range parts {
			if _, ok := cur.children[part]; !ok {
				child := &treeNode{
					name:     part,
					children: make(map[string]*treeNode),
					fileIdx:  -1,
				}
				if j == len(parts)-1 {
					child.fileIdx = i
				}
				cur.children[part] = child
				cur.order = append(cur.order, part)
			}
			cur = cur.children[part]
		}
	}

	// Collapse single-child directories
	var collapse func(n *treeNode) *treeNode
	collapse = func(n *treeNode) *treeNode {
		for _, name := range n.order {
			n.children[name] = collapse(n.children[name])
		}
		if n.fileIdx < 0 && len(n.children) == 1 {
			childName := n.order[0]
			child := n.children[childName]
			if child.fileIdx < 0 {
				merged := &treeNode{
					name:     n.name + "/" + child.name,
					children: child.children,
					order:    child.order,
					fileIdx:  -1,
				}
				return merged
			}
		}
		return n
	}
	for _, name := range root.order {
		root.children[name] = collapse(root.children[name])
	}

	// Flatten to lines
	var lines []fileTreeLine
	var walk func(n *treeNode, indent string)
	walk = func(n *treeNode, indent string) {
		for _, name := range n.order {
			child := n.children[name]
			if child.fileIdx >= 0 {
				lines = append(lines, fileTreeLine{
					display:   child.name,
					indent:    indent,
					fileIndex: child.fileIdx,
				})
			} else {
				lines = append(lines, fileTreeLine{
					display:   child.name,
					indent:    indent,
					fileIndex: -1,
				})
				walk(child, indent+"  ")
			}
		}
	}
	walk(root, " ")

	return lines
}

// renderFileTree renders changed files as a compact tree.
func renderFileTree(files []string, maxWidth, maxLines int) string {
	if len(files) == 0 {
		return ""
	}

	// Build tree structure
	type node struct {
		name     string
		children map[string]*node
		isFile   bool
		order    []string // preserve insertion order
	}

	root := &node{children: make(map[string]*node)}

	for _, file := range files {
		parts := strings.Split(file, "/")
		current := root
		for i, part := range parts {
			if _, ok := current.children[part]; !ok {
				child := &node{name: part, children: make(map[string]*node)}
				if i == len(parts)-1 {
					child.isFile = true
				}
				current.children[part] = child
				current.order = append(current.order, part)
			}
			current = current.children[part]
		}
	}

	// Collapse single-child directories (e.g. "internal/tui/" → "internal/tui/")
	var collapse func(n *node) *node
	collapse = func(n *node) *node {
		for _, name := range n.order {
			child := n.children[name]
			n.children[name] = collapse(child)
		}
		if !n.isFile && len(n.children) == 1 {
			childName := n.order[0]
			child := n.children[childName]
			if !child.isFile {
				merged := &node{
					name:     n.name + "/" + child.name,
					children: child.children,
					order:    child.order,
					isFile:   child.isFile,
				}
				return merged
			}
		}
		return n
	}

	for _, name := range root.order {
		root.children[name] = collapse(root.children[name])
	}

	// Render tree
	var lines []string
	var render func(n *node, indent string)
	render = func(n *node, indent string) {
		if len(lines) >= maxLines {
			return
		}
		for _, name := range n.order {
			child := n.children[name]
			if len(lines) >= maxLines {
				return
			}
			if child.isFile {
				lines = append(lines, indent+child.name)
			} else {
				lines = append(lines, indent+child.name+"/")
				render(child, indent+"  ")
			}
		}
	}
	render(root, " ")

	if len(files) > len(lines) {
		lines = append(lines, fmt.Sprintf(" +%d more", len(files)-len(lines)))
	}

	return strings.Join(lines, "\n")
}

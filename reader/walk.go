package reader

import (
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

// alwaysIgnored is skipped unless named as a root: a dependency tree holds
// thousands of folders and no work of a person's own.
var alwaysIgnored = []string{"node_modules"}

// finder walks folders for git checkouts.
type finder struct {
	ignore []string
}

func newFinder(ignore []string) finder {
	var patterns []string
	for _, p := range append(append([]string{}, alwaysIgnored...), ignore...) {
		p = strings.Trim(strings.TrimPrefix(filepath.ToSlash(p), "**/"), "/")
		p = strings.TrimSuffix(p, "/**")
		if p != "" {
			patterns = append(patterns, p)
		}
	}
	return finder{ignore: patterns}
}

// ignored says whether a folder, by its path from the root, matches an
// ignore glob: the glob matches the folder's name, or the end of its path,
// at any depth.
func (f finder) ignored(rel string) bool {
	parts := strings.Split(rel, "/")
	for i := range parts {
		tail := strings.Join(parts[i:], "/")
		for _, p := range f.ignore {
			if ok, _ := path.Match(p, tail); ok {
				return true
			}
		}
	}
	return false
}

// find answers every checkout under root, sorted. A folder holding `.git`,
// a folder or a file (a linked worktree carries a file), is a checkout, and
// nothing inside it is walked. Hidden folders are walked, since worktrees
// live in them; links are not, so a link back up the tree cannot loop.
func (f finder) find(root string) []string {
	var found []string
	pending := []string{root}
	for len(pending) > 0 {
		dir := pending[len(pending)-1]
		pending = pending[:len(pending)-1]
		if dir != root {
			rel, err := filepath.Rel(root, dir)
			if err == nil && f.ignored(filepath.ToSlash(rel)) {
				continue
			}
		}
		if _, err := os.Lstat(filepath.Join(dir, ".git")); err == nil {
			found = append(found, dir)
			continue
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			// Unreadable, or removed while walking: nothing here to report.
			continue
		}
		for _, e := range entries {
			if e.Type()&fs.ModeSymlink != 0 || !e.IsDir() {
				continue
			}
			pending = append(pending, filepath.Join(dir, e.Name()))
		}
	}
	sort.Strings(found)
	return found
}

// expand resolves a root that starts with ~ to the person's home folder.
func expand(root string) string {
	if root != "~" && !strings.HasPrefix(root, "~/") && !strings.HasPrefix(root, `~\`) {
		return root
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return root
	}
	return filepath.Join(home, root[1:])
}

package reader

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
)

// worktree is one entry of `git worktree list --porcelain`.
type worktree struct {
	path     string
	bare     bool
	prunable bool
}

func parseWorktrees(porcelain string) []worktree {
	var out []worktree
	for _, l := range lines(porcelain) {
		switch {
		case strings.HasPrefix(l, "worktree "):
			out = append(out, worktree{path: l[len("worktree "):]})
		case len(out) == 0:
		case l == "bare":
			out[len(out)-1].bare = true
		case strings.HasPrefix(l, "prunable"):
			out[len(out)-1].prunable = true
		}
	}
	return out
}

// changes are a worktree's files by state, from `git status --porcelain -z`.
type changes struct{ staged, modified, untracked, conflicted int }

func parseStatus(z string) changes {
	var c changes
	fields := strings.Split(z, "\x00")
	for i := 0; i < len(fields); i++ {
		f := fields[i]
		if len(f) < 3 {
			continue
		}
		x, y := f[0], f[1]
		// A rename or a copy names the file it came from in the next field.
		if x == 'R' || x == 'C' {
			i++
		}
		switch {
		case x == '?' && y == '?':
			c.untracked++
		case x == '!' && y == '!':
		case x == 'U' || y == 'U' || (x == 'A' && y == 'A') || (x == 'D' && y == 'D'):
			c.conflicted++
		default:
			if x != ' ' {
				c.staged++
			}
			if y != ' ' {
				c.modified++
			}
		}
	}
	return c
}

// branchRef is one `for-each-ref` line: name, upstream ref, upstream short, track.
type branchRef struct {
	Branch
	upstreamRef string
}

func parseBranch(line string) branchRef {
	parts := strings.Split(line, "\t")
	for len(parts) < 4 {
		parts = append(parts, "")
	}
	b := branchRef{Branch: Branch{Name: parts[0], Upstream: parts[2], UpstreamGone: parts[3] == "gone"}, upstreamRef: parts[1]}
	for _, piece := range strings.Split(parts[3], ",") {
		piece = strings.TrimSpace(piece)
		if n, ok := strings.CutPrefix(piece, "ahead "); ok {
			b.Ahead, _ = strconv.Atoi(n)
		} else if n, ok := strings.CutPrefix(piece, "behind "); ok {
			b.Behind, _ = strconv.Atoi(n)
		}
	}
	return b
}

// inspect reads one repository from any of its checkouts.
func inspect(ctx context.Context, checkout string) (Checkout, error) {
	listed, err := git(ctx, checkout, "worktree", "list", "--porcelain")
	if err != nil {
		return Checkout{}, err
	}
	trees := parseWorktrees(listed)
	if len(trees) == 0 {
		return Checkout{}, fmt.Errorf("git listed no worktrees")
	}
	main := trees[0]
	out := Checkout{Folder: strings.TrimSuffix(filepath.Base(main.path), ".git"), Branches: []Branch{}}
	// The repository's own commands run where git can always reach it: the
	// main worktree, or the checkout found when the main one is bare or gone.
	repo := main.path
	if main.bare {
		repo = checkout
	}
	for _, t := range trees {
		// A bare repository has no files to change, and a prunable worktree's folder is gone.
		if t.bare || t.prunable {
			continue
		}
		if info, err := os.Stat(t.path); err != nil || !info.IsDir() {
			continue
		}
		status, err := git(ctx, t.path, "status", "--porcelain=v1", "-z", "--untracked-files=normal")
		if err != nil {
			return Checkout{}, err
		}
		c := parseStatus(status)
		out.Worktrees++
		out.Staged += c.staged
		out.Modified += c.modified
		out.Untracked += c.untracked
		out.Conflicted += c.conflicted
	}
	if out.Worktrees == 0 && !main.bare {
		return Checkout{}, fmt.Errorf("no worktree of it is on disk")
	}
	out.Remote = remote(ctx, repo)
	refs, err := git(ctx, repo, "for-each-ref", "--format=%(refname:lstrip=2)%09%(upstream)%09%(upstream:short)%09%(upstream:track,nobracket)", "refs/heads")
	if err != nil {
		return Checkout{}, err
	}
	for _, l := range lines(refs) {
		b := parseBranch(l)
		b.Unpushed, err = unpushed(ctx, repo, b)
		if err != nil {
			return Checkout{}, err
		}
		out.Branches = append(out.Branches, b.Branch)
	}
	if stashes, err := git(ctx, repo, "stash", "list"); err == nil {
		out.Stashes = len(lines(stashes))
	}
	return out, nil
}

// unpushed counts the commits on a branch that no remote-tracking ref holds.
// A branch level with a remote upstream that still exists has none, so git is
// only asked about the others.
func unpushed(ctx context.Context, repo string, b branchRef) (int, error) {
	if strings.HasPrefix(b.upstreamRef, "refs/remotes/") && !b.UpstreamGone && b.Ahead == 0 {
		return 0, nil
	}
	n, err := git(ctx, repo, "rev-list", "--count", "refs/heads/"+b.Name, "--not", "--remotes")
	if err != nil {
		return 0, err
	}
	return strconv.Atoi(strings.TrimSpace(n))
}

// remote is the repository's origin, else its first remote, normalized.
func remote(ctx context.Context, repo string) string {
	names, err := git(ctx, repo, "remote")
	if err != nil {
		return ""
	}
	list := lines(names)
	if len(list) == 0 {
		return ""
	}
	name := list[0]
	if slices.Contains(list, "origin") {
		name = "origin"
	}
	u, err := git(ctx, repo, "remote", "get-url", name)
	if err != nil {
		return ""
	}
	return normalizeRemote(u)
}

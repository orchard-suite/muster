package reader

import (
	"context"
	"errors"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"
)

// Config says where to look. A root may start with ~. Ignore holds globs
// matched against a folder's name or the end of its path under a root, at any
// depth; node_modules is always ignored below a root.
type Config struct {
	Roots  []string
	Ignore []string
}

// ErrNoGit is returned when git is not on the path.
var ErrNoGit = errors.New("reader: git is not on the path")

// Read walks the roots and reports every repository found, once each: a
// linked worktree found on its own is counted under the repository it
// belongs to. A root that is not there is skipped, and so is a repository git
// cannot read. It never fetches and takes no optional git lock.
func Read(ctx context.Context, cfg Config) (Report, error) {
	if _, err := exec.LookPath("git"); err != nil {
		return Report{}, ErrNoGit
	}
	scanned := time.Now().UTC().Truncate(time.Second)
	f := newFinder(cfg.Ignore)
	seen := map[string]bool{}
	var checkouts []string
	for _, root := range cfg.Roots {
		abs, err := filepath.Abs(expand(root))
		if err != nil {
			continue
		}
		for _, c := range f.find(abs) {
			if !seen[c] {
				seen[c] = true
				checkouts = append(checkouts, c)
			}
		}
	}

	// Group by the folder every worktree of a repository shares, so each is read once.
	var (
		mu      sync.Mutex
		claimed = map[string]bool{}
		out     []Checkout
		wg      sync.WaitGroup
		work    = make(chan string)
	)
	workers := min(runtime.NumCPU(), max(len(checkouts), 1))
	for range workers {
		wg.Go(func() {
			for c := range work {
				common, err := git(ctx, c, "rev-parse", "--path-format=absolute", "--git-common-dir")
				if err != nil {
					continue
				}
				common = filepath.Clean(strings.TrimSpace(common))
				mu.Lock()
				first := !claimed[common]
				claimed[common] = true
				mu.Unlock()
				if !first {
					continue
				}
				r, err := inspect(ctx, c)
				if err != nil {
					continue
				}
				mu.Lock()
				out = append(out, r)
				mu.Unlock()
			}
		})
	}
	for _, c := range checkouts {
		select {
		case work <- c:
		case <-ctx.Done():
		}
		if ctx.Err() != nil {
			break
		}
	}
	close(work)
	wg.Wait()
	if err := ctx.Err(); err != nil {
		return Report{}, err
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Remote != out[j].Remote {
			return out[i].Remote < out[j].Remote
		}
		return out[i].Folder < out[j].Folder
	})
	if out == nil {
		out = []Checkout{}
	}
	return Report{ScannedAt: scanned, Checkouts: out}, nil
}

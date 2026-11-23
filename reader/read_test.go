package reader

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// sandbox is a folder for one test's repositories, with a git identity of
// its own so a commit needs nothing from the machine's settings.
func sandbox(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not on the path")
	}
	root := t.TempDir()
	// macOS hands out /var, a link to /private/var; git answers the real path.
	if real, err := filepath.EvalSymlinks(root); err == nil {
		root = real
	}
	for k, v := range map[string]string{
		"GIT_AUTHOR_NAME": "A User", "GIT_AUTHOR_EMAIL": "user@example.com",
		"GIT_COMMITTER_NAME": "A User", "GIT_COMMITTER_EMAIL": "user@example.com",
	} {
		t.Setenv(k, v)
	}
	return root
}

func run(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-c", "commit.gpgsign=false", "-c", "init.defaultBranch=main"}, args...)...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s in %s: %v\n%s", strings.Join(args, " "), dir, err, out)
	}
	return strings.TrimSpace(string(out))
}

func write(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// repo makes a repository at dir with one commit on main.
func repo(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	run(t, dir, "init", "-q", "-b", "main")
	write(t, filepath.Join(dir, "readme.txt"), "one\n")
	run(t, dir, "add", ".")
	run(t, dir, "commit", "-q", "-m", "first")
}

func commit(t *testing.T, dir, file, body string) {
	t.Helper()
	write(t, filepath.Join(dir, file), body)
	run(t, dir, "add", ".")
	run(t, dir, "commit", "-q", "-m", "change "+file)
}

// track gives a repository an origin on a forge and a remote-tracking main
// at its HEAD, as a fetch would have left it, without any network.
func track(t *testing.T, dir, url string) {
	t.Helper()
	run(t, dir, "remote", "add", "origin", url)
	run(t, dir, "update-ref", "refs/remotes/origin/main", "HEAD")
	run(t, dir, "branch", "-q", "--set-upstream-to=origin/main", "main")
}

func read(t *testing.T, cfg Config) Report {
	t.Helper()
	r, err := Read(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func only(t *testing.T, r Report) Checkout {
	t.Helper()
	if len(r.Checkouts) != 1 {
		t.Fatalf("want one checkout, got %+v", r.Checkouts)
	}
	return r.Checkouts[0]
}

func branch(t *testing.T, c Checkout, name string) Branch {
	t.Helper()
	for _, b := range c.Branches {
		if b.Name == name {
			return b
		}
	}
	t.Fatalf("no branch %s in %+v", name, c.Branches)
	return Branch{}
}

func asJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestACleanTrackedCheckoutHasNothingWaiting(t *testing.T) {
	root := sandbox(t)
	dir := filepath.Join(root, "widgets")
	repo(t, dir)
	track(t, dir, "git@github.com:Acme/Widgets.git")

	got := asJSON(t, only(t, read(t, Config{Roots: []string{root}})))
	want := `{"remote":"github.com/acme/widgets","folder":"widgets","worktrees":1,"staged":0,"modified":0,"untracked":0,"conflicted":0,"stashes":0,` +
		`"branches":[{"name":"main","ahead":0,"behind":0,"unpushed":0,"upstream":"origin/main","upstream_gone":false}]}`
	if got != want {
		t.Fatalf("got  %s\nwant %s", got, want)
	}
}

func TestFilesAreCountedByStateAndNeverNamed(t *testing.T) {
	root := sandbox(t)
	dir := filepath.Join(root, "widgets")
	repo(t, dir)
	track(t, dir, "https://github.com/acme/widgets.git")
	write(t, filepath.Join(dir, "staged-plan.txt"), "new\n")
	run(t, dir, "add", "staged-plan.txt")
	write(t, filepath.Join(dir, "readme.txt"), "two\n")
	write(t, filepath.Join(dir, "untracked-salary.txt"), "x\n")
	write(t, filepath.Join(dir, "also-untracked.txt"), "x\n")

	r := read(t, Config{Roots: []string{root}})
	c := only(t, r)
	if c.Staged != 1 || c.Modified != 1 || c.Untracked != 2 || c.Conflicted != 0 {
		t.Fatalf("counts %+v", c)
	}
	body := asJSON(t, r)
	for _, leak := range []string{"plan", "salary", "readme", root, "first"} {
		if strings.Contains(body, leak) {
			t.Fatalf("the report holds %q: %s", leak, body)
		}
	}
}

func TestAConflictIsCountedAsConflicted(t *testing.T) {
	root := sandbox(t)
	dir := filepath.Join(root, "widgets")
	repo(t, dir)
	run(t, dir, "checkout", "-q", "-b", "other")
	commit(t, dir, "readme.txt", "theirs\n")
	run(t, dir, "checkout", "-q", "main")
	commit(t, dir, "readme.txt", "ours\n")
	cmd := exec.Command("git", "merge", "-q", "other")
	cmd.Dir = dir
	if err := cmd.Run(); err == nil {
		t.Fatal("the merge was meant to conflict")
	}

	c := only(t, read(t, Config{Roots: []string{root}}))
	if c.Conflicted != 1 || c.Staged != 0 || c.Modified != 0 {
		t.Fatalf("counts %+v", c)
	}
}

func TestStashesAreCounted(t *testing.T) {
	root := sandbox(t)
	dir := filepath.Join(root, "widgets")
	repo(t, dir)
	write(t, filepath.Join(dir, "readme.txt"), "two\n")
	run(t, dir, "stash", "-q")
	write(t, filepath.Join(dir, "readme.txt"), "three\n")
	run(t, dir, "stash", "-q")

	if c := only(t, read(t, Config{Roots: []string{root}})); c.Stashes != 2 || c.Modified != 0 {
		t.Fatalf("got %+v", c)
	}
}

func TestBranchesStandAgainstTheirUpstreams(t *testing.T) {
	root := sandbox(t)
	dir := filepath.Join(root, "widgets")
	repo(t, dir)
	track(t, dir, "ssh://git@github.com:22/acme/widgets.git")

	// main: one commit the remote has not seen, and the remote one ahead.
	base := run(t, dir, "rev-parse", "HEAD")
	commit(t, dir, "a.txt", "a\n")
	run(t, dir, "checkout", "-q", "--detach", base)
	commit(t, dir, "b.txt", "b\n")
	run(t, dir, "update-ref", "refs/remotes/origin/main", "HEAD")
	run(t, dir, "checkout", "-q", "main")

	// spike: no upstream, two commits of its own past main's one unpushed.
	run(t, dir, "checkout", "-q", "-b", "spike")
	commit(t, dir, "c.txt", "c\n")
	commit(t, dir, "d.txt", "d\n")

	// done: its upstream was deleted on the remote.
	run(t, dir, "checkout", "-q", "-b", "done", "main")
	run(t, dir, "config", "branch.done.remote", "origin")
	run(t, dir, "config", "branch.done.merge", "refs/heads/done")
	run(t, dir, "checkout", "-q", "main")

	c := only(t, read(t, Config{Roots: []string{root}}))
	if got := branch(t, c, "main"); got != (Branch{Name: "main", Ahead: 1, Behind: 1, Unpushed: 1, Upstream: "origin/main"}) {
		t.Fatalf("main %+v", got)
	}
	if got := branch(t, c, "spike"); got != (Branch{Name: "spike", Unpushed: 3}) {
		t.Fatalf("spike %+v", got)
	}
	if got := branch(t, c, "done"); got != (Branch{Name: "done", Unpushed: 1, Upstream: "origin/done", UpstreamGone: true}) {
		t.Fatalf("done %+v", got)
	}
}

func TestALinkedWorktreeIsCountedUnderItsCheckout(t *testing.T) {
	root := sandbox(t)
	dir := filepath.Join(root, "widgets")
	repo(t, dir)
	track(t, dir, "git@github.com:acme/widgets.git")
	write(t, filepath.Join(dir, ".git", "info", "exclude"), ".worktrees/\n")
	// One worktree inside the checkout, one beside it under the same root.
	run(t, dir, "worktree", "add", "-q", "-b", "inside", filepath.Join(dir, ".worktrees", "inside"))
	run(t, dir, "worktree", "add", "-q", "-b", "beside", filepath.Join(root, "widgets-beside"))
	write(t, filepath.Join(dir, ".worktrees", "inside", "x.txt"), "x\n")
	write(t, filepath.Join(root, "widgets-beside", "readme.txt"), "changed\n")
	write(t, filepath.Join(dir, "readme.txt"), "changed\n")

	c := only(t, read(t, Config{Roots: []string{root}}))
	if c.Folder != "widgets" || c.Remote != "github.com/acme/widgets" || c.Worktrees != 3 || c.Modified != 2 || c.Untracked != 1 {
		t.Fatalf("got %+v", c)
	}
	if len(c.Branches) != 3 {
		t.Fatalf("branches %+v", c.Branches)
	}
}

func TestARepositoryWithNoRemoteIsNamedByItsFolder(t *testing.T) {
	root := sandbox(t)
	dir := filepath.Join(root, "notes")
	repo(t, dir)
	commit(t, dir, "b.txt", "b\n")

	c := only(t, read(t, Config{Roots: []string{root}}))
	if c.Remote != "" || c.Folder != "notes" {
		t.Fatalf("got %+v", c)
	}
	if b := branch(t, c, "main"); b.Unpushed != 2 || b.Upstream != "" {
		t.Fatalf("main %+v", b)
	}
}

func TestIgnoredFoldersAreNotWalked(t *testing.T) {
	root := sandbox(t)
	repo(t, filepath.Join(root, "widgets"))
	repo(t, filepath.Join(root, "web", "node_modules", "left-pad"))
	repo(t, filepath.Join(root, "vendor", "thing"))
	repo(t, filepath.Join(root, "archive", "2020", "old"))

	r := read(t, Config{Roots: []string{root}, Ignore: []string{"vendor", "**/archive/2020"}})
	if c := only(t, r); c.Folder != "widgets" {
		t.Fatalf("got %+v", r.Checkouts)
	}
	// A root named outright is read, even one a glob would skip.
	r = read(t, Config{Roots: []string{filepath.Join(root, "web", "node_modules")}})
	if c := only(t, r); c.Folder != "left-pad" {
		t.Fatalf("got %+v", r.Checkouts)
	}
}

func TestARepositoryInTwoRootsIsReadOnce(t *testing.T) {
	root := sandbox(t)
	repo(t, filepath.Join(root, "a", "widgets"))
	only(t, read(t, Config{Roots: []string{root, filepath.Join(root, "a"), filepath.Join(root, "missing")}}))
}

func TestAnEmptyMachineReportsNoCheckouts(t *testing.T) {
	root := sandbox(t)
	r := read(t, Config{Roots: []string{root}})
	if len(r.Checkouts) != 0 || r.ScannedAt.IsZero() {
		t.Fatalf("got %+v", r)
	}
	if body := asJSON(t, r); !strings.Contains(body, `"checkouts":[]`) {
		t.Fatalf("checkouts must be a list: %s", body)
	}
}

func TestReadTakesNoOptionalLocks(t *testing.T) {
	env := strings.Join(gitEnv(), "\n")
	for _, want := range []string{"GIT_OPTIONAL_LOCKS=0", "GIT_TERMINAL_PROMPT=0"} {
		if !strings.Contains(env, want) {
			t.Fatalf("git runs without %s", want)
		}
	}
	// A file touched with the same bytes makes a plain `git status` refresh
	// the index. A read leaves it as it was.
	root := sandbox(t)
	dir := filepath.Join(root, "widgets")
	repo(t, dir)
	index := filepath.Join(dir, ".git", "index")
	before, err := os.Stat(index)
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(dir, "readme.txt")
	write(t, file, "one\n")
	later := before.ModTime().Add(2 * time.Second)
	if err := os.Chtimes(file, later, later); err != nil {
		t.Fatal(err)
	}
	read(t, Config{Roots: []string{root}})
	after, err := os.Stat(index)
	if err != nil {
		t.Fatal(err)
	}
	if !after.ModTime().Equal(before.ModTime()) {
		t.Fatal("the read rewrote the index")
	}
}

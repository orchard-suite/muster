package reader

import "testing"

func TestRemotesAreNamedByHostAndPath(t *testing.T) {
	for raw, want := range map[string]string{
		"git@github.com:acme/widgets.git":                               "github.com/acme/widgets",
		"https://github.com/Acme/Widgets":                               "github.com/acme/widgets",
		"https://user:token@github.com:443/acme/widgets.git":            "github.com/acme/widgets",
		"ssh://git@gitlab.example.com:2222/group/sub/project.git":       "gitlab.example.com/group/sub/project",
		"git://example.com/acme/widgets":                                "example.com/acme/widgets",
		"https://acme@dev.azure.com/acme/Shop/_git/Web%20App":           "dev.azure.com/acme/shop/_git/web app",
		"git@ssh.dev.azure.com:v3/acme/Shop/web":                        "dev.azure.com/acme/shop/_git/web",
		"acme@vs-ssh.visualstudio.com:v3/acme/Shop/web":                 "dev.azure.com/acme/shop/_git/web",
		"https://acme.visualstudio.com/DefaultCollection/Shop/_git/web": "dev.azure.com/acme/shop/_git/web",
		"/srv/git/widgets.git":                                          "",
		"../widgets":                                                    "",
		"file:///srv/git/widgets.git":                                   "",
		`C:\work\widgets`:                                               "",
		"":                                                              "",
	} {
		if got := normalizeRemote(raw); got != want {
			t.Errorf("%q: got %q, want %q", raw, got, want)
		}
	}
}

func TestStatusIsCountedByItsTwoLetters(t *testing.T) {
	z := "M  staged\x00 M modified\x00MM both\x00?? new\x00!! ignored\x00UU conflict\x00AA added\x00R  to\x00from\x00"
	want := changes{staged: 3, modified: 2, untracked: 1, conflicted: 2}
	if got := parseStatus(z); got != want {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

func TestBranchLinesAreParsed(t *testing.T) {
	b := parseBranch("feature/x\trefs/remotes/origin/feature/x\torigin/feature/x\tahead 2, behind 1")
	if b.Name != "feature/x" || b.Upstream != "origin/feature/x" || b.Ahead != 2 || b.Behind != 1 || b.UpstreamGone {
		t.Fatalf("got %+v", b)
	}
	if g := parseBranch("old\trefs/remotes/origin/old\torigin/old\tgone"); !g.UpstreamGone {
		t.Fatalf("got %+v", g)
	}
}

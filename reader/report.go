package reader

import "time"

// Report is what Read found, report version 1.
type Report struct {
	ScannedAt time.Time  `json:"scanned_at"`
	Checkouts []Checkout `json:"checkouts"`
}

// Checkout is one repository on the machine. Its counts sum over every
// worktree of it.
type Checkout struct {
	// Remote is the fetch URL of origin, else the first remote, as
	// host/owner/name in lower case. Empty when the repository has no remote
	// on a host; Folder then is its only name.
	Remote string `json:"remote"`
	// Folder is the name of the main worktree's folder, and nothing above it.
	Folder     string   `json:"folder"`
	Worktrees  int      `json:"worktrees"`
	Staged     int      `json:"staged"`
	Modified   int      `json:"modified"`
	Untracked  int      `json:"untracked"`
	Conflicted int      `json:"conflicted"`
	Stashes    int      `json:"stashes"`
	Branches   []Branch `json:"branches"`
}

// Branch is one local branch and how it stands against its upstream.
type Branch struct {
	Name   string `json:"name"`
	Ahead  int    `json:"ahead"`
	Behind int    `json:"behind"`
	// Unpushed counts the commits on the branch that no remote-tracking ref holds.
	Unpushed int `json:"unpushed"`
	// Upstream is the tracked branch, such as origin/main; empty when none is set.
	Upstream string `json:"upstream"`
	// UpstreamGone is true when an upstream is set and no longer exists.
	UpstreamGone bool `json:"upstream_gone"`
}

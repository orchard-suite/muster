// Package reader is Muster's reader: it walks a machine's folders for git
// checkouts and reports, for each one, the work that has not left the
// machine. Tender carries it and posts what it reads to Muster.
//
// It only reads. It never fetches, takes no optional git locks, and needs
// nothing beyond the standard library and git on the path. A report holds
// names and counts: a checkout's remote or folder name, its file counts, its
// stashes and worktrees, and its branches. Never a file's name, a path above
// the checkout's folder, a commit message, or a file's contents.
package reader

// Name is the reader's name, in Tender's settings and in the address a
// member serves it under.
const Name = "muster"

// Version is the report version Read writes.
const Version = 1

package catalog

import (
	"sort"
	"strings"
	"time"

	"github.com/orchard-suite/muster/internal/machines"
	"github.com/orchard-suite/orrery/v2/sdk"
)

// Checkout is the blueprint of one repository on one machine.
const Checkout = "sw_checkout"

// The relations a checkout takes besides its repository, each offered on its
// own so a hub without the blueprint it points at refuses only that link.
const (
	CheckoutMachine = "machine"
	CheckoutOwner   = "owner"
)

var (
	fromCheckouts      = "<-" + Checkout + ":repository"
	checkoutRepository = map[string]any{"target": Repository, "many": false, "title": "Repository", "inverseTitle": "Checkouts"}
)

// CheckoutBlueprint is one repository on one machine, from the machine's own
// report: names and counts only. An estate decides who reads it; until it
// says, Muster writes none (see the README).
func CheckoutBlueprint() sdk.Blueprint {
	n := func(name, title, help string) Field {
		return f(name, typed("number", "title", title, "description", help))
	}
	return sdk.Blueprint{
		"id": Checkout, "title": "Checkout", "group": "Catalog", "icon": "branch",
		"description": "One repository on one machine, keyed <device>/<remote> (or <device>/<folder> with no remote), as the machine's last report says: counts of files not committed, commits not pushed, stashes, worktrees, and each branch against its upstream. Never a file's name, a commit message, or a path above the folder.",
		"status":      map[string]any{"field": "status"},
		"fields": Fields{
			f("device", typed("string", "chip", true, "description", "The device's id, as its certificate names it.")),
			f("device_name", typed("string", "title", "Machine name", "description", "The certificate's DNS name, else its common name.")),
			f("person", typed("user", "description", "The person the device's certificate names.")),
			f("device_certificate_expires", typed("datetime", "title", "Device certificate expires", "description", "When the certificate the machine reports with runs out. After that it cannot report.")),
			f("device_certificate_fingerprint", typed("string", "title", "Device certificate", "description", "SHA-256 of the device certificate, hex.")),
			f("remote", typed("string", "description", "The fetch URL of origin, else the first remote, as host/owner/name. Empty with no remote.")),
			f("folder", typed("string", "description", "The checkout's folder name, and nothing above it.")),
			n("worktrees", "Worktrees", "Its worktrees on the machine."),
			n("staged", "Staged files", "Files staged and not committed, over every worktree."),
			n("modified", "Modified files", "Files changed and not staged, over every worktree."),
			n("untracked", "Untracked files", "Files git does not track and does not ignore, over every worktree."),
			n("conflicted", "Conflicted files", "Files left mid-merge, over every worktree."),
			n("stashes", "Stashes", "Stashed changes."),
			n("unpushed_commits", "Unpushed commits", "Commits on its branches that no remote-tracking branch holds, summed over the branches."),
			n("branches_unpushed", "Branches with unpushed commits", "Branches holding commits no remote holds."),
			n("branches_behind", "Branches behind", "Branches behind their upstream, as of the machine's last fetch."),
			n("branches_upstream_gone", "Branches whose upstream is gone", "Branches whose upstream was deleted on the remote."),
			f("branches", typed("array", "description", "Each local branch: name, ahead, behind, unpushed, upstream, and whether the upstream is gone.")),
			f("scanned_at", typed("datetime", "title", "Scanned", "description", "When the machine read its checkouts.")),
			f("reported_at", typed("datetime", "title", "Reported", "description", "When Muster took the report.")),
			f("changed_files", typed("number", "title", "Changed files", "description", "Staged, modified, untracked, and conflicted files.", "calculation", changedFiles)),
			f("unseen_work", typed("boolean", "title", "Holds unseen work", "description", "It holds commits no remote has, or files not committed: work the forge has not seen.", "calculation", unseenWork)),
			f("device_certificate_days_left", typed("number", "title", "Certificate days left", "description", "Whole days until the device certificate runs out; below zero once it has.", "calculation", certificateLeft)),
			f("status", typed("enum", "values", levelValues, "overridable", false, "description", "Amber when the device certificate runs out within 14 days, red once it has and the machine can no longer report.", "calculation", checkoutStatus)),
			f("status_reason", typed("string", "overridable", false, "description", "Why it is not green.", "calculation", checkoutReason)),
		},
		"relations": map[string]any{"repository": checkoutRepository},
	}
}

// CheckoutMachineBlueprint links a checkout to the machine's record, of the
// blueprint the estate keeps its computers in.
func CheckoutMachineBlueprint(target string) sdk.Blueprint {
	return sdk.Blueprint{"id": Checkout, "title": "Checkout", "fields": Fields{}, "relations": map[string]any{
		CheckoutMachine: map[string]any{"target": target, "many": false, "title": "Machine", "inverseTitle": "Checkouts"},
	}}
}

// CheckoutOwnerBlueprint links a checkout to the record of the device's
// person, as the thing's owner, so an estate's ownership paths and an access
// rule that opens checkouts to their owners reach it.
func CheckoutOwnerBlueprint(target string) sdk.Blueprint {
	return sdk.Blueprint{"id": Checkout, "title": "Checkout", "fields": Fields{}, "relations": map[string]any{
		CheckoutOwner: map[string]any{"target": target, "many": false, "title": "Owner", "inverseTitle": "Checkouts"},
	}}
}

// CheckoutLinks are what a device's checkouts link to: the repository key a
// remote names, when the catalog holds it; the machine's key; and the
// owner's key. An empty one links nothing.
type CheckoutLinks struct {
	Repository func(remote string) string
	Machine    string
	Owner      string
}

// CheckoutKey is a checkout's key: the device, then the remote it names, or
// with no remote its folder.
func CheckoutKey(device, remote, folder string) string {
	if remote != "" {
		return device + "/" + remote
	}
	return device + "/" + folder
}

// CheckoutEntities shapes one device's report. Two checkouts of one remote
// on one machine are one record: the repository is on the machine once, and
// its counts are the sum of both.
func CheckoutEntities(r machines.Report, links CheckoutLinks) []sdk.Entity {
	type merged struct {
		remote, folder                                                   string
		worktrees, staged, modified, untracked, conflicted, stashes      int
		unpushed, branchesUnpushed, branchesBehind, branchesUpstreamGone int
		branches                                                         []any
	}
	byKey := map[string]*merged{}
	var keys []string
	for _, c := range r.Report.Checkouts {
		key := CheckoutKey(r.Device.ID, c.Remote, c.Folder)
		m := byKey[key]
		if m == nil {
			m = &merged{remote: c.Remote, folder: c.Folder, branches: []any{}}
			byKey[key] = m
			keys = append(keys, key)
		}
		m.worktrees += c.Worktrees
		m.staged += c.Staged
		m.modified += c.Modified
		m.untracked += c.Untracked
		m.conflicted += c.Conflicted
		m.stashes += c.Stashes
		for _, b := range c.Branches {
			m.unpushed += b.Unpushed
			if b.Unpushed > 0 {
				m.branchesUnpushed++
			}
			if b.Behind > 0 {
				m.branchesBehind++
			}
			if b.UpstreamGone {
				m.branchesUpstreamGone++
			}
			m.branches = append(m.branches, map[string]any{"name": b.Name, "ahead": b.Ahead, "behind": b.Behind, "unpushed": b.Unpushed, "upstream": b.Upstream, "upstream_gone": b.UpstreamGone})
		}
	}
	sort.Strings(keys)
	machine := r.Device.Name
	if machine == "" {
		machine = r.Device.ID
	}
	out := make([]sdk.Entity, 0, len(keys))
	for _, key := range keys {
		m := byKey[key]
		name := m.folder
		if m.remote != "" {
			name = m.remote[strings.LastIndex(m.remote, "/")+1:]
		}
		e := sdk.Entity{Blueprint: Checkout, Key: key, Title: name + " on " + machine, Relations: map[string]any{}, Fields: map[string]any{
			"device":                         r.Device.ID,
			"device_name":                    r.Device.Name,
			"person":                         strings.ToLower(r.Device.Person),
			"device_certificate_expires":     r.Device.NotAfter.UTC().Format(time.RFC3339),
			"device_certificate_fingerprint": r.Device.Fingerprint,
			"remote":                         m.remote,
			"folder":                         m.folder,
			"worktrees":                      m.worktrees,
			"staged":                         m.staged,
			"modified":                       m.modified,
			"untracked":                      m.untracked,
			"conflicted":                     m.conflicted,
			"stashes":                        m.stashes,
			"unpushed_commits":               m.unpushed,
			"branches_unpushed":              m.branchesUnpushed,
			"branches_behind":                m.branchesBehind,
			"branches_upstream_gone":         m.branchesUpstreamGone,
			"branches":                       m.branches,
			"scanned_at":                     r.Report.ScannedAt.UTC().Format(time.RFC3339),
			"reported_at":                    r.Received.UTC().Format(time.RFC3339),
		}}
		if m.remote != "" && links.Repository != nil {
			if repo := links.Repository(m.remote); repo != "" {
				e.Relations["repository"] = repo
			}
		}
		if links.Machine != "" {
			e.Relations[CheckoutMachine] = links.Machine
		}
		if links.Owner != "" {
			e.Relations[CheckoutOwner] = links.Owner
		}
		out = append(out, e)
	}
	return out
}

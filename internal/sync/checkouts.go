package sync

import (
	"context"
	"strings"
	"time"

	"github.com/orchard-suite/muster/internal/catalog"
	"github.com/orchard-suite/muster/internal/machines"
	"github.com/orchard-suite/muster/internal/outcomes"
	"github.com/orchard-suite/muster/internal/state"
	"github.com/orchard-suite/muster/reader"
	"github.com/orchard-suite/orrery/v2/sdk"
)

// CheckoutOptions say what a device's checkouts link to: the machine whose
// MachineMatch field, on MachineBlueprint, names the device, and the person
// whose PersonMatch field, on PersonBlueprint, holds the device's person.
// An empty blueprint links nothing of that kind.
type CheckoutOptions struct {
	MachineBlueprint, MachineMatch string
	PersonBlueprint, PersonMatch   string
}

// deviceScope is the scope a device's checkouts are written in: one
// snapshot each, so a checkout gone from its last report leaves the catalog.
func deviceScope(device string) string { return "devices/" + device }

// WriteDevice writes one device's checkouts from its last kept report, as
// soon as it posts one.
func (e *Engine) WriteDevice(ctx context.Context, device string) {
	var r machines.Report
	ok, err := e.o.Store.Report(ctx, device, reader.Name, &r)
	if err != nil || !ok {
		e.o.Log.WarnContext(ctx, "device report unreadable; its checkouts wait for the sweep", "device", device, "error", err)
		return
	}
	e.writeCheckouts(ctx, []machines.Report{r})
}

// pushCheckouts writes every device's checkouts from its last report, on
// every sweep: a link that could not be made at a post is made here.
func (e *Engine) pushCheckouts(ctx context.Context) {
	reports, err := state.Reports[machines.Report](ctx, e.o.Store, reader.Name)
	if err != nil {
		e.o.Log.ErrorContext(ctx, "device reports unreadable; no checkouts written", "error", err)
		return
	}
	if len(reports) > 0 {
		e.writeCheckouts(ctx, reports)
	}
}

// writeCheckouts writes each report as its device's snapshot. Nothing is
// written until the hub gives sw_checkout an access rule: a checkout says
// what sits on a person's machine, and an estate decides who reads it before
// any is written. Without a read token Muster cannot see the rule, so it
// writes none.
func (e *Engine) writeCheckouts(ctx context.Context, reports []machines.Report) {
	allowed, ok := e.offer.ready(ctx)
	if !ok || !allowed(catalog.Checkout) {
		return
	}
	if e.o.Read == nil {
		e.o.Log.WarnContext(ctx, "checkouts held: ORRERY_READ_TOKEN is not set, so muster cannot see who may read them", "devices", len(reports))
		return
	}
	restricted, err := e.o.Read.Restricted(ctx, catalog.Checkout)
	if err != nil {
		e.o.Log.WarnContext(ctx, "checkouts held: sw_checkout's access rule unreadable", "devices", len(reports), "error", err)
		return
	}
	if !restricted {
		e.o.Log.WarnContext(ctx, "checkouts held: sw_checkout has no access rule; an admin sets who reads them before any is written", "devices", len(reports))
		return
	}
	var machinesBy, peopleBy map[string]string
	if o := e.o.Checkouts; o.MachineBlueprint != "" && allowed(extraCheckoutMachine) {
		machinesBy = e.lookup(ctx, o.MachineBlueprint, o.MachineMatch)
	}
	if o := e.o.Checkouts; o.PersonBlueprint != "" && allowed(extraCheckoutOwner) {
		peopleBy = e.lookup(ctx, o.PersonBlueprint, o.PersonMatch)
	}
	for _, r := range reports {
		links := catalog.CheckoutLinks{Repository: e.heldRepository, Machine: machineOf(machinesBy, r.Device), Owner: peopleBy[strings.ToLower(r.Device.Person)]}
		e.o.Log.InfoContext(ctx, "device checkouts shaped", "device", r.Device.ID, "machine", links.Machine, "owned", links.Owner != "", "checkouts", len(r.Report.Checkouts), "scannedAt", r.Report.ScannedAt.Format(time.RFC3339))
		e.run(ctx, sdk.Snapshot, deviceScope(r.Device.ID), catalog.Checkout, catalog.CheckoutEntities(r, links), nil)
	}
}

// heldRepository is the key of the repository a remote names, when Muster
// watches it; else "". A checkout links only to a repository the catalog
// holds.
func (e *Engine) heldRepository(remote string) string {
	ref, ok := outcomes.ParseRepoURL("https://"+remote, e.o.GitLabHost)
	if !ok || ref.Path != "" {
		return ""
	}
	if e.get(ref.Key()) == nil {
		return ""
	}
	return ref.Key()
}

// lookup reads every entity of a blueprint and answers its key by each value
// its field holds, lowercased: a string, or each string of a list. An
// unreadable blueprint links nothing this pass.
func (e *Engine) lookup(ctx context.Context, blueprint, field string) map[string]string {
	records, err := e.o.Read.Records(ctx, blueprint, field)
	if err != nil {
		e.o.Log.WarnContext(ctx, "checkout links unreadable; checkouts are written without them", "blueprint", blueprint, "error", err)
		return nil
	}
	out := map[string]string{}
	add := func(v any, key string) {
		if s, ok := v.(string); ok && strings.TrimSpace(s) != "" {
			if _, had := out[strings.ToLower(strings.TrimSpace(s))]; !had {
				out[strings.ToLower(strings.TrimSpace(s))] = key
			}
		}
	}
	for _, r := range records {
		switch v := r.Fields[field].(type) {
		case []any:
			for _, x := range v {
				add(x, r.Key)
			}
		default:
			add(v, r.Key)
		}
	}
	return out
}

// machineOf is the machine a device is, by its name, its id, or the short
// form of either before the first dot; "" when the catalog has none.
func machineOf(by map[string]string, d machines.Device) string {
	for _, n := range []string{d.Name, d.ID} {
		n = strings.ToLower(n)
		if n == "" {
			continue
		}
		if k := by[n]; k != "" {
			return k
		}
		if short, _, ok := strings.Cut(n, "."); ok {
			if k := by[short]; k != "" {
				return k
			}
		}
	}
	return ""
}

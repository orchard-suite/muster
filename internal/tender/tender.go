// Package tender is Muster's side of Tender, the suite's device agent: the
// two calls a device makes to the Muster reader's address, behind
// platformkit's device door. A device asks what Muster takes, then posts its
// report; each post replaces the device's last one whole.
package tender

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/orchard-suite/muster/internal/machines"
	"github.com/orchard-suite/muster/internal/state"
	"github.com/orchard-suite/muster/internal/telemetry"
	"github.com/orchard-suite/muster/reader"
	"github.com/orchard-suite/platformkit/v3/device"
)

// Path is the address Tender reaches the Muster reader at.
const Path = "/tender/v1/readers/" + reader.Name

// Limits a report is held to. Tender holds every reader to names and
// counts, so no string in a report is longer than MaxString bytes.
const (
	MaxString    = 512
	MaxCheckouts = 10_000
	MaxBranches  = 10_000
)

// Options are what the routes need.
type Options struct {
	Store *state.Store
	// Every is how often a device is asked to report.
	Every time.Duration
	// Kept is told each device whose report was kept, to write its checkouts.
	Kept func(ctx context.Context, device string)
	Tel  *telemetry.Telemetry
	Log  *slog.Logger
	// Now is the clock; nil is time.Now.
	Now func() time.Time
}

// Routes serves the ask and the post on mux, both behind door.
func Routes(mux *http.ServeMux, door *device.Door, o Options) {
	h := Handler(o)
	mux.Handle("GET "+Path, o.Tel.Middleware(Path, door.Wrap(h)))
	mux.Handle("POST "+Path, o.Tel.Middleware(Path, door.Wrap(h)))
}

// Handler answers the ask and takes the post. It expects the door in front
// of it: a request with no device in its context is refused.
func Handler(o Options) http.Handler {
	if o.Now == nil {
		o.Now = time.Now
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		dev, ok := device.From(r.Context())
		if !ok {
			http.Error(w, "no device", http.StatusUnauthorized)
			return
		}
		switch r.Method {
		case http.MethodGet:
			answer(w, http.StatusOK, map[string]any{"accept": true, "version": reader.Version, "every": every(o.Every)})
		case http.MethodPost:
			take(w, r, dev, o)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	})
}

// envelope is what Tender posts around a reader's report.
type envelope struct {
	Reader  string          `json:"reader"`
	Version int             `json:"version"`
	At      time.Time       `json:"at"`
	Report  json.RawMessage `json:"report"`
}

func take(w http.ResponseWriter, r *http.Request, dev device.Device, o Options) {
	ctx := r.Context()
	var env envelope
	if err := json.NewDecoder(r.Body).Decode(&env); err != nil {
		refuse(w, r, o, dev, "invalid", http.StatusBadRequest, "the body is not a report envelope", err)
		return
	}
	if env.Reader != reader.Name {
		refuse(w, r, o, dev, "invalid", http.StatusBadRequest, "this address takes the muster reader's reports", fmt.Errorf("reader %q", env.Reader))
		return
	}
	if env.Version != reader.Version {
		o.Tel.DeviceReport(ctx, "version")
		o.Log.InfoContext(ctx, "device report refused: another version", "device", dev.ID, "version", env.Version, "takes", reader.Version)
		answer(w, http.StatusConflict, map[string]any{"version": reader.Version})
		return
	}
	if env.At.IsZero() {
		refuse(w, r, o, dev, "invalid", http.StatusBadRequest, "the envelope has no time", nil)
		return
	}
	rep, err := parse(env.Report)
	if err != nil {
		refuse(w, r, o, dev, "invalid", http.StatusBadRequest, "the report is not version 1 of the muster reader's", err)
		return
	}
	stored := machines.Report{
		Device:   machines.Device{ID: dev.ID, Name: dev.Name, Person: dev.Person, Serial: dev.Serial, Fingerprint: dev.Fingerprint, NotAfter: dev.NotAfter.UTC()},
		At:       env.At.UTC(),
		Received: o.Now().UTC(),
		Report:   rep,
	}
	kept, err := o.Store.PutReport(ctx, dev.ID, reader.Name, stored.At, stored)
	if err != nil {
		o.Log.ErrorContext(ctx, "device report not kept", "device", dev.ID, "error", err)
		http.Error(w, "the report could not be kept", http.StatusInternalServerError)
		return
	}
	if !kept {
		o.Tel.DeviceReport(ctx, "late")
		o.Log.InfoContext(ctx, "device report older than the one kept; left as it was", "device", dev.ID, "at", stored.At.Format(time.RFC3339))
		w.WriteHeader(http.StatusNoContent)
		return
	}
	o.Tel.DeviceReport(ctx, "kept")
	o.Log.InfoContext(ctx, "device report kept", "device", dev.ID, "checkouts", len(rep.Checkouts), "at", stored.At.Format(time.RFC3339), "certificateExpires", stored.Device.NotAfter.Format(time.RFC3339))
	if o.Kept != nil {
		go o.Kept(context.WithoutCancel(ctx), dev.ID)
	}
	w.WriteHeader(http.StatusNoContent)
}

// parse reads a version 1 report and holds it to names and counts. A field
// version 1 does not have is refused, so nothing more than it promises is
// ever kept.
func parse(raw json.RawMessage) (reader.Report, error) {
	var rep reader.Report
	if len(bytes.TrimSpace(raw)) == 0 {
		return rep, errors.New("no report")
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&rep); err != nil {
		return rep, err
	}
	if rep.ScannedAt.IsZero() {
		return rep, errors.New("scanned_at is missing")
	}
	if len(rep.Checkouts) > MaxCheckouts {
		return rep, fmt.Errorf("%d checkouts, at most %d", len(rep.Checkouts), MaxCheckouts)
	}
	for i, c := range rep.Checkouts {
		if err := checkout(c); err != nil {
			return rep, fmt.Errorf("checkout %d: %w", i, err)
		}
	}
	return rep, nil
}

func checkout(c reader.Checkout) error {
	if c.Remote == "" && c.Folder == "" {
		return errors.New("neither a remote nor a folder")
	}
	if strings.ContainsAny(c.Folder, `/\`) {
		return errors.New("the folder names a path")
	}
	if strings.Contains(c.Remote, "://") || strings.Contains(c.Remote, "@") {
		return errors.New("the remote carries a scheme or a user")
	}
	for _, s := range []string{c.Remote, c.Folder} {
		if len(s) > MaxString {
			return fmt.Errorf("a name of %d bytes, at most %d", len(s), MaxString)
		}
	}
	for _, n := range []int{c.Worktrees, c.Staged, c.Modified, c.Untracked, c.Conflicted, c.Stashes} {
		if n < 0 {
			return errors.New("a negative count")
		}
	}
	if len(c.Branches) > MaxBranches {
		return fmt.Errorf("%d branches, at most %d", len(c.Branches), MaxBranches)
	}
	for _, b := range c.Branches {
		if b.Name == "" || len(b.Name) > MaxString || len(b.Upstream) > MaxString {
			return errors.New("a branch name that is empty or too long")
		}
		if b.Ahead < 0 || b.Behind < 0 || b.Unpushed < 0 {
			return fmt.Errorf("branch %q: a negative count", b.Name)
		}
	}
	return nil
}

func refuse(w http.ResponseWriter, r *http.Request, o Options, dev device.Device, outcome string, code int, reason string, err error) {
	o.Tel.DeviceReport(r.Context(), outcome)
	attrs := []any{"device", dev.ID, "reason", reason}
	if err != nil {
		attrs = append(attrs, "error", err.Error())
	}
	o.Log.WarnContext(r.Context(), "device report refused", attrs...)
	http.Error(w, reason, code)
}

func answer(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

// every writes a duration the way Tender reads it: 15m, 6h, 90s.
func every(d time.Duration) string {
	switch {
	case d <= 0:
		return "15m"
	case d%time.Hour == 0:
		return fmt.Sprintf("%dh", d/time.Hour)
	case d%time.Minute == 0:
		return fmt.Sprintf("%dm", d/time.Minute)
	default:
		return fmt.Sprintf("%ds", d/time.Second)
	}
}

package sync

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	gosync "sync"
	"testing"
	"time"

	"github.com/orchard-suite/muster/internal/catalog"
	"github.com/orchard-suite/muster/internal/machines"
	"github.com/orchard-suite/muster/internal/outcomes"
	"github.com/orchard-suite/muster/internal/provider"
	"github.com/orchard-suite/muster/internal/provider/providertest"
	"github.com/orchard-suite/muster/internal/telemetry"
	"github.com/orchard-suite/muster/reader"
	"github.com/orchard-suite/orrery/v2/sdk"
)

// deviceHub is a fake Orrery for checkouts: it keeps each run's scope and
// mode with what it wrote, answers whether sw_checkout has an access rule,
// and lists machines and people.
type deviceHub struct {
	*httptest.Server
	mu         gosync.Mutex
	restricted bool
	refuse     map[string]bool // blueprints an offer of a relation to is refused
	runs       map[string]string
	written    map[string][]sdk.Entity
	open       map[string]string
}

func newDeviceHub(t *testing.T, restricted bool) *deviceHub {
	t.Helper()
	h := &deviceHub{restricted: restricted, refuse: map[string]bool{}, runs: map[string]string{}, written: map[string][]sdk.Entity{}, open: map[string]string{}}
	h.Server = httptest.NewServer(http.HandlerFunc(h.serve))
	t.Cleanup(h.Close)
	return h
}

func (h *deviceHub) serve(w http.ResponseWriter, r *http.Request) {
	h.mu.Lock()
	defer h.mu.Unlock()
	body, _ := io.ReadAll(r.Body)
	w.Header().Set("Content-Type", "application/json")
	path := r.URL.Path
	switch {
	case r.Method == http.MethodPut && path == "/v1/ingest/blueprints":
		var in struct{ Blueprints []sdk.Blueprint }
		_ = json.Unmarshal(body, &in)
		rejected := []map[string]string{}
		for _, bp := range in.Blueprints {
			for _, rel := range bp["relations"].(map[string]any) {
				if h.refuse[rel.(map[string]any)["target"].(string)] {
					rejected = append(rejected, map[string]string{"blueprint": bp["id"].(string), "reason": "no such blueprint"})
				}
			}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"created": []string{}, "extended": []string{}, "kept": []string{}, "rejected": rejected})
	case r.Method == http.MethodGet && path == "/v1/blueprints/sw_checkout":
		def := map[string]any{"id": "sw_checkout"}
		if h.restricted {
			def["access"] = map[string]any{"grants": []any{}, "owners": map[string]any{"roles": []string{"owner"}, "fields": "all"}}
		}
		_ = json.NewEncoder(w).Encode(def)
	case r.Method == http.MethodGet && path == "/v1/entities":
		var list []map[string]any
		switch r.URL.Query().Get("blueprint") {
		case "it_workstation":
			list = []map[string]any{{"key": "ws-17", "fields": map[string]any{"hostname": "LAPTOP-7"}}, {"key": "ws-18", "fields": map[string]any{"hostname": "desk-2"}}}
		case "person":
			list = []map[string]any{{"key": "p-1", "fields": map[string]any{"emails": []any{"a.user@example.com", "user@example.com"}}}}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"entities": list, "total": len(list)})
	case r.Method == http.MethodPost && path == "/v1/ingest/runs":
		var in struct{ Mode, Scope string }
		_ = json.Unmarshal(body, &in)
		id := "run-" + in.Scope
		h.open[id] = in.Scope
		h.runs[in.Scope] = in.Mode
		h.written[in.Scope] = nil
		_ = json.NewEncoder(w).Encode(map[string]any{"id": id, "sightings": false})
	case r.Method == http.MethodPut && strings.HasSuffix(path, "/entities"):
		var in struct{ Entities []sdk.Entity }
		_ = json.Unmarshal(body, &in)
		id := strings.TrimSuffix(strings.TrimPrefix(path, "/v1/ingest/runs/"), "/entities")
		h.written[h.open[id]] = append(h.written[h.open[id]], in.Entities...)
		_ = json.NewEncoder(w).Encode(map[string]any{"accepted": len(in.Entities), "rejected": []any{}})
	case r.Method == http.MethodPost && strings.HasSuffix(path, "/commit"):
		_, _ = w.Write([]byte(`{"id":"run","state":"committed","counts":{}}`))
	default:
		http.Error(w, `{"message":"not found"}`, http.StatusNotFound)
	}
}

func (h *deviceHub) scope(s string) (string, []sdk.Entity) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.runs[s], h.written[s]
}

// devices is an engine that watches github/acme/widgets, writing to h and
// reading it with an access token.
func devices(t *testing.T, h *deviceHub) *Engine {
	t.Helper()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	tel, err := telemetry.Setup(context.Background(), "", "", "test", log)
	if err != nil {
		t.Fatal(err)
	}
	store := providertest.Store(t)
	read := outcomes.NewReader(h.URL, "read", store, h.Client(), log)
	e := New(Options{Store: store, Push: sdk.New(h.URL, "token", sdk.WithLogger(log)), Read: &read, Tel: tel, Log: log,
		Checkouts: CheckoutOptions{MachineBlueprint: "it_workstation", MachineMatch: "hostname", PersonBlueprint: "person", PersonMatch: "emails"}})
	key := catalog.RepoKey(provider.GitHub, "acme/widgets")
	e.repos[key] = &tracked{repo: &provider.Repo{Provider: provider.GitHub, FullName: "acme/widgets"}, ok: true}
	return e
}

func report(t *testing.T, e *Engine, id, name string, checkouts ...reader.Checkout) {
	t.Helper()
	r := machines.Report{
		Device:   machines.Device{ID: id, Name: name, Person: "User@example.com", Fingerprint: "ab12", NotAfter: time.Date(2027, 1, 10, 0, 0, 0, 0, time.UTC)},
		At:       time.Now(),
		Received: time.Now(),
		Report:   reader.Report{ScannedAt: time.Now().UTC().Truncate(time.Second), Checkouts: checkouts},
	}
	if _, err := e.o.Store.PutReport(context.Background(), id, reader.Name, r.At, r); err != nil {
		t.Fatal(err)
	}
}

func TestADevicesCheckoutsLinkToTheRepositoryTheMachineAndItsPerson(t *testing.T) {
	h := newDeviceHub(t, true)
	e := devices(t, h)
	report(t, e, "laptop-7", "laptop-7.example.com",
		reader.Checkout{Remote: "github.com/acme/widgets", Folder: "widgets", Worktrees: 1, Modified: 2, Branches: []reader.Branch{{Name: "main", Unpushed: 1, Upstream: "origin/main"}}},
		reader.Checkout{Remote: "github.com/acme/widgets", Folder: "widgets-2", Worktrees: 1, Untracked: 1},
		reader.Checkout{Remote: "gitlab.example.com/other/thing", Folder: "thing", Worktrees: 1},
		reader.Checkout{Folder: "notes", Worktrees: 1},
	)
	e.WriteDevice(context.Background(), "laptop-7")

	mode, list := h.scope("devices/laptop-7")
	if mode != string(sdk.Snapshot) || len(list) != 3 {
		t.Fatalf("mode %s, entities %+v", mode, list)
	}
	by := map[string]sdk.Entity{}
	for _, x := range list {
		by[x.Key] = x
	}
	w := by["laptop-7/github.com/acme/widgets"]
	if w.Relations["repository"] != "github/acme/widgets" || w.Relations["machine"] != "ws-17" || w.Relations["owner"] != "p-1" {
		t.Fatalf("widgets links %v", w.Relations)
	}
	// Two clones of one remote on one machine are one checkout.
	if w.Fields["worktrees"] != float64(2) || w.Fields["modified"] != float64(2) || w.Fields["untracked"] != float64(1) || w.Fields["unpushed_commits"] != float64(1) {
		t.Fatalf("widgets fields %v", w.Fields)
	}
	if w.Fields["person"] != "user@example.com" || w.Fields["device_certificate_expires"] != "2027-01-10T00:00:00Z" || w.Title != "widgets on laptop-7.example.com" {
		t.Fatalf("widgets device %v %s", w.Fields, w.Title)
	}
	// A remote the catalog does not hold, and a repository with no remote,
	// still land, linked only to the machine.
	for _, k := range []string{"laptop-7/gitlab.example.com/other/thing", "laptop-7/notes"} {
		x, ok := by[k]
		if !ok {
			t.Fatalf("no %s in %v", k, by)
		}
		if _, linked := x.Relations["repository"]; linked || x.Relations["machine"] != "ws-17" {
			t.Fatalf("%s links %v", k, x.Relations)
		}
	}
}

func TestAMachineTheCatalogLacksStillReportsItsCheckouts(t *testing.T) {
	h := newDeviceHub(t, true)
	e := devices(t, h)
	report(t, e, "unknown-box", "", reader.Checkout{Folder: "notes", Worktrees: 1})
	e.WriteDevice(context.Background(), "unknown-box")

	_, list := h.scope("devices/unknown-box")
	if len(list) != 1 {
		t.Fatalf("entities %+v", list)
	}
	x := list[0]
	if _, linked := x.Relations["machine"]; linked || x.Fields["device"] != "unknown-box" || x.Title != "notes on unknown-box" {
		t.Fatalf("got %+v", x)
	}
}

func TestNoCheckoutIsWrittenUntilTheEstateSaysWhoReadsThem(t *testing.T) {
	h := newDeviceHub(t, false)
	e := devices(t, h)
	report(t, e, "laptop-7", "laptop-7", reader.Checkout{Folder: "notes", Worktrees: 1})
	e.pushCheckouts(context.Background())
	if mode, _ := h.scope("devices/laptop-7"); mode != "" {
		t.Fatalf("checkouts written to an open blueprint: %s", mode)
	}

	// Without a read token Muster cannot see the rule, so it writes none either.
	h.restricted = true
	e.o.Read = nil
	e.pushCheckouts(context.Background())
	if mode, _ := h.scope("devices/laptop-7"); mode != "" {
		t.Fatalf("checkouts written blind: %s", mode)
	}
}

func TestARefusedLinkLeavesTheCheckoutsStanding(t *testing.T) {
	h := newDeviceHub(t, true)
	h.refuse["it_workstation"] = true
	e := devices(t, h)
	report(t, e, "laptop-7", "laptop-7", reader.Checkout{Remote: "github.com/acme/widgets", Folder: "widgets", Worktrees: 1})
	e.WriteDevice(context.Background(), "laptop-7")

	_, list := h.scope("devices/laptop-7")
	if len(list) != 1 {
		t.Fatalf("entities %+v", list)
	}
	if _, linked := list[0].Relations["machine"]; linked || list[0].Relations["owner"] != "p-1" || list[0].Relations["repository"] != "github/acme/widgets" {
		t.Fatalf("links %v", list[0].Relations)
	}
}

func TestACheckoutGoneFromTheReportLeavesWithTheNextSnapshot(t *testing.T) {
	h := newDeviceHub(t, true)
	e := devices(t, h)
	report(t, e, "laptop-7", "laptop-7", reader.Checkout{Folder: "a", Worktrees: 1}, reader.Checkout{Folder: "b", Worktrees: 1})
	e.pushCheckouts(context.Background())
	report(t, e, "laptop-7", "laptop-7", reader.Checkout{Folder: "b", Worktrees: 1})
	e.pushCheckouts(context.Background())
	mode, list := h.scope("devices/laptop-7")
	if mode != string(sdk.Snapshot) || len(list) != 1 || list[0].Key != "laptop-7/b" {
		t.Fatalf("mode %s, entities %+v", mode, list)
	}
}

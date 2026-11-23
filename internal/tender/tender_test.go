package tender

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	gosync "sync"
	"testing"
	"time"

	"github.com/orchard-suite/muster/internal/machines"
	"github.com/orchard-suite/muster/internal/state"
	"github.com/orchard-suite/muster/internal/telemetry"
	"github.com/orchard-suite/muster/reader"
	"github.com/orchard-suite/platformkit/v3/device"
	"github.com/orchard-suite/platformkit/v3/device/devicetest"
)

// member is Muster's routes behind a real door, with an authority minted in
// memory and a device it issued.
type member struct {
	url   string
	store *state.Store
	dev   devicetest.Issued
	kept  chan string
}

func newMember(t *testing.T) *member {
	t.Helper()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	tel, err := telemetry.Setup(context.Background(), "", "", "test", log)
	if err != nil {
		t.Fatal(err)
	}
	store, err := state.Open(filepath.Join(t.TempDir(), "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	ca := devicetest.New(t)
	m := &member{store: store, kept: make(chan string, 4),
		dev: ca.Issue(t, devicetest.Leaf{CN: "laptop-7", Email: "user@example.com", DNS: []string{"laptop-7.example.com"}})}

	var mu gosync.Mutex
	var h http.Handler
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		h.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	m.url = srv.URL
	door, err := device.New(device.Config{Audience: srv.URL, Roots: ca.Roots, HTTP: ca.Server.Client()}, log)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	Routes(mux, door, Options{Store: store, Every: 15 * time.Minute, Tel: tel, Log: log,
		Kept: func(_ context.Context, id string) { m.kept <- id }})
	h = mux
	return m
}

// call signs a request as the device and sends it.
func (m *member) call(t *testing.T, method string, body []byte, signed bool) (*http.Response, string) {
	t.Helper()
	req, err := http.NewRequest(method, m.url+Path, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	if signed {
		if err := devicetest.Sign(req, m.dev.Key, m.dev.Chain, m.url); err != nil {
			t.Fatal(err)
		}
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return res, strings.TrimSpace(string(b))
}

func post(t *testing.T, version int, at time.Time, report string) []byte {
	t.Helper()
	b, err := json.Marshal(map[string]any{"reader": "muster", "version": version, "at": at, "report": json.RawMessage(report)})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

const sample = `{"scanned_at":"2026-11-23T14:00:00Z","checkouts":[
 {"remote":"github.com/acme/widgets","folder":"widgets","worktrees":2,"staged":0,"modified":3,"untracked":1,"conflicted":0,"stashes":1,
  "branches":[{"name":"main","ahead":0,"behind":2,"unpushed":0,"upstream":"origin/main","upstream_gone":false},
              {"name":"spike","ahead":0,"behind":0,"unpushed":4,"upstream":"","upstream_gone":false}]}]}`

func TestTheAskSaysWhatMusterTakes(t *testing.T) {
	m := newMember(t)
	res, body := m.call(t, http.MethodGet, nil, true)
	if res.StatusCode != http.StatusOK || body != `{"accept":true,"every":"15m","version":1}` {
		t.Fatalf("%d %s", res.StatusCode, body)
	}
}

func TestAPostIsKeptWithTheDeviceTheDoorNamed(t *testing.T) {
	m := newMember(t)
	at := time.Date(2026, 11, 23, 14, 0, 5, 0, time.UTC)
	res, body := m.call(t, http.MethodPost, post(t, 1, at, sample), true)
	if res.StatusCode != http.StatusNoContent {
		t.Fatalf("%d %s", res.StatusCode, body)
	}
	if id := <-m.kept; id != "laptop-7" {
		t.Fatalf("told of %q", id)
	}
	var got machines.Report
	if ok, err := m.store.Report(context.Background(), "laptop-7", reader.Name, &got); !ok || err != nil {
		t.Fatalf("nothing kept: %v", err)
	}
	if got.Device.Name != "laptop-7.example.com" || got.Device.Person != "user@example.com" || got.Device.NotAfter.IsZero() || got.Device.Fingerprint == "" {
		t.Fatalf("device %+v", got.Device)
	}
	if !got.At.Equal(at) || len(got.Report.Checkouts) != 1 || got.Report.Checkouts[0].Branches[1].Unpushed != 4 {
		t.Fatalf("report %+v", got)
	}
}

func TestANewerPostReplacesTheLastWholeAndALateOneDoesNot(t *testing.T) {
	m := newMember(t)
	at := time.Now().UTC()
	m.call(t, http.MethodPost, post(t, 1, at, sample), true)
	<-m.kept
	empty := `{"scanned_at":"2026-11-23T15:00:00Z","checkouts":[]}`
	if res, body := m.call(t, http.MethodPost, post(t, 1, at.Add(time.Minute), empty), true); res.StatusCode != http.StatusNoContent {
		t.Fatalf("%d %s", res.StatusCode, body)
	}
	<-m.kept
	if res, _ := m.call(t, http.MethodPost, post(t, 1, at, sample), true); res.StatusCode != http.StatusNoContent {
		t.Fatalf("a late post is answered as taken, got %d", res.StatusCode)
	}
	var got machines.Report
	_, _ = m.store.Report(context.Background(), "laptop-7", reader.Name, &got)
	if len(got.Report.Checkouts) != 0 {
		t.Fatalf("kept %+v; the newer, empty report stands", got.Report)
	}
	select {
	case id := <-m.kept:
		t.Fatalf("a late post was written for %s", id)
	case <-time.After(50 * time.Millisecond):
	}
}

func TestAnotherVersionIsAnsweredWithTheOneMusterTakes(t *testing.T) {
	m := newMember(t)
	res, body := m.call(t, http.MethodPost, post(t, 2, time.Now(), sample), true)
	if res.StatusCode != http.StatusConflict || body != `{"version":1}` {
		t.Fatalf("%d %s", res.StatusCode, body)
	}
}

func TestAReportThatSaysMoreThanNamesAndCountsIsRefused(t *testing.T) {
	m := newMember(t)
	for name, report := range map[string]string{
		"a file name":          `{"scanned_at":"2026-11-23T14:00:00Z","checkouts":[{"remote":"","folder":"x","files":["plan.txt"]}]}`,
		"a path":               `{"scanned_at":"2026-11-23T14:00:00Z","checkouts":[{"remote":"","folder":"home/someone/x"}]}`,
		"a remote with a user": `{"scanned_at":"2026-11-23T14:00:00Z","checkouts":[{"remote":"https://user@github.com/acme/x","folder":"x"}]}`,
		"a long name":          `{"scanned_at":"2026-11-23T14:00:00Z","checkouts":[{"remote":"","folder":"` + strings.Repeat("a", 513) + `"}]}`,
		"a negative count":     `{"scanned_at":"2026-11-23T14:00:00Z","checkouts":[{"remote":"","folder":"x","modified":-1}]}`,
		"no scan time":         `{"checkouts":[]}`,
	} {
		res, _ := m.call(t, http.MethodPost, post(t, 1, time.Now(), report), true)
		if res.StatusCode != http.StatusBadRequest {
			t.Errorf("%s: %d", name, res.StatusCode)
		}
	}
	var got machines.Report
	if ok, _ := m.store.Report(context.Background(), "laptop-7", reader.Name, &got); ok {
		t.Fatal("a refused report was kept")
	}
}

func TestTheDoorRefusesAnUnsignedCall(t *testing.T) {
	m := newMember(t)
	for _, method := range []string{http.MethodGet, http.MethodPost} {
		if res, _ := m.call(t, method, post(t, 1, time.Now(), sample), false); res.StatusCode != http.StatusUnauthorized {
			t.Errorf("%s unsigned: %d", method, res.StatusCode)
		}
	}
}

func TestEveryIsWrittenAsTenderReadsIt(t *testing.T) {
	for d, want := range map[time.Duration]string{0: "15m", 15 * time.Minute: "15m", 6 * time.Hour: "6h", 90 * time.Second: "90s"} {
		if got := every(d); got != want {
			t.Errorf("%s: %s", d, got)
		}
	}
}

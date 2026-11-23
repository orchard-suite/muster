package main

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	"github.com/orchard-suite/muster/internal/catalog"
	"github.com/orchard-suite/muster/internal/config"
	"github.com/orchard-suite/muster/internal/outcomes"
	"github.com/orchard-suite/muster/internal/provider"
	"github.com/orchard-suite/muster/internal/provider/azuredevops"
	"github.com/orchard-suite/muster/internal/provider/github"
	"github.com/orchard-suite/muster/internal/provider/gitlab"
	"github.com/orchard-suite/muster/internal/services"
	"github.com/orchard-suite/muster/internal/state"
	msync "github.com/orchard-suite/muster/internal/sync"
	"github.com/orchard-suite/muster/internal/telemetry"
	"github.com/orchard-suite/muster/internal/tender"
	"github.com/orchard-suite/orrery/v2/sdk"
	"github.com/orchard-suite/platformkit/v3/device"
)

// webhookLimit is the largest webhook body read.
const webhookLimit = 25 << 20

// stateFile is the state's file name in DATA_DIR.
const stateFile = "state.sqlite"

func serve() error {
	base := telemetry.NewLogger(os.Stdout)
	log := base.With("component", "muster")
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	tel, err := telemetry.Setup(ctx, cfg.OtelEndpoint, cfg.OtelService, version, log)
	if err != nil {
		return err
	}
	store, err := state.Open(filepath.Join(cfg.DataDir, stateFile))
	if err != nil {
		return err
	}
	defer store.Close()
	client := &http.Client{Timeout: 60 * time.Second, Transport: telemetry.Transport(nil)}

	gh, err := github.New(github.Config{AppID: cfg.GitHubAppID, PrivateKey: cfg.GitHubAppPrivateKey, WebhookSecret: cfg.GitHubWebhookSecret, Allowed: cfg.GitHubAllowed, Token: cfg.GitHubToken, TokenAccounts: cfg.GitHubTokenAccounts}, store, client, base.With("component", "github"))
	if err != nil {
		return err
	}
	gl, err := gitlab.New(gitlab.Config{URL: cfg.GitLabURL, Allowed: cfg.GitLabAllowed, ClientID: cfg.GitLabClientID, ClientSecret: cfg.GitLabClientSecret, RefreshToken: cfg.GitLabRefreshToken, WebhookSecret: cfg.GitLabWebhookSecret, Token: cfg.GitLabToken, TokenAccounts: cfg.GitLabTokenAccounts, SecretKey: cfg.SecretKey}, store, client, base.With("component", "gitlab"))
	if err != nil {
		return err
	}
	ado := azuredevops.New(azuredevops.Config{TenantID: cfg.AzureTenantID, ClientID: cfg.AzureClientID, ClientSecret: cfg.AzureClientSecret, Organizations: cfg.AzureOrganizations}, store, client, base.With("component", "azuredevops"))
	providers := []provider.Provider{gh, gl, ado}
	tel.WatchQuotas(func() []telemetry.Quota {
		var out []telemetry.Quota
		for _, p := range providers {
			out = append(out, p.Quotas()...)
		}
		return out
	})

	hub := func(token, component string) *sdk.Client {
		if cfg.OrreryURL == "" || token == "" {
			return nil
		}
		return sdk.New(cfg.OrreryURL, token, sdk.WithHTTPClient(client), sdk.WithLogger(base.With("component", component)))
	}
	push := hub(cfg.OrreryToken, "orrery")
	if push == nil {
		log.Warn("orrery push off: ORRERY_URL and ORRERY_TOKEN are not both set")
	}
	var reader *outcomes.Reader
	if cfg.OrreryURL != "" && cfg.OrreryReadToken != "" {
		r := outcomes.NewReader(cfg.OrreryURL, cfg.OrreryReadToken, store, client, base.With("component", "orrery-read"))
		reader = &r
	} else {
		log.Info("orrery read off: ORRERY_URL and ORRERY_READ_TOKEN are not both set; Argo CD syncs and incidents are not read")
	}
	gitlabHost := ""
	if u, err := url.Parse(cfg.GitLabURL); err == nil {
		gitlabHost = u.Hostname()
	}
	engine := msync.New(msync.Options{Providers: providers, Store: store, Push: push, Read: reader, Tel: tel, Log: base.With("component", "sync"),
		Bots: services.Bots{Extra: cfg.BotAuthors}, Probe: cfg.ProbePaths, Interval: cfg.SyncInterval, Concurrency: cfg.Concurrency, GitLabHost: gitlabHost, ComponentFields: cfg.ComponentFields, ArgoServices: cfg.ArgoServices,
		Flaky:     catalog.FlakyRule{Window: cfg.FlakyWindowRuns, Clear: cfg.FlakyClearRuns, MaxAge: time.Duration(cfg.FlakyMaxAgeDays) * 24 * time.Hour},
		Checkouts: msync.CheckoutOptions{MachineBlueprint: cfg.MachineBlueprint, MachineMatch: cfg.MachineMatch, PersonBlueprint: cfg.PersonBlueprint, PersonMatch: cfg.PersonMatch}})
	go engine.Run(ctx)

	mux := http.NewServeMux()
	mux.Handle("GET /healthz", tel.Middleware("/healthz", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "version": version})
	})))
	for _, p := range []provider.Provider{gh, gl} {
		hooks, ok := p.(provider.Webhooks)
		if !ok {
			continue
		}
		kind := p.Kind()
		route := "/api/webhooks/" + string(kind)
		mux.Handle("POST "+route, tel.Middleware(route, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			body, err := io.ReadAll(io.LimitReader(r.Body, webhookLimit))
			if err != nil {
				http.Error(w, "unreadable", http.StatusBadRequest)
				return
			}
			d, err := hooks.Webhook(r.Context(), r.Header, body)
			if err != nil {
				tel.Webhook(r.Context(), string(kind), "rejected")
				log.WarnContext(r.Context(), "webhook rejected", "provider", kind, "error", err)
				http.Error(w, "rejected", http.StatusBadRequest)
				return
			}
			engine.Webhook(context.WithoutCancel(r.Context()), kind, d)
			w.WriteHeader(http.StatusAccepted)
			_, _ = w.Write([]byte("OK"))
		})))
	}
	if cfg.TenderAudience == "" {
		log.Info("device door off: TENDER_AUDIENCE and DEVICE_CA_FILE are not set; no machine reports are taken")
	} else {
		door, err := openDoor(cfg, base.With("component", "door"))
		if err != nil {
			return err
		}
		tender.Routes(mux, door, tender.Options{Store: store, Every: cfg.TenderEvery, Kept: engine.WriteDevice, Tel: tel, Log: base.With("component", "tender")})
		log.Info("device door open", "audience", cfg.TenderAudience, "path", tender.Path, "every", cfg.TenderEvery.String())
	}
	server := &http.Server{Addr: ":" + strconv.Itoa(cfg.Port), Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	go func() {
		<-ctx.Done()
		log.Info("shutting down")
		shut, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = server.Shutdown(shut)
		_ = tel.Shutdown(shut)
	}()
	log.Info("listening", "port", cfg.Port, "version", version, "dataDir", cfg.DataDir, "publicUrl", cfg.PublicURL)
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// openDoor makes the device door from the estate's device authority.
func openDoor(cfg config.Config, log *slog.Logger) (*device.Door, error) {
	pem, err := os.ReadFile(cfg.DeviceCAFile)
	if err != nil {
		return nil, fmt.Errorf("DEVICE_CA_FILE: %w", err)
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(pem) {
		return nil, fmt.Errorf("DEVICE_CA_FILE: %s holds no PEM certificate", cfg.DeviceCAFile)
	}
	return device.New(device.Config{
		Audience:   cfg.TenderAudience,
		Roots:      roots,
		DeviceFrom: cfg.DeviceIDFrom,
		PersonFrom: cfg.DevicePersonFrom,
		HTTP:       &http.Client{Timeout: 10 * time.Second, Transport: telemetry.Transport(nil)},
	}, log)
}

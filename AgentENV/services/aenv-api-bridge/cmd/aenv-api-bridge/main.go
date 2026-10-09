package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"agentenv/services/aenv-api-bridge/internal/auth"
	"agentenv/services/aenv-api-bridge/internal/catalog"
	"agentenv/services/aenv-api-bridge/internal/control"
	"agentenv/services/aenv-api-bridge/internal/creation"
	"agentenv/services/aenv-api-bridge/internal/dataplane"
	"agentenv/services/aenv-api-bridge/internal/expiry"
	extensionservice "agentenv/services/aenv-api-bridge/internal/extensions"
	forkservice "agentenv/services/aenv-api-bridge/internal/fork"
	"agentenv/services/aenv-api-bridge/internal/guestmetrics"
	"agentenv/services/aenv-api-bridge/internal/httpapi"
	"agentenv/services/aenv-api-bridge/internal/lifecycle"
	"agentenv/services/aenv-api-bridge/internal/metadata"
	"agentenv/services/aenv-api-bridge/internal/servertls"
	"agentenv/services/aenv-api-bridge/internal/snapshots"
	"agentenv/services/aenv-api-bridge/internal/templateregistry"
	"github.com/jackc/pgx/v5/pgxpool"
)

type config struct {
	ColdTemplates           map[string]string   `json:"coldTemplates"`
	DefaultRefreshTimeout   *int                `json:"defaultRefreshTimeout"`
	Templates               []creation.Template `json:"templates"`
	IngressOrigin           string              `json:"ingressOrigin"`
	IngressRoots            string              `json:"ingressRoots"`
	IngressCredentialBundle string              `json:"ingressCredentialBundle"`
	DataCredentialKeyFile   string              `json:"dataCredentialKeyFile"`
	AutoResumeTimeout       int                 `json:"autoResumeTimeout"`
	SandboxDomain           string              `json:"sandboxDomain"`
	Listen                  string              `json:"listen"`
	Certificate             string              `json:"certificate"`
	PrivateKey              string              `json:"privateKey"`
	GatewayRoots            string              `json:"gatewayRoots"`
	GatewayIdentities       []string            `json:"gatewayIdentities"`
	ControlAddress          string              `json:"controlAddress"`
	ControlServerName       string              `json:"controlServerName"`
	ControlRoots            string              `json:"controlRoots"`
	Tenants                 []auth.Tenant       `json:"tenants"`
}

func readConfig(path string) (config, error) {
	var c config
	f, err := os.Open(path)
	if err != nil {
		return c, err
	}
	defer f.Close()
	dec := json.NewDecoder(io.LimitReader(f, 1<<20))
	dec.DisallowUnknownFields()
	if err = dec.Decode(&c); err != nil {
		return c, err
	}
	var extra any
	if dec.Decode(&extra) != io.EOF {
		return c, fmt.Errorf("exactly one config object required")
	}
	if c.Listen == "" {
		return c, fmt.Errorf("explicit bridge listen address required")
	}
	return c, nil
}
func run(ctx context.Context, path string) error {
	c, err := readConfig(path)
	if err != nil {
		return fmt.Errorf("invalid bridge configuration: %w", err)
	}
	authenticator, err := auth.New(c.Tenants, c.GatewayIdentities)
	if err != nil {
		return err
	}
	serverTLS, err := servertls.Rotating(c.Certificate, c.PrivateKey, c.GatewayRoots)
	if err != nil {
		return err
	}
	rpc, closeRPC, err := control.Dial(c.ControlAddress, c.ControlServerName, c.ControlRoots)
	if err != nil {
		return err
	}
	defer closeRPC()
	dsn := os.Getenv("AENV_BRIDGE_DATABASE_URL")
	if dsn == "" {
		return fmt.Errorf("AENV_BRIDGE_DATABASE_URL required")
	}
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return fmt.Errorf("invalid bridge database configuration")
	}
	defer pool.Close()
	setup, cancel := context.WithTimeout(ctx, 30*time.Second)
	err = pool.Ping(setup)
	store := &metadata.Store{Pool: pool}
	if err == nil {
		err = store.Migrate(setup)
	}
	if err == nil {
		err = (&catalog.Catalog{Pool: pool}).Migrate(setup)
	}
	registry := &templateregistry.Store{Pool: pool}
	if err == nil {
		err = registry.Migrate(setup)
	}
	metricsStore := &guestmetrics.Store{Pool: pool}
	if err == nil {
		err = metricsStore.Migrate(setup)
	}
	cancel()
	if err != nil {
		return fmt.Errorf("bridge database startup failed: %w", err)
	}
	tenants := map[string]auth.Tenant{}
	for _, t := range c.Tenants {
		tenants[t.ID] = t
	}
	if c.ColdTemplates == nil {
		c.ColdTemplates = map[string]string{}
		for _, p := range c.Templates {
			if c.ColdTemplates[p.Tenant] == "" {
				c.ColdTemplates[p.Tenant] = p.ID
			}
		}
	}
	creations := &creation.Service{ColdTemplates: c.ColdTemplates, Images: creation.RegistryImages{}, Store: store, Control: rpc, Templates: c.Templates, Registry: registry, Tenants: tenants}
	if err = creations.Validate(); err != nil {
		return err
	}
	// Validate and freeze pre-provisioned artifact identities before publishing SDK aliases.
	for _, profile := range c.Templates {
		native, err := rpc.Template(ctx, tenants[profile.Tenant], profile.Name)
		if err != nil {
			return fmt.Errorf("resolve configured native template: %w", err)
		}
		if err = creation.ValidateTemplateResources(profile, native); err != nil {
			return err
		}
		digest, err := creation.NativeTemplateDigest(native)
		if err != nil {
			return err
		}
		if profile.NativeUID != "" && profile.NativeUID != native.GetMetadata().GetUid() || profile.NativeDigest != "" && profile.NativeDigest != digest {
			return fmt.Errorf("configured native template identity mismatch")
		}
		profile.NativeUID = native.GetMetadata().GetUid()
		profile.NativeDigest = digest
		created, updated := native.GetMetadata().GetCreateTime(), native.GetMetadata().GetUpdateTime()
		if created == nil || updated == nil || created.CheckValid() != nil || updated.CheckValid() != nil {
			return fmt.Errorf("native template creation/update times required")
		}
		if err = registry.Publish(ctx, profile, created.AsTime(), updated.AsTime()); err != nil {
			return fmt.Errorf("publish configured template: %w", err)
		}
	}
	forks := &forkservice.Service{Store: store, Control: rpc, Children: creations, Tenants: tenants}
	extensions := &extensionservice.Service{Store: store, Control: rpc, Tenants: tenants}
	pauses := &lifecycle.Service{Store: store, Control: rpc, Tenants: tenants}

	tokens, err := dataplane.LoadTokens(c.DataCredentialKeyFile)
	if err != nil {
		return err
	}
	target, transport, err := dataplane.Transport(c.IngressOrigin, c.IngressRoots, c.IngressCredentialBundle)
	if err != nil {
		return err
	}
	defer transport.CloseIdleConnections()
	if c.AutoResumeTimeout == 0 {
		c.AutoResumeTimeout = 300
	}
	data := &dataplane.Handler{Domain: c.SandboxDomain, Target: target, Transport: transport, Store: store, Control: rpc, Lifecycle: pauses, Gateway: authenticator, Tokens: tokens, Tenants: tenants, AutoResumeTimeout: c.AutoResumeTimeout}
	if err = data.Validate(); err != nil {
		return err
	}
	refreshTimeout := 15
	if c.DefaultRefreshTimeout != nil {
		refreshTimeout = *c.DefaultRefreshTimeout
	}
	if refreshTimeout < 0 || uint64(refreshTimeout) > 4294967295 {
		return fmt.Errorf("invalid default refresh timeout")
	}
	refreshes := &lifecycle.RefreshService{Store: store, Control: rpc, DefaultTimeout: refreshTimeout}
	snapshotsService := &snapshots.Service{Store: store, Control: rpc, Tenants: tenants}
	api := &httpapi.Server{Snapshots: snapshotsService, ColdCreations: creations, Refreshes: refreshes, Templates: registry, Extensions: extensions, Forks: forks, Infos: store, InfoControl: rpc, Creations: creations, Profiles: store, Data: data, Auth: authenticator, Store: store, Control: rpc, LatestMetrics: metricsStore, Metrics: metricsStore, Pauses: pauses, Connections: pauses, SandboxDomain: c.SandboxDomain, RequestTimeout: 2 * time.Minute}
	handler, err := api.Handler()
	if err != nil {
		return err
	}
	server := &http.Server{Addr: c.Listen, Handler: handler, TLSConfig: serverTLS, ReadHeaderTimeout: 10 * time.Second, IdleTimeout: time.Minute, MaxHeaderBytes: 32 << 10}
	worker := &expiry.Worker{Pauses: pauses, Store: store, Control: rpc, Tenants: tenants, Batch: 100, RPCTimeout: time.Minute}
	lifetime, stop := context.WithCancel(ctx)
	defer stop()
	workerDone := make(chan error, 1)
	go func() {
		workerDone <- worker.Run(lifetime, 5*time.Second, func(err error) { slog.Error("expiry sweep remains pending", "error", err) })
	}()
	creationDone := make(chan error, 1)
	go func() {
		creationDone <- creations.Run(lifetime, func(err error) { slog.Error("creation reconciliation remains pending", "error", err) })
	}()
	extensionDone := make(chan error, 1)
	go func() {
		extensionDone <- extensions.Run(lifetime, func(err error) { slog.Error("extension reconciliation remains pending", "error", err) })
	}()
	forkDone := make(chan error, 1)
	go func() {
		forkDone <- forks.Run(lifetime, func(err error) { slog.Error("fork reconciliation remains pending", "error", err) })
	}()
	lifecycleDone := make(chan error, 1)
	go func() {
		lifecycleDone <- pauses.Run(lifetime, func(err error) { slog.Error("lifecycle reconciliation remains pending", "error", err) })
	}()
	metricsDone := make(chan error, 1)
	collector := &guestmetrics.Collector{Store: metricsStore, Control: rpc, Tenants: tenants}
	go func() {
		metricsDone <- collector.Run(lifetime, func(err error) { slog.Warn("guest metrics sampling incomplete", "error", err) })
	}()
	snapshotsDone := make(chan error, 1)
	go func() {
		snapshotsDone <- snapshotsService.Run(lifetime, func(err error) { slog.Error("snapshot capture reconciliation remains pending", "error", err) })
	}()
	serverDone := make(chan error, 1)
	go func() { serverDone <- server.ListenAndServeTLS("", "") }()
	select {
	case <-ctx.Done():
		err = ctx.Err()
	case err = <-extensionDone:
		extensionDone = nil
	case err = <-forkDone:
		forkDone = nil
	case err = <-creationDone:
		creationDone = nil
	case err = <-lifecycleDone:
		lifecycleDone = nil
	case err = <-metricsDone:
		metricsDone = nil
	case err = <-workerDone:
		workerDone = nil
	case err = <-snapshotsDone:
		snapshotsDone = nil
	case err = <-serverDone:
		serverDone = nil
	}
	stop()
	shutdown, finish := context.WithTimeout(context.Background(), 30*time.Second)
	shutdownErr := server.Shutdown(shutdown)
	finish()
	if shutdownErr != nil {
		_ = server.Close()
	}
	if serverDone != nil {
		<-serverDone
	}
	if extensionDone != nil {
		<-extensionDone
	}
	if forkDone != nil {
		<-forkDone
	}
	if creationDone != nil {
		<-creationDone
	}
	if lifecycleDone != nil {
		<-lifecycleDone
	}
	if snapshotsDone != nil {
		<-snapshotsDone
	}
	if metricsDone != nil {
		<-metricsDone
	}
	if workerDone != nil {
		<-workerDone
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, http.ErrServerClosed) {
		err = nil
	}
	return errors.Join(err, shutdownErr)
}
func main() {
	if len(os.Args) != 2 {
		slog.Error("usage: aenv-api-bridge CONFIG.json")
		os.Exit(2)
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1]); err != nil {
		slog.Error("bridge stopped", "error", err)
		os.Exit(1)
	}
}

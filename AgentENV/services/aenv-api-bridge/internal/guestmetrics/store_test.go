package guestmetrics

import (
	"agentenv/services/aenv-api-bridge/internal/metadata"
	"context"
	"crypto/rand"
	"encoding/hex"
	pb "github.com/agent-substrate/substrate/pkg/proto/ateapipb"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"os"
	"strings"
	"testing"
	"time"
)

func databaseRequired() bool {
	return os.Getenv("CI") == "true" || os.Getenv("REQUIRE_CATALOG_DATABASE") == "true"
}
func testStore(t *testing.T) *Store {
	t.Helper()
	dsn := os.Getenv("AENV_CATALOG_TEST_DSN")
	if dsn == "" {
		if databaseRequired() {
			t.Fatal("AENV_CATALOG_TEST_DSN is required for bridge metadata database tests")
		}
		t.Skip("bridge metadata database tests not executed: AENV_CATALOG_TEST_DSN absent")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	config, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	admin, err := pgxpool.NewWithConfig(ctx, config.Copy())
	if err != nil {
		t.Fatal(err)
	}
	token := make([]byte, 12)
	if _, err := rand.Read(token); err != nil {
		admin.Close()
		t.Fatal(err)
	}
	name := "aenv_metadata_test_" + hex.EncodeToString(token)
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+pgx.Identifier{name}.Sanitize()); err != nil {
		admin.Close()
		t.Fatal(err)
	}
	config.ConnConfig.Database = name
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		admin.Exec(ctx, "DROP DATABASE "+pgx.Identifier{name}.Sanitize())
		admin.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		pool.Close()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if _, err := admin.Exec(ctx, "DROP DATABASE "+pgx.Identifier{name}.Sanitize()); err != nil {
			t.Error(err)
		}
		admin.Close()
	})
	if err := (&metadata.Store{Pool: pool}).Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	catalog := &Store{Pool: pool}
	if err := catalog.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err := catalog.Migrate(ctx); err != nil {
		t.Fatal("migration not idempotent", err)
	}
	return catalog
}

func TestHistoryIsolationIntervalsDeduplicationAndRetention(t *testing.T) {
	s := testStore(t)
	ctx := t.Context()
	meta := &metadata.Store{Pool: s.Pool}
	b := metadata.Sandbox{Tenant: "tenant", ExternalID: "sandbox", ActorAtespace: "space", ActorName: "actor", ActorUID: "uid", TemplateAlias: "python", TimeoutSeconds: 300}
	r := metadata.Request{Tenant: b.Tenant, ExternalID: b.ExternalID, ID: "create", Kind: "create", Digest: strings.Repeat("a", 64)}
	if _, err := meta.ReserveCreate(ctx, b, r); err != nil {
		t.Fatal(err)
	}
	if err := meta.BindActor(ctx, b.Tenant, b.ExternalID, b.ActorUID); err != nil {
		t.Fatal(err)
	}
	sample := &pb.GetActorGuestMetricsResponse{ActorUid: b.ActorUID, Assignment: &pb.WorkerAssignment{ExecutorInstanceId: "executor", AssignmentGeneration: 1}, ObservedAtUnixMillis: 2500, CpuCount: 2, CpuUsedPercent: 12.5, MemoryUsedBytes: 10, MemoryTotalBytes: 100, DiskUsedBytes: 50, DiskTotalBytes: 200}
	if err := s.Append(ctx, b, sample); err != nil {
		t.Fatal(err)
	}
	if err := s.Append(ctx, b, sample); err != nil {
		t.Fatal(err)
	}
	sample.ObservedAtUnixMillis = 1500
	if err := s.Append(ctx, b, sample); err != nil {
		t.Fatal(err)
	}
	all, err := s.History(ctx, b, nil, nil)
	if err != nil || len(all) != 2 || all[0].TimestampUnix != 1 || all[1].TimestampUnix != 2 || all[0].CPUCount != 2 || all[0].CPUUsedPct != 12.5 {
		t.Fatal("history ordering/dedup/units", all, err)
	}
	bound := int64(2)
	limited, err := s.History(ctx, b, &bound, &bound)
	if err != nil || len(limited) != 1 || limited[0].TimestampUnix != 2 {
		t.Fatal("inclusive interval", limited, err)
	}
	other := b
	other.Tenant = "other"
	isolated, err := s.History(ctx, other, nil, nil)
	if err != nil || len(isolated) != 0 {
		t.Fatal("tenant history leaked", err)
	}
	other = b
	other.ActorUID = "old"
	sample.ActorUid = "old"
	sample.ObservedAtUnixMillis = 3500
	if err = s.Append(ctx, other, sample); err != nil {
		t.Fatal(err)
	}
	isolated, err = s.History(ctx, other, nil, nil)
	if err != nil || len(isolated) != 0 {
		t.Fatal("old incarnation was persisted", err)
	}
	candidates, err := s.Candidates(ctx, "", "")
	if err != nil || len(candidates) != 1 || candidates[0].ActorUID != b.ActorUID {
		t.Fatal("sampling candidates", candidates, err)
	}
	candidates, err = s.Candidates(ctx, b.Tenant, b.ExternalID)
	if err != nil || len(candidates) != 0 {
		t.Fatal("cursor repeated row", err)
	}
	if _, err = s.Pool.Exec(ctx, `UPDATE aenv_bridge.guest_metrics SET collected_at=clock_timestamp()-interval '2 hours'`); err != nil {
		t.Fatal(err)
	}
	all, err = s.History(ctx, b, nil, nil)
	if err != nil || len(all) != 0 {
		t.Fatal("expired samples visible", err)
	}
	if err = s.Prune(ctx); err != nil {
		t.Fatal(err)
	}
	var count int
	if err = s.Pool.QueryRow(ctx, `SELECT count(*) FROM aenv_bridge.guest_metrics`).Scan(&count); err != nil || count != 0 {
		t.Fatal("retention did not reclaim", err)
	}
}

func TestLatestMetricsRequiresCurrentAssignmentAndLiveSandbox(t *testing.T) {
	s := testStore(t)
	ctx := t.Context()
	meta := &metadata.Store{Pool: s.Pool}
	b := metadata.Sandbox{Tenant: "tenant", ExternalID: "sandbox", ActorAtespace: "space", ActorName: "actor", ActorUID: "uid", TemplateAlias: "python", TimeoutSeconds: 300}
	r := metadata.Request{Tenant: b.Tenant, ExternalID: b.ExternalID, ID: "create", Kind: "create", Digest: strings.Repeat("a", 64)}
	if _, err := meta.ReserveCreate(ctx, b, r); err != nil {
		t.Fatal(err)
	}
	if err := meta.BindActor(ctx, b.Tenant, b.ExternalID, b.ActorUID); err != nil {
		t.Fatal(err)
	}
	a := &pb.WorkerAssignment{ExecutorInstanceId: "current", AssignmentGeneration: 2}
	response := &pb.GetActorGuestMetricsResponse{ActorUid: b.ActorUID, Assignment: a, ObservedAtUnixMillis: 2500, CpuCount: 1, MemoryTotalBytes: 100, DiskTotalBytes: 200}
	if err := s.Append(ctx, b, response); err != nil {
		t.Fatal(err)
	}
	got, err := s.Latest(ctx, b, a)
	if err != nil || got == nil || got.TimestampUnix != 2 {
		t.Fatal(got, err)
	}
	if got, err = s.Latest(ctx, b, &pb.WorkerAssignment{ExecutorInstanceId: "current", AssignmentGeneration: 3}); err != nil || got != nil {
		t.Fatal("old generation returned", got, err)
	}
	if got, err = s.Latest(ctx, b, &pb.WorkerAssignment{ExecutorInstanceId: "old", AssignmentGeneration: 2}); err != nil || got != nil {
		t.Fatal("old process returned", got, err)
	}
	if _, err = s.Pool.Exec(ctx, `UPDATE aenv_bridge.sandboxes SET deleted=true`); err != nil {
		t.Fatal(err)
	}
	if got, err = s.Latest(ctx, b, a); err != nil || got != nil {
		t.Fatal("deleted sandbox returned", got, err)
	}
}

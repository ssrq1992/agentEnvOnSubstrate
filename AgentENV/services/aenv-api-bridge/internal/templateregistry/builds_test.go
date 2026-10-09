package templateregistry

import (
	"errors"
	"sync"
	"testing"
	"time"

	"agentenv/services/aenv-api-bridge/internal/metadata"
)

func TestBuildReservationAndFencedTransitions(t *testing.T) {
	s := testRegistry(t)
	ctx := t.Context()
	first, err := s.ReserveBuild(ctx, "tenant", "build", "request", []byte(`{"size":9007199254740993,"image":"sha256:aaa"}`))
	if err != nil {
		t.Fatal(err)
	}
	duplicate, err := s.ReserveBuild(ctx, "tenant", "other-id", "request", []byte(`{"image":"sha256:aaa", "size":9007199254740993}`))
	if err != nil || duplicate.ID != first.ID {
		t.Fatal(duplicate, err)
	}
	if _, err = s.ReserveBuild(ctx, "tenant", "x", "request", []byte(`{"size":9007199254740992,"image":"sha256:aaa"}`)); !errors.Is(err, metadata.ErrConflict) {
		t.Fatal("input mutation accepted", err)
	}
	if _, err = s.Build(ctx, "other", "build"); !errors.Is(err, metadata.ErrNotFound) {
		t.Fatal("tenant leak", err)
	}
	b, err := s.ClaimBuild(ctx, "executor-one", time.Minute)
	if err != nil || b.State != "allocating" || b.Generation != 1 {
		t.Fatal(b, err)
	}
	if _, err = s.ClaimBuild(ctx, "executor-two", time.Minute); !errors.Is(err, metadata.ErrNotFound) {
		t.Fatal("duplicate dispatch", err)
	}
	if _, err = s.AdvanceBuild(ctx, b, "ready", "actor", time.Minute); err == nil {
		t.Fatal("skipped execution accepted")
	}
	forged := b
	forged.Generation++
	if _, err = s.AdvanceBuild(ctx, forged, "executing", "actor", time.Minute); !errors.Is(err, metadata.ErrConflict) {
		t.Fatal("stale generation accepted", err)
	}
	b, err = s.AdvanceBuild(ctx, b, "executing", "actor", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.AdvanceBuild(ctx, b, "capturing", "other-actor", time.Minute); !errors.Is(err, metadata.ErrConflict) {
		t.Fatal("Actor identity changed", err)
	}
	for _, next := range []string{"capturing", "publishing", "ready"} {
		b, err = s.AdvanceBuild(ctx, b, next, "", time.Minute)
		if err != nil {
			t.Fatal(err)
		}
	}
	if _, err = s.AdvanceBuild(ctx, b, "executing", "", time.Minute); err == nil {
		t.Fatal("terminal job replayed")
	}
}

func TestExpiredBuildIsUncertainAndCancellationDoesNotInventStop(t *testing.T) {
	s := testRegistry(t)
	ctx := t.Context()
	if _, err := s.ReserveBuild(ctx, "tenant", "active", "a", []byte(`{}`)); err != nil {
		t.Fatal(err)
	}
	b, err := s.ClaimBuild(ctx, "owner", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	b, err = s.AdvanceBuild(ctx, b, "executing", "actor", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	cancelled, err := s.RequestBuildCancellation(ctx, "tenant", b.ID)
	if err != nil || cancelled.State != "executing" || !cancelled.CancelRequested {
		t.Fatal(cancelled, err)
	}
	if _, err = s.AdvanceBuild(ctx, b, "capturing", "", time.Minute); !errors.Is(err, metadata.ErrConflict) {
		t.Fatal("cancelled build advanced normally", err)
	}
	if _, err = s.Pool.Exec(ctx, `UPDATE aenv_bridge.template_build_jobs SET lease_until=clock_timestamp()-interval '1 second'`); err != nil {
		t.Fatal(err)
	}
	if _, err = s.AdvanceBuild(ctx, b, "capturing", "", time.Minute); !errors.Is(err, metadata.ErrConflict) {
		t.Fatal("expired executor accepted", err)
	}
	if n, err := s.ExpireBuildLeases(ctx); err != nil || n != 1 {
		t.Fatal(n, err)
	}
	if _, err = s.ClaimBuild(ctx, "new-owner", time.Minute); !errors.Is(err, metadata.ErrNotFound) {
		t.Fatal("unknown execution redispatched", err)
	}
	current, err := s.Build(ctx, "tenant", b.ID)
	if err != nil || current.State != "uncertain" || current.Actor != "actor" {
		t.Fatal(current, err)
	}
	if _, err = s.ReserveBuild(ctx, "tenant", "queued", "q", []byte(`{}`)); err != nil {
		t.Fatal(err)
	}
	current, err = s.RequestBuildCancellation(ctx, "tenant", "queued")
	if err != nil || current.State != "cancelled" {
		t.Fatal(current, err)
	}
}

func TestBuildLogsSurviveRestartDeduplicateAndSerialize(t *testing.T) {
	s := testRegistry(t)
	ctx := t.Context()
	if _, err := s.ReserveBuild(ctx, "tenant", "build", "r", []byte(`{}`)); err != nil {
		t.Fatal(err)
	}
	b, err := s.ClaimBuild(ctx, "owner", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for range 12 {
		wg.Go(func() {
			if log, err := s.AppendBuildLog(ctx, b, "event", "hello"); err != nil || log.Sequence != 1 {
				t.Error(log, err)
			}
		})
	}
	wg.Wait()
	restarted := &Store{Pool: s.Pool}
	if logs, err := restarted.BuildLogs(ctx, "tenant", "build", 0, 10); err != nil || len(logs) != 1 || logs[0].Message != "hello" {
		t.Fatal(logs, err)
	}
	if _, err := s.AppendBuildLog(ctx, b, "event", "different"); !errors.Is(err, metadata.ErrConflict) {
		t.Fatal("duplicate event changed", err)
	}
	stale := b
	stale.Generation++
	if _, err := s.AppendBuildLog(ctx, stale, "late", "late"); !errors.Is(err, metadata.ErrConflict) {
		t.Fatal("stale output accepted", err)
	}
	if _, err := s.BuildLogs(ctx, "other", "build", 0, 10); !errors.Is(err, metadata.ErrNotFound) {
		t.Fatal("logs crossed tenant", err)
	}
	if logs, err := s.BuildLogs(ctx, "tenant", "build", 1, 10); err != nil || len(logs) != 0 {
		t.Fatal(logs, err)
	}
}

func TestBuildRejectsCorruptPayloadAndInvalidJSON(t *testing.T) {
	s := testRegistry(t)
	ctx := t.Context()
	for _, payload := range []string{`[]`, `null`, `{} {}`, `{"x":`} {
		if _, err := s.ReserveBuild(ctx, "tenant", "bad", "bad", []byte(payload)); err == nil {
			t.Fatal("invalid input accepted", payload)
		}
	}
	if _, err := s.ReserveBuild(ctx, "tenant", "build", "r", []byte(`{"numeric":1e3}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Pool.Exec(ctx, `UPDATE aenv_bridge.template_build_jobs SET payload='{"numeric":2000}'`); err != nil {
		t.Fatal(err)
	}
	if b, err := s.Build(ctx, "tenant", "build"); err == nil || b.ID != "" {
		t.Fatal("corrupt build returned", b, err)
	}
}

func TestConcurrentBuildClaimsDispatchExactlyOnce(t *testing.T) {
	s := testRegistry(t)
	ctx := t.Context()
	if _, err := s.ReserveBuild(ctx, "tenant", "build", "r", []byte(`{}`)); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	var mu sync.Mutex
	claimed := 0
	for range 16 {
		wg.Go(func() {
			_, err := s.ClaimBuild(ctx, "worker", time.Minute)
			if err == nil {
				mu.Lock()
				claimed++
				mu.Unlock()
			} else if !errors.Is(err, metadata.ErrNotFound) {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	if claimed != 1 {
		t.Fatal("claim count", claimed)
	}
}

func TestBuildLeaseRenewalCannotReviveExpiredExecutor(t *testing.T) {
	s := testRegistry(t)
	ctx := t.Context()
	if _, err := s.ReserveBuild(ctx, "tenant", "build", "r", []byte(`{}`)); err != nil {
		t.Fatal(err)
	}
	b, err := s.ClaimBuild(ctx, "worker", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	renewed, err := s.RenewBuild(ctx, b, 2*time.Minute)
	if err != nil || !renewed.LeaseUntil.After(*b.LeaseUntil) {
		t.Fatal(renewed, err)
	}
	if _, err = s.Pool.Exec(ctx, `UPDATE aenv_bridge.template_build_jobs SET lease_until=clock_timestamp()-interval '1 second'`); err != nil {
		t.Fatal(err)
	}
	if _, err = s.RenewBuild(ctx, b, time.Minute); !errors.Is(err, metadata.ErrConflict) {
		t.Fatal("expired lease revived", err)
	}
}

func TestBuildMigrationRejectsModifiedDefinition(t *testing.T) {
	s := testRegistry(t)
	if _, err := s.Pool.Exec(t.Context(), `UPDATE aenv_bridge.template_build_schema SET digest='modified'`); err != nil {
		t.Fatal(err)
	}
	if err := s.Migrate(t.Context()); err == nil {
		t.Fatal("modified build schema accepted")
	}
}

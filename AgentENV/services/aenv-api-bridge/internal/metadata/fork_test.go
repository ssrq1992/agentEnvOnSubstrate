package metadata

import (
	"errors"
	"strings"
	"testing"
)

func TestForkDurableCaptureAndIndependentOutcomes(t *testing.T) {
	s := testStore(t)
	ctx := t.Context()
	b := Sandbox{Tenant: "tenant", ExternalID: "parent", ActorAtespace: "space", ActorName: "source", TemplateAlias: "template", TimeoutSeconds: 300}
	create := Request{Tenant: b.Tenant, ExternalID: b.ExternalID, ID: "create", Kind: "create", Digest: strings.Repeat("a", 64)}
	if _, err := s.ReserveCreate(ctx, b, create); err != nil {
		t.Fatal(err)
	}
	if err := s.BindActor(ctx, b.Tenant, b.ExternalID, "source-uid"); err != nil {
		t.Fatal(err)
	}
	b.ActorUID = "source-uid"
	r := Request{Tenant: b.Tenant, ExternalID: b.ExternalID, ID: "fork", Kind: "fork", Digest: strings.Repeat("b", 64)}
	i, err := s.ReserveFork(ctx, b, r, []byte("frozen"))
	if err != nil {
		t.Fatal(err)
	}
	replay, err := s.ReserveFork(ctx, b, r, []byte("changed"))
	if err != nil || string(replay.Prepared) != "frozen" {
		t.Fatal("intent changed", err)
	}
	if _, err := s.Pool.Exec(ctx, `UPDATE aenv_bridge.sandboxes SET expires_at=clock_timestamp()-interval '1 hour'`); err != nil {
		t.Fatal(err)
	}
	if claims, err := s.ClaimExpired(ctx, 10); err != nil || len(claims) != 0 {
		t.Fatal("capture source expired", err)
	}

	if _, err := s.ReserveSuspend(ctx, b, 7); !errors.Is(err, ErrConflict) {
		t.Fatal("capture source paused", err)
	}
	jobs, err := s.ClaimForks(ctx, 10)
	if err != nil || len(jobs) != 1 {
		t.Fatal("job not claimable", err)
	}
	if jobs, err := s.ClaimForks(ctx, 10); err != nil || len(jobs) != 0 {
		t.Fatal("claimed job not deferred", err)
	}
	if err := s.ConfirmForkCaptured(ctx, i, []byte("snapshot")); err != nil {
		t.Fatal(err)
	}
	if err := s.ConfirmForkCaptured(ctx, i, []byte("different")); !errors.Is(err, ErrConflict) {
		t.Fatal("snapshot replaced", err)
	}
	current, err := s.Fork(ctx, r)
	if err != nil || !current.Captured || string(current.Snapshot) != "snapshot" {
		t.Fatal("capture receipt lost", err)
	}
	for _, child := range []ForkChild{{Index: 0, ID: "successful"}, {Index: 1, ID: "failed", Failed: true}} {
		if err := s.RecordForkChild(ctx, i, child); err != nil {
			t.Fatal(err)
		}
		if err := s.RecordForkChild(ctx, i, child); err != nil {
			t.Fatal("repeat outcome", err)
		}
	}
	if err := s.RecordForkChild(ctx, i, ForkChild{Index: 1, ID: "failed", Failed: false}); !errors.Is(err, ErrConflict) {
		t.Fatal("outcome changed", err)
	}
	results, err := s.ForkChildren(ctx, i)
	if err != nil || len(results) != 2 || !results[1].Failed {
		t.Fatal("outcomes lost", err)
	}
	if err := s.Complete(ctx, r, false, []byte("receipt")); err != nil {
		t.Fatal(err)
	}
	current, err = s.Fork(ctx, r)
	if err != nil || current.Request.State != "completed" || string(current.Request.Result) != "receipt" {
		t.Fatal("completion lost", err)
	}
	r.Digest = strings.Repeat("d", 64)
	if _, err := s.Fork(ctx, r); !errors.Is(err, ErrConflict) {
		t.Fatal("payload mismatch", err)
	}
	if claims, err := s.ClaimExpired(ctx, 10); err != nil || len(claims) != 1 {
		t.Fatal("published capture still holds expiry", err)
	}
}
func TestSnapshotFailureStopsCreationReconciliation(t *testing.T) {
	s := testStore(t)
	ctx := t.Context()
	b := Sandbox{Tenant: "tenant", ExternalID: "child", ActorAtespace: "space", ActorName: "child", TemplateAlias: "template", TimeoutSeconds: 300}
	r := Request{Tenant: b.Tenant, ExternalID: b.ExternalID, ID: "create-child", Kind: "create", Digest: strings.Repeat("e", 64)}
	_, err := s.ReserveCreation(ctx, b, r, []byte(`{"SourceTagUID":"tag-uid"}`))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.RejectSnapshotCreation(ctx, b, "tag-uid"); !errors.Is(err, ErrConflict) {
		t.Fatal("unknown UID cleaned", err)
	}
	if err := s.BindActor(ctx, b.Tenant, b.ExternalID, "child-uid"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RejectSnapshotCreation(ctx, b, "wrong-tag"); !errors.Is(err, ErrConflict) {
		t.Fatal("other operation child rejected", err)
	}
	failed, err := s.RejectSnapshotCreation(ctx, b, "tag-uid")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Creation(ctx, b.Tenant, b.ExternalID); !errors.Is(err, ErrConflict) {
		t.Fatal("rejected child runnable", err)
	}
	if jobs, err := s.ClaimCreations(ctx, 10); err != nil || len(jobs) != 0 {
		t.Fatal("rejected child claimed", err)
	}
	if _, err := s.Get(ctx, b.Tenant, b.ExternalID); err != nil {
		t.Fatal("unacknowledged deletion tombstoned", err)
	}
	if err := s.ConfirmSnapshotCreationFailed(ctx, failed); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get(ctx, b.Tenant, b.ExternalID); !errors.Is(err, ErrNotFound) {
		t.Fatal("failed child exposed", err)
	}
	if _, err := s.RejectSnapshotCreation(ctx, b, "tag-uid"); err != nil {
		t.Fatal("cleanup retry lost identity", err)
	}
}

func TestForkRecognizesConfirmedChildAfterDeletion(t *testing.T) {
	s := testStore(t)
	ctx := t.Context()
	b := Sandbox{Tenant: "tenant", ExternalID: "expired", ActorAtespace: "space", ActorName: "actor", TemplateAlias: "template", TimeoutSeconds: 0}
	r := Request{Tenant: b.Tenant, ExternalID: b.ExternalID, ID: "created", Kind: "create", Digest: strings.Repeat("f", 64)}
	i, err := s.ReserveCreation(ctx, b, r, []byte(`{"SourceTagUID":"tag-uid"}`))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.BindActor(ctx, b.Tenant, b.ExternalID, "uid"); err != nil {
		t.Fatal(err)
	}
	if err := s.ConfirmCreated(ctx, i, "uid", Profile{TemplateID: "template", EnvdVersion: "envd", CPUCount: 1, MemoryMB: 128, DiskSizeMB: 1024}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Pool.Exec(ctx, `UPDATE aenv_bridge.sandboxes SET deleted=true WHERE tenant=$1 AND external_id=$2`, b.Tenant, b.ExternalID); err != nil {
		t.Fatal(err)
	}
	if confirmed, err := s.SnapshotCreationConfirmed(ctx, b, "tag-uid"); err != nil || !confirmed {
		t.Fatal("confirmed expired child lost", err)
	}
	if _, err := s.SnapshotCreationConfirmed(ctx, b, "other-tag"); !errors.Is(err, ErrConflict) {
		t.Fatal("another operation acknowledged", err)
	}
}

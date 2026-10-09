package metadata

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestCaptureHoldsLifecycleAndPublishesSnapshotAtomically(t *testing.T) {
	s := testStore(t)
	ctx := t.Context()
	seedSuspendSandbox(t, s, "source")
	b, err := s.Get(ctx, "tenant", "source")
	if err != nil {
		t.Fatal(err)
	}
	r := Request{Tenant: b.Tenant, ExternalID: b.ExternalID, ID: "capture", Kind: "capture", Digest: strings.Repeat("a", 64)}
	first, err := s.ReserveCapture(ctx, b, r, []byte(`{"source":"first"}`), "named")
	if err != nil {
		t.Fatal(err)
	}
	retry, err := s.ReserveCapture(ctx, b, r, []byte(`{"source":"changed"}`), "named")
	if err != nil || string(retry.Prepared) != string(first.Prepared) {
		t.Fatal("capture changed after retry", retry, err)
	}
	if claims, err := s.ClaimExpired(ctx, 10); err != nil || len(claims) != 0 {
		t.Fatal("capture expired", claims, err)
	}
	if _, err = s.ReserveSuspend(ctx, b, 2); !errors.Is(err, ErrConflict) {
		t.Fatal("pause raced capture", err)
	}
	deleteRequest := r
	deleteRequest.ID = "delete"
	deleteRequest.Kind = "delete"
	if _, err = s.Reserve(ctx, deleteRequest); !errors.Is(err, ErrConflict) {
		t.Fatal("capture parent deleted", err)
	}
	ext := r
	ext.ID = "extension"
	ext.Kind = "extension"
	if _, err = s.ReserveExtension(ctx, b, ext, []byte(`{}`)); !errors.Is(err, ErrConflict) {
		t.Fatal("extension raced capture", err)
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	id := "11111111-1111-4111-8111-111111111111"
	wire, _ := json.Marshal(map[string]any{"Tenant": "tenant", "Source": map[string]string{"ExternalID": "source"}, "Info": map[string]any{"snapshotID": id, "createdAt": now}})
	for range 2 {
		if err = s.ConfirmCapture(ctx, first, id, "tag-uid", wire, now); err != nil {
			t.Fatal(err)
		}
	}
	committed, err := s.Capture(ctx, r)
	if err != nil || committed.Request.State != "completed" {
		t.Fatal(committed, err)
	}
	for _, ref := range []string{id, "named"} {
		if record, err := s.Snapshot(ctx, "tenant", ref); err != nil || len(record) == 0 {
			t.Fatal(ref, err)
		}
	}
	if _, err = s.Snapshot(ctx, "other", id); !errors.Is(err, ErrNotFound) {
		t.Fatal("snapshot leaked tenant", err)
	}
	if page, err := s.SnapshotPageFiltered(ctx, "tenant", nil, "", 10, "source", "named"); err != nil || len(page) != 1 {
		t.Fatal(page, err)
	}
	if page, err := s.SnapshotPageFiltered(ctx, "tenant", nil, "", 10, "other", "named"); err != nil || len(page) != 0 {
		t.Fatal("source filter ignored", page, err)
	}
	if claims, err := s.ClaimCaptures(ctx, 10); err != nil || len(claims) != 0 {
		t.Fatal("completed capture replayed", claims, err)
	}
	if _, err = s.Reserve(ctx, deleteRequest); err != nil {
		t.Fatal("completed capture kept delete hold", err)
	}
}
func TestCaptureAliasCollisionDoesNotCommitPendingHold(t *testing.T) {
	s := testStore(t)
	ctx := t.Context()
	seedSuspendSandbox(t, s, "one")
	seedSuspendSandbox(t, s, "two")
	one, err := s.Get(ctx, "tenant", "one")
	if err != nil {
		t.Fatal(err)
	}
	two, err := s.Get(ctx, "tenant", "two")
	if err != nil {
		t.Fatal(err)
	}
	r := Request{Tenant: "tenant", ExternalID: "one", ID: "capture-one", Kind: "capture", Digest: strings.Repeat("a", 64)}
	if _, err = s.ReserveCapture(ctx, one, r, []byte(`{}`), "same"); err != nil {
		t.Fatal(err)
	}
	second := r
	second.ExternalID = "two"
	second.ID = "capture-two"
	if _, err = s.ReserveCapture(ctx, two, second, []byte(`{}`), "same"); !errors.Is(err, ErrConflict) {
		t.Fatal("alias collision accepted", err)
	}
	if _, err = s.Capture(ctx, second); !errors.Is(err, ErrNotFound) {
		t.Fatal("failed alias reserved capture hold", err)
	}
	claims, err := s.ClaimCaptures(ctx, 10)
	if err != nil || len(claims) != 1 || claims[0].Request.ID != r.ID {
		t.Fatal(claims, err)
	}
}
func TestCaptureMigrationUpgradesVersionEight(t *testing.T) {
	s := testStore(t)
	if _, err := s.Pool.Exec(t.Context(), `DROP TABLE aenv_bridge.snapshot_names; DROP TABLE aenv_bridge.snapshot_records; DROP TABLE aenv_bridge.capture_jobs; DELETE FROM aenv_bridge.metadata_schema WHERE version=9`); err != nil {
		t.Fatal(err)
	}
	if err := s.Migrate(t.Context()); err != nil {
		t.Fatal(err)
	}
	var version int
	if err := s.Pool.QueryRow(t.Context(), `SELECT max(version) FROM aenv_bridge.metadata_schema`).Scan(&version); err != nil || version != 9 {
		t.Fatal(version, err)
	}
}

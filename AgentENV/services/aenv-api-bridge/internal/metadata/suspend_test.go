package metadata

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

func seedSuspendSandbox(t *testing.T, s *Store, id string) Sandbox {
	t.Helper()
	b := Sandbox{Tenant: "tenant", ExternalID: id, ActorAtespace: "space", ActorName: id, ActorUID: "uid-" + id, TemplateAlias: "python", TimeoutSeconds: 300}
	r := Request{Tenant: b.Tenant, ExternalID: id, ID: "create-" + id, Kind: "create", Digest: strings.Repeat("a", 64)}
	if _, err := s.ReserveCreate(t.Context(), b, r); err != nil {
		t.Fatal(err)
	}
	if err := s.BindActor(t.Context(), b.Tenant, b.ExternalID, b.ActorUID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Pool.Exec(t.Context(), `UPDATE aenv_bridge.sandboxes SET expires_at=clock_timestamp()-interval '1 second' WHERE tenant=$1 AND external_id=$2`, b.Tenant, id); err != nil {
		t.Fatal(err)
	}
	return b
}
func TestSuspendHoldsExpiryAndPersistsRetries(t *testing.T) {
	s := testStore(t)
	b := seedSuspendSandbox(t, s, "sandbox")
	ctx := t.Context()
	first, err := s.ReserveSuspend(ctx, b, 7)
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.ReserveSuspend(ctx, b, 7)
	if err != nil || second.Request.ID != first.Request.ID || second.Generation != 7 {
		t.Fatal("pause retry lost allocation", err)
	}
	if claimed, err := s.ClaimExpired(ctx, 10); err != nil || len(claimed) != 0 {
		t.Fatal("pause can be expired", claimed, err)
	}
	if err = s.Extend(ctx, b.Tenant, b.ExternalID, 1, 300); !errors.Is(err, ErrConflict) {
		t.Fatal("timer reactivated during pause", err)
	}
	if _, err = s.ReserveSuspend(ctx, b, 8); !errors.Is(err, ErrConflict) {
		t.Fatal("new generation joined old intent", err)
	}
	if _, err = s.Pool.Exec(ctx, `UPDATE aenv_bridge.suspend_intents SET retry_after=clock_timestamp()-interval '1 second'`); err != nil {
		t.Fatal(err)
	}
	recovered, err := s.ClaimSuspends(ctx, 10)
	if err != nil || len(recovered) != 1 || recovered[0].Generation != 7 || recovered[0].Request.ID != first.Request.ID {
		t.Fatal("restart lost pending suspend", recovered, err)
	}
	if err = s.ConfirmSuspended(ctx, recovered[0]); err != nil {
		t.Fatal(err)
	}
	if claims, err := s.ClaimSuspends(ctx, 10); err != nil || len(claims) != 0 {
		t.Fatal("completed pause retried", err)
	}
	already, err := s.ReserveSuspend(ctx, b, 0)
	if err != nil || already.Request.State != "completed" || already.Generation != 7 {
		t.Fatal("already paused did not join completed intent", err)
	}
	if claims, err := s.ClaimExpired(ctx, 10); err != nil || len(claims) != 0 {
		t.Fatal("completed persistent pause expired", err)
	}
	// Simulate a subsequent resume committing removal of the timer hold.
	if _, err = s.Pool.Exec(ctx, `DELETE FROM aenv_bridge.suspend_intents`); err != nil {
		t.Fatal(err)
	}
	if err = s.ConfirmSuspended(ctx, first); !errors.Is(err, ErrConflict) {
		t.Fatal("late completion reinstated a timer hold", err)
	}
}
func TestExpiryDecisionPreventsPauseReservation(t *testing.T) {
	s := testStore(t)
	b := seedSuspendSandbox(t, s, "sandbox")
	if claims, err := s.ClaimExpired(t.Context(), 10); err != nil || len(claims) != 1 {
		t.Fatal("expiry not claimed", err)
	}
	if _, err := s.ReserveSuspend(t.Context(), b, 7); !errors.Is(err, ErrConflict) {
		t.Fatal("pause raced past committed deletion", err)
	}
}
func TestSuspendMigrationUpgradesVersionOne(t *testing.T) {
	s := testStore(t)
	b := seedSuspendSandbox(t, s, "sandbox")
	if _, err := s.Pool.Exec(t.Context(), `DROP TABLE aenv_bridge.snapshot_names; DROP TABLE aenv_bridge.snapshot_records; DROP TABLE aenv_bridge.capture_jobs; DROP TABLE aenv_bridge.extension_jobs; DROP TABLE aenv_bridge.fork_children; DROP TABLE aenv_bridge.fork_jobs; DROP TABLE aenv_bridge.creation_jobs; DROP TABLE aenv_bridge.sandbox_profiles; DROP INDEX aenv_bridge.sandboxes_external_lookup; DROP TABLE aenv_bridge.sandbox_access; DROP TABLE aenv_bridge.resume_intents; DROP TABLE aenv_bridge.suspend_intents; DELETE FROM aenv_bridge.metadata_schema WHERE version>=2; ALTER TABLE aenv_bridge.sandboxes DROP CONSTRAINT sandboxes_timeout_seconds_check; ALTER TABLE aenv_bridge.sandboxes ALTER COLUMN timeout_seconds TYPE integer; ALTER TABLE aenv_bridge.sandboxes ADD CONSTRAINT sandboxes_timeout_seconds_check CHECK(timeout_seconds>0 AND timeout_seconds<=86400)`); err != nil {
		t.Fatal(err)
	}
	if err := s.Migrate(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := s.Migrate(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReserveSuspend(t.Context(), b, 7); err != nil {
		t.Fatal("upgraded store unusable", err)
	}
}

func TestSuspendAndExpiryRaceHasOneWinner(t *testing.T) {
	s := testStore(t)
	for n := 0; n < 20; n++ {
		id := fmt.Sprintf("race-%d", n)
		b := seedSuspendSandbox(t, s, id)
		ready := make(chan struct{})
		paused := make(chan error, 1)
		expired := make(chan []Expiry, 1)
		errs := make(chan error, 1)
		go func() { <-ready; _, err := s.ReserveSuspend(t.Context(), b, 7); paused <- err }()
		go func() { <-ready; claims, err := s.ClaimExpired(t.Context(), 1000); expired <- claims; errs <- err }()
		close(ready)
		pauseErr := <-paused
		claims := <-expired
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
		expiryWon := false
		for _, claim := range claims {
			if claim.Sandbox.ExternalID == id {
				expiryWon = true
			}
		}
		if pauseErr == nil && expiryWon {
			t.Fatal("pause and expiry both committed")
		}
		if pauseErr != nil && (!errors.Is(pauseErr, ErrConflict) || !expiryWon) {
			t.Fatal("neither operation won", pauseErr)
		}
	}
}

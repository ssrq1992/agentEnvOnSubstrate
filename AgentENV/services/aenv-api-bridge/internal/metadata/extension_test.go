package metadata

import (
	"errors"
	"strings"
	"testing"
)

func extensionSandbox(t *testing.T, s *Store) Sandbox {
	t.Helper()
	b := Sandbox{Tenant: "tenant", ExternalID: "sandbox", ActorAtespace: "space", ActorName: "actor", TemplateAlias: "template", TimeoutSeconds: 300}
	r := Request{Tenant: b.Tenant, ExternalID: b.ExternalID, ID: "create", Kind: "create", Digest: strings.Repeat("a", 64)}
	if _, err := s.ReserveCreate(t.Context(), b, r); err != nil {
		t.Fatal(err)
	}
	if err := s.BindActor(t.Context(), b.Tenant, b.ExternalID, "actor-uid"); err != nil {
		t.Fatal(err)
	}
	b.ActorUID = "actor-uid"
	return b
}
func TestExtensionFrozenIntentHoldsExpiryAndLifecycle(t *testing.T) {
	s := testStore(t)
	b := extensionSandbox(t, s)
	r := Request{Tenant: b.Tenant, ExternalID: b.ExternalID, ID: "extension", Kind: "extension", Digest: strings.Repeat("b", 64)}
	i, err := s.ReserveExtension(t.Context(), b, r, []byte("frozen"))
	if err != nil {
		t.Fatal(err)
	}
	replay, err := s.ReserveExtension(t.Context(), b, r, []byte("changed"))
	if err != nil || string(replay.Prepared) != "frozen" {
		t.Fatal("preparation changed", err)
	}
	if _, err = s.Pool.Exec(t.Context(), `UPDATE aenv_bridge.sandboxes SET expires_at=clock_timestamp()-interval '1 hour'`); err != nil {
		t.Fatal(err)
	}
	if claims, err := s.ClaimExpired(t.Context(), 10); err != nil || len(claims) != 0 {
		t.Fatal("pending extension expired", err)
	}
	if _, err = s.ReserveSuspend(t.Context(), b, 7); !errors.Is(err, ErrConflict) {
		t.Fatal("pending extension paused", err)
	}
	fork := Request{Tenant: b.Tenant, ExternalID: b.ExternalID, ID: "fork", Kind: "fork", Digest: strings.Repeat("c", 64)}
	if _, err = s.ReserveFork(t.Context(), b, fork, []byte("capture")); !errors.Is(err, ErrConflict) {
		t.Fatal("pending extension captured", err)
	}
	other := r
	other.ID = "another"
	if _, err = s.ReserveExtension(t.Context(), b, other, []byte("another")); !errors.Is(err, ErrConflict) {
		t.Fatal("concurrent update reserved", err)
	}
	claims, err := s.ClaimExtensions(t.Context(), 10)
	if err != nil || len(claims) != 1 || string(claims[0].Prepared) != string(i.Prepared) {
		t.Fatal("frozen request lost", err)
	}
	if claims, err = s.ClaimExtensions(t.Context(), 10); err != nil || len(claims) != 0 {
		t.Fatal("claim retry not deferred", err)
	}
	if err = s.Complete(t.Context(), r, false, []byte(`{"approved":true}`)); err != nil {
		t.Fatal(err)
	}
	current, err := s.Extension(t.Context(), r)
	if err != nil || current.Request.State != "completed" || string(current.Request.Result) != `{"approved":true}` {
		t.Fatal("approval lost", err)
	}
	if claims, err := s.ClaimExpired(t.Context(), 10); err != nil || len(claims) != 1 {
		t.Fatal("completed update still holds expiry", err)
	}
	r.Digest = strings.Repeat("d", 64)
	if _, err = s.Extension(t.Context(), r); !errors.Is(err, ErrConflict) {
		t.Fatal("payload mismatch accepted", err)
	}
}
func TestExtensionReserveRespectsPriorSuspendOrFork(t *testing.T) {
	for _, kind := range []string{"suspend", "fork", "expiry"} {
		t.Run(kind, func(t *testing.T) {
			s := testStore(t)
			b := extensionSandbox(t, s)
			switch kind {
			case "suspend":
				if _, err := s.ReserveSuspend(t.Context(), b, 7); err != nil {
					t.Fatal(err)
				}
			case "fork":
				r := Request{Tenant: b.Tenant, ExternalID: b.ExternalID, ID: "fork", Kind: "fork", Digest: strings.Repeat("c", 64)}
				if _, err := s.ReserveFork(t.Context(), b, r, []byte("capture")); err != nil {
					t.Fatal(err)
				}
			case "expiry":
				if _, err := s.Pool.Exec(t.Context(), `UPDATE aenv_bridge.sandboxes SET expires_at=clock_timestamp()-interval '1 hour'`); err != nil {
					t.Fatal(err)
				}
				if claims, err := s.ClaimExpired(t.Context(), 10); err != nil || len(claims) != 1 {
					t.Fatal(err)
				}
			}
			r := Request{Tenant: b.Tenant, ExternalID: b.ExternalID, ID: "extension", Kind: "extension", Digest: strings.Repeat("b", 64)}
			if _, err := s.ReserveExtension(t.Context(), b, r, []byte("patch")); !errors.Is(err, ErrConflict) {
				t.Fatal("existing hold bypassed", err)
			}
		})
	}
}

func TestExtensionMigrationFromV7PreservesExistingSandboxes(t *testing.T) {
	s := testStore(t)
	b := extensionSandbox(t, s)
	if _, err := s.Pool.Exec(t.Context(), `DROP TABLE aenv_bridge.snapshot_names; DROP TABLE aenv_bridge.snapshot_records; DROP TABLE aenv_bridge.capture_jobs; DROP TABLE aenv_bridge.extension_jobs; DELETE FROM aenv_bridge.metadata_schema WHERE version>=8`); err != nil {
		t.Fatal(err)
	}
	if err := s.Migrate(t.Context()); err != nil {
		t.Fatal(err)
	}
	current, err := s.Get(t.Context(), b.Tenant, b.ExternalID)
	if err != nil || current.ActorUID != b.ActorUID || current.ActorName != b.ActorName {
		t.Fatal("v7 upgrade lost identity", err)
	}
	r := Request{Tenant: b.Tenant, ExternalID: b.ExternalID, ID: "new-extension", Kind: "extension", Digest: strings.Repeat("e", 64)}
	if _, err = s.ReserveExtension(t.Context(), current, r, []byte("new-job")); err != nil {
		t.Fatal("upgraded extension job unavailable", err)
	}
}

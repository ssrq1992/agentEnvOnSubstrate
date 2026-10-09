package metadata

import (
	"errors"
	"testing"
	"time"
)

func TestRefreshOnlyExtendsAndOrdersAgainstExpiry(t *testing.T) {
	s := testStore(t)
	ctx := t.Context()
	seedSuspendSandbox(t, s, "sandbox")
	b, err := s.Get(ctx, "tenant", "sandbox")
	if err != nil {
		t.Fatal(err)
	}
	if err = s.RefreshMinimum(ctx, b, 600); err != nil {
		t.Fatal(err)
	}
	before, err := s.Get(ctx, b.Tenant, b.ExternalID)
	if err != nil {
		t.Fatal(err)
	}
	if time.Until(before.ExpiresAt) < 590*time.Second || before.TimeoutSeconds != 600 {
		t.Fatal(before)
	}
	if err = s.RefreshMinimum(ctx, b, 1000); !errors.Is(err, ErrConflict) {
		t.Fatal("stale refresh accepted", err)
	}
	if err = s.RefreshMinimum(ctx, before, 15); err != nil {
		t.Fatal(err)
	}
	after, err := s.Get(ctx, b.Tenant, b.ExternalID)
	if err != nil || !after.ExpiresAt.Equal(before.ExpiresAt) || after.Revision != before.Revision || after.TimeoutSeconds != 600 {
		t.Fatal("short refresh changed timeout", after, err)
	}
	if _, err = s.Pool.Exec(ctx, `UPDATE aenv_bridge.sandboxes SET expires_at=clock_timestamp()-interval '1 second'`); err != nil {
		t.Fatal(err)
	}
	if claims, err := s.ClaimExpired(ctx, 1); err != nil || len(claims) != 1 {
		t.Fatal(claims, err)
	}
	if err = s.RefreshMinimum(ctx, after, 1000); !errors.Is(err, ErrConflict) {
		t.Fatal("expiry claim bypassed", err)
	}
}

func TestRefreshCannotReactivatePausedTimer(t *testing.T) {
	s := testStore(t)
	ctx := t.Context()
	seedSuspendSandbox(t, s, "sandbox")
	b, err := s.Get(ctx, "tenant", "sandbox")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.ReserveSuspend(ctx, b, 7); err != nil {
		t.Fatal(err)
	}
	if err = s.RefreshMinimum(ctx, b, 300); !errors.Is(err, ErrConflict) {
		t.Fatal("pause hold bypassed", err)
	}
	forged := b
	forged.ActorUID = "other"
	if err = s.RefreshMinimum(ctx, forged, 300); !errors.Is(err, ErrConflict) {
		t.Fatal("Actor UID bypassed", err)
	}
}

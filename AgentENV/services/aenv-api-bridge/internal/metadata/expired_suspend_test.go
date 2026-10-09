package metadata

import (
	"errors"
	"testing"
)

func TestAutoPauseClaimTransfersHoldAndFreezesGeneration(t *testing.T) {
	s := testStore(t)
	ctx := t.Context()
	b := seedSuspendSandbox(t, s, "auto")
	b, err := s.Get(ctx, b.Tenant, b.ExternalID)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.PutAccess(ctx, b, Access{AutoPause: true, EnvdPort: 49983}); err != nil {
		t.Fatal(err)
	}
	claims, err := s.ClaimExpired(ctx, 10)
	if err != nil || len(claims) != 1 || claims[0].Request.Kind != "suspend" {
		t.Fatalf("claim %v %v", claims, err)
	}
	if err = s.Extend(ctx, b.Tenant, b.ExternalID, b.Revision, 300); !errors.Is(err, ErrConflict) {
		t.Fatal("extended committed timeout", err)
	}
	i, err := s.PrepareExpiredSuspend(ctx, claims[0], 7)
	if err != nil {
		t.Fatal(err)
	}
	again, err := s.PrepareExpiredSuspend(ctx, claims[0], 8)
	if err != nil || again.Generation != 7 {
		t.Fatal("generation changed on retry", again, err)
	}
	if claims, err = s.ClaimExpired(ctx, 10); err != nil || len(claims) != 0 {
		t.Fatal("pause also deleted", err)
	}
	if _, err = s.ReserveResume(ctx, b, "s3://bucket/snapshot", 300, false); !errors.Is(err, ErrConflict) {
		t.Fatal("resumed unknown pause", err)
	}
	if err = s.ConfirmSuspended(ctx, i); err != nil {
		t.Fatal(err)
	}
	resume, err := s.ReserveResume(ctx, b, "s3://bucket/snapshot", 300, false)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.ConfirmResumed(ctx, resume); err != nil {
		t.Fatal(err)
	}
	if _, err = s.PrepareExpiredSuspend(ctx, claimsOr(t, i), 7); !errors.Is(err, ErrConflict) {
		t.Fatal("old timeout reinstated hold", err)
	}
}
func claimsOr(t *testing.T, i SuspendIntent) Expiry {
	t.Helper()
	return Expiry{Sandbox: i.Sandbox, Request: i.Request}
}

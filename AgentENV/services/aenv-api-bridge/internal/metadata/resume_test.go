package metadata

import (
	"errors"
	"testing"
	"time"
)

func TestResumeCommitsTimerAndReleasesPauseHoldAtomically(t *testing.T) {
	s := testStore(t)
	ctx := t.Context()
	b := seedSuspendSandbox(t, s, "sandbox")
	pause, err := s.ReserveSuspend(ctx, b, 7)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.ReserveResume(ctx, b, "s3://bucket/source", 300, false); !errors.Is(err, ErrConflict) {
		t.Fatal("unconfirmed pause resumed", err)
	}
	if err = s.ConfirmSuspended(ctx, pause); err != nil {
		t.Fatal(err)
	}
	intent, err := s.ReserveResume(ctx, b, "s3://bucket/source", 300, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.ReserveSuspend(ctx, b, 7); !errors.Is(err, ErrConflict) {
		t.Fatal("resume raced with new pause", err)
	}
	retry, err := s.ReserveResume(ctx, b, "s3://bucket/source", 300, true)
	if err != nil || retry.Request.ID != intent.Request.ID || retry.ExtendOnly {
		t.Fatal("unknown result changed original timeout semantics", err)
	}
	if claims, err := s.ClaimExpired(ctx, 10); err != nil || len(claims) != 0 {
		t.Fatal("resuming sandbox expired", err)
	}
	if _, err = s.Pool.Exec(ctx, `UPDATE aenv_bridge.resume_intents SET retry_after=clock_timestamp()-interval '1 second'`); err != nil {
		t.Fatal(err)
	}
	pending, err := s.ClaimResumes(ctx, 10)
	if err != nil || len(pending) != 1 || pending[0].SourceURI != intent.SourceURI || pending[0].Request.ID != intent.Request.ID {
		t.Fatal("restart lost resume preparation", err)
	}
	if err = s.ConfirmResumed(ctx, intent); err != nil {
		t.Fatal(err)
	}
	row, err := s.Get(ctx, b.Tenant, b.ExternalID)
	if err != nil || row.TimeoutSeconds != 300 || time.Until(row.ExpiresAt) < 290*time.Second {
		t.Fatal("resume did not rearm timer", row, err)
	}
	if err = s.ConfirmResumed(ctx, intent); err != nil {
		t.Fatal("duplicate resume completion", err)
	}
	again, err := s.Get(ctx, b.Tenant, b.ExternalID)
	if err != nil || again.Revision != row.Revision || !again.ExpiresAt.Equal(row.ExpiresAt) {
		t.Fatal("duplicate completion changed deadline", err)
	}
	if err = s.ConfirmSuspended(ctx, pause); !errors.Is(err, ErrConflict) {
		t.Fatal("late pause completion reinstated hold", err)
	}
	if _, err = s.ReserveSuspend(ctx, b, 8); err != nil {
		t.Fatal("new allocation cannot pause", err)
	}
}
func TestConnectOnlyExtendsAndExpiryBlocksResume(t *testing.T) {
	s := testStore(t)
	ctx := t.Context()
	b := seedSuspendSandbox(t, s, "sandbox")
	if err := s.Extend(ctx, b.Tenant, b.ExternalID, 1, 600); err != nil {
		t.Fatal(err)
	}
	before, err := s.Get(ctx, b.Tenant, b.ExternalID)
	if err != nil {
		t.Fatal(err)
	}
	intent, err := s.ReserveResume(ctx, b, "", 0, true)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.ConfirmResumed(ctx, intent); err != nil {
		t.Fatal(err)
	}
	after, err := s.Get(ctx, b.Tenant, b.ExternalID)
	if err != nil || !after.ExpiresAt.Equal(before.ExpiresAt) || after.TimeoutSeconds != 600 {
		t.Fatal("connect shortened existing TTL", err)
	}
	if err = s.Extend(ctx, b.Tenant, b.ExternalID, after.Revision, 0); err != nil {
		t.Fatal("zero timeout is valid", err)
	}
	if claims, err := s.ClaimExpired(ctx, 10); err != nil || len(claims) != 1 {
		t.Fatal("zero timeout did not expire", err)
	}
	if _, err = s.ReserveResume(ctx, b, "", 300, true); !errors.Is(err, ErrConflict) {
		t.Fatal("resume bypassed committed expiry", err)
	}
}
func TestTimeoutSupportsOriginalUnsigned32BitRange(t *testing.T) {
	s := testStore(t)
	b := seedSuspendSandbox(t, s, "sandbox")
	if err := s.Extend(t.Context(), b.Tenant, b.ExternalID, 1, 4294967295); err != nil {
		t.Fatal("maximum original timeout rejected", err)
	}
	got, err := s.Get(t.Context(), b.Tenant, b.ExternalID)
	if err != nil || got.TimeoutSeconds != 4294967295 {
		t.Fatal("timeout truncated", err)
	}
}

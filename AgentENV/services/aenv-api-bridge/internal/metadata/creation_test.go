package metadata

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestCreationPreparationHoldAndConfirmation(t *testing.T) {
	s := testStore(t)
	ctx := t.Context()
	b := Sandbox{Tenant: "tenant", ExternalID: "new", ActorAtespace: "space", ActorName: "actor", TemplateAlias: "template", TimeoutSeconds: 1}
	r := Request{Tenant: b.Tenant, ExternalID: b.ExternalID, ID: "create", Kind: "create", Digest: strings.Repeat("a", 64)}
	i, err := s.ReserveCreation(ctx, b, r, []byte("frozen"))
	if err != nil {
		t.Fatal(err)
	}
	replay, err := s.ReserveCreation(ctx, b, r, []byte("changed upstream template"))
	if err != nil || string(replay.Prepared) != "frozen" {
		t.Fatal("preparation changed", err)
	}
	if err = s.BindActor(ctx, b.Tenant, b.ExternalID, "uid"); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Pool.Exec(ctx, `UPDATE aenv_bridge.sandboxes SET expires_at=clock_timestamp()-interval '1 hour'`); err != nil {
		t.Fatal(err)
	}
	if claims, err := s.ClaimExpired(ctx, 10); err != nil || len(claims) != 0 {
		t.Fatal("creating actor expired", err)
	}
	if _, err = s.LookupExternal(ctx, b.ExternalID); !errors.Is(err, ErrNotFound) {
		t.Fatal("pending creation exposed public traffic", err)
	}
	p := Profile{TemplateID: "template", EnvdVersion: "0.5.0", CPUCount: 1, MemoryMB: 128, DiskSizeMB: 1024}
	if err = s.ConfirmCreated(ctx, i, "uid", p); err != nil {
		t.Fatal(err)
	}
	created, err := s.Get(ctx, b.Tenant, b.ExternalID)
	if err != nil || created.CreatedAt.IsZero() || created.ExpiresAt.Before(time.Now()) {
		t.Fatal("TTL or identity not committed", created, err)
	}
	if err = s.ConfirmCreated(ctx, i, "uid", p); err != nil {
		t.Fatal(err)
	}
	again, err := s.Get(ctx, b.Tenant, b.ExternalID)
	if err != nil || !again.ExpiresAt.Equal(created.ExpiresAt) {
		t.Fatal("repeat confirmation rearmed TTL", err)
	}
	if found, err := s.LookupExternal(ctx, b.ExternalID); err != nil || found.ActorUID != "uid" {
		t.Fatal("confirmed creation unavailable", err)
	}
	p2, err := s.Profile(ctx, created)
	if err != nil || p2.TemplateID != p.TemplateID {
		t.Fatal("profile unavailable", err)
	}
	jobs, err := s.ClaimCreations(ctx, 10)
	if err != nil || len(jobs) != 0 {
		t.Fatal("completed creation replayed", err)
	}
	r.Digest = strings.Repeat("b", 64)
	if _, err = s.ReserveCreation(ctx, b, r, []byte("other")); !errors.Is(err, ErrConflict) {
		t.Fatal("request key changed payload", err)
	}
}

package metadata

import (
	"context"
	"errors"
	"testing"
)

func TestAccessImmutableAndTenantLookup(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	b := Sandbox{Tenant: "one", ExternalID: "sandbox", ActorAtespace: "one", ActorName: "actor", TemplateAlias: "template", TimeoutSeconds: 300}
	r := Request{Tenant: b.Tenant, ExternalID: b.ExternalID, ID: "create", Kind: "create", Digest: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}
	if _, err := s.ReserveCreate(ctx, b, r); err != nil {
		t.Fatal(err)
	}
	if err := s.BindActor(ctx, b.Tenant, b.ExternalID, "uid"); err != nil {
		t.Fatal(err)
	}
	b.ActorUID = "uid"
	a := Access{Secure: true, EnvdPort: 49983, AutoResume: true}
	if err := s.PutAccess(ctx, b, a); err != nil {
		t.Fatal(err)
	}
	if err := s.PutAccess(ctx, b, a); err != nil {
		t.Fatal(err)
	}
	got, err := s.Access(ctx, b)
	if err != nil || got != a {
		t.Fatalf("access %v %v", got, err)
	}
	a.Secure = false
	if !errors.Is(s.PutAccess(ctx, b, a), ErrConflict) {
		t.Fatal("changed access accepted")
	}
	found, err := s.LookupExternal(ctx, b.ExternalID)
	if err != nil || found.ActorUID != "uid" {
		t.Fatal("lookup failed", err)
	}
	b.Tenant = "two"
	b.ActorAtespace = "two"
	r.Tenant = "two"
	if _, err = s.ReserveCreate(ctx, b, r); err != nil {
		t.Fatal(err)
	}
	if err = s.BindActor(ctx, "two", "sandbox", "uid-two"); err != nil {
		t.Fatal(err)
	}
	if _, err = s.LookupExternal(ctx, "sandbox"); !errors.Is(err, ErrNotFound) {
		t.Fatal("ambiguous host exposed tenant", err)
	}
}

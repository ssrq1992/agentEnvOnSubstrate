package metadata

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"os"
	"strings"
	"testing"
	"time"
)

func databaseRequired() bool {
	return os.Getenv("CI") == "true" || os.Getenv("REQUIRE_CATALOG_DATABASE") == "true"
}
func testStore(t *testing.T) *Store {
	t.Helper()
	dsn := os.Getenv("AENV_CATALOG_TEST_DSN")
	if dsn == "" {
		if databaseRequired() {
			t.Fatal("AENV_CATALOG_TEST_DSN is required for bridge metadata database tests")
		}
		t.Skip("bridge metadata database tests not executed: AENV_CATALOG_TEST_DSN absent")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	config, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	admin, err := pgxpool.NewWithConfig(ctx, config.Copy())
	if err != nil {
		t.Fatal(err)
	}
	token := make([]byte, 12)
	if _, err := rand.Read(token); err != nil {
		admin.Close()
		t.Fatal(err)
	}
	name := "aenv_metadata_test_" + hex.EncodeToString(token)
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+pgx.Identifier{name}.Sanitize()); err != nil {
		admin.Close()
		t.Fatal(err)
	}
	config.ConnConfig.Database = name
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		admin.Exec(ctx, "DROP DATABASE "+pgx.Identifier{name}.Sanitize())
		admin.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		pool.Close()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if _, err := admin.Exec(ctx, "DROP DATABASE "+pgx.Identifier{name}.Sanitize()); err != nil {
			t.Error(err)
		}
		admin.Close()
	})
	catalog := &Store{Pool: pool}
	if err := catalog.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err := catalog.Migrate(ctx); err != nil {
		t.Fatal("migration not idempotent", err)
	}
	return catalog
}

func TestValidateRequest(t *testing.T) {
	base := Request{Tenant: "tenant", ID: "request", ExternalID: "sandbox", Kind: "create", Digest: strings.Repeat("a", 64)}
	for _, field := range []string{"valid", "tenant", "id", "external", "kind", "digest", "uppercase"} {
		t.Run(field, func(t *testing.T) {
			r := base
			switch field {
			case "tenant":
				r.Tenant = ""
			case "id":
				r.ID = "bad\n"
			case "external":
				r.ExternalID = ""
			case "kind":
				r.Kind = "startVM"
			case "digest":
				r.Digest = "00"
			case "uppercase":
				r.Digest = strings.Repeat("A", 64)
			}
			if err := validateRequest(r); (err == nil) != (field == "valid") {
				t.Fatalf("unexpected validation %v", err)
			}
		})
	}
}
func TestMetadataIdentityAndOperationContract(t *testing.T) {
	s := testStore(t)
	ctx := t.Context()
	b := Sandbox{Tenant: "tenant", ExternalID: "sandbox", ActorAtespace: "space", ActorName: "stable-actor", TemplateAlias: "python", TimeoutSeconds: 300}
	r := Request{Tenant: b.Tenant, ID: "create-1", ExternalID: b.ExternalID, Kind: "create", Digest: strings.Repeat("a", 64)}
	got, err := s.ReserveCreate(ctx, b, r)
	if err != nil || got.State != "pending" {
		t.Fatalf("reserve: %v %v", got, err)
	}
	if _, err = s.ReserveCreate(ctx, b, r); err != nil {
		t.Fatal(err)
	}
	changed := r
	changed.Digest = strings.Repeat("b", 64)
	if _, err = s.ReserveCreate(ctx, b, changed); !errors.Is(err, ErrConflict) {
		t.Fatalf("payload reuse: %v", err)
	}
	other := b
	other.ActorName = "different-actor"
	if _, err = s.ReserveCreate(ctx, other, r); !errors.Is(err, ErrConflict) {
		t.Fatalf("changed mapping: %v", err)
	}
	if _, err = s.Get(ctx, "another-tenant", b.ExternalID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("tenant isolation: %v", err)
	}
	if err = s.BindActor(ctx, b.Tenant, b.ExternalID, "actor-uid"); err != nil {
		t.Fatal(err)
	}
	if err = s.BindActor(ctx, b.Tenant, b.ExternalID, "actor-uid"); err != nil {
		t.Fatal(err)
	}
	if err = s.BindActor(ctx, b.Tenant, b.ExternalID, "different-uid"); !errors.Is(err, ErrConflict) {
		t.Fatalf("actor rebound: %v", err)
	}
	if err = s.Complete(ctx, r, false, []byte("confirmed")); err != nil {
		t.Fatal(err)
	}
	if err = s.Complete(ctx, r, false, []byte("confirmed")); err != nil {
		t.Fatal(err)
	}
	if err = s.Complete(ctx, r, true, []byte("rejected")); !errors.Is(err, ErrConflict) {
		t.Fatalf("terminal result overwritten: %v", err)
	}
	got, err = s.ReserveCreate(ctx, b, r)
	if err != nil || got.State != "completed" || string(got.Result) != "confirmed" {
		t.Fatalf("cached result: %v %v", got, err)
	}
	row, err := s.Get(ctx, b.Tenant, b.ExternalID)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Extend(ctx, b.Tenant, b.ExternalID, row.Revision, 600); err != nil {
		t.Fatal(err)
	}
	if err = s.Extend(ctx, b.Tenant, b.ExternalID, row.Revision, 10); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale extension: %v", err)
	}
	// A creation retry does not reset an extended timeout.
	if _, err = s.ReserveCreate(ctx, b, r); err != nil {
		t.Fatal(err)
	}
	row, err = s.Get(ctx, b.Tenant, b.ExternalID)
	if err != nil || row.TimeoutSeconds != 600 || row.Revision != 2 {
		t.Fatalf("extension lost: %v %v", row, err)
	}

	deletion := Request{Tenant: b.Tenant, ID: "delete-1", ExternalID: b.ExternalID, Kind: "delete", Digest: strings.Repeat("c", 64)}
	if _, err = s.Reserve(ctx, deletion); err != nil {
		t.Fatal(err)
	}
	if err = s.ConfirmDeleted(ctx, deletion); err != nil {
		t.Fatal(err)
	}
	if err = s.ConfirmDeleted(ctx, deletion); err != nil {
		t.Fatal("deletion retry", err)
	}
	if _, err = s.Get(ctx, b.Tenant, b.ExternalID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleted sandbox still visible: %v", err)
	}
	fresh := r
	fresh.ID = "create-2"
	if _, err = s.ReserveCreate(ctx, b, fresh); !errors.Is(err, ErrConflict) {
		t.Fatalf("deleted identity reused: %v", err)
	}
	replay, err := s.ReserveCreate(ctx, b, r)
	if err != nil || replay.State != "completed" {
		t.Fatalf("original receipt lost: %v %v", replay, err)
	}
}

func TestExpiryDecisionOrdersAgainstExtension(t *testing.T) {
	s := testStore(t)
	ctx := t.Context()
	b := Sandbox{Tenant: "tenant", ExternalID: "expired", ActorAtespace: "space", ActorName: "actor", TemplateAlias: "python", TimeoutSeconds: 1}
	r := Request{Tenant: b.Tenant, ExternalID: b.ExternalID, ID: "create", Kind: "create", Digest: strings.Repeat("a", 64)}
	if _, err := s.ReserveCreate(ctx, b, r); err != nil {
		t.Fatal(err)
	}
	if err := s.BindActor(ctx, b.Tenant, b.ExternalID, "uid"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Pool.Exec(ctx, `UPDATE aenv_bridge.sandboxes SET expires_at=clock_timestamp()-interval '1 second'`); err != nil {
		t.Fatal(err)
	}
	if err := s.Extend(ctx, b.Tenant, b.ExternalID, 1, 300); err != nil {
		t.Fatal(err)
	}
	claims, err := s.ClaimExpired(ctx, 10)
	if err != nil || len(claims) != 0 {
		t.Fatalf("extended sandbox claimed: %v %v", claims, err)
	}
	if _, err := s.Pool.Exec(ctx, `UPDATE aenv_bridge.sandboxes SET expires_at=clock_timestamp()-interval '1 second'`); err != nil {
		t.Fatal(err)
	}
	claims, err = s.ClaimExpired(ctx, 10)
	if err != nil || len(claims) != 1 {
		t.Fatalf("expiry not claimed: %v %v", claims, err)
	}
	if err := s.Extend(ctx, b.Tenant, b.ExternalID, 2, 300); !errors.Is(err, ErrConflict) {
		t.Fatalf("uncertain deletion allowed extension: %v", err)
	}
	deferred, err := s.ClaimExpired(ctx, 10)
	if err != nil || len(deferred) != 0 {
		t.Fatal("expiry retry backoff missing", err)
	}
	if _, err = s.Pool.Exec(ctx, `UPDATE aenv_bridge.expiry_claims SET retry_after=clock_timestamp()-interval '1 second'`); err != nil {
		t.Fatal(err)
	}
	replay, err := s.ClaimExpired(ctx, 10)
	if err != nil || len(replay) != 1 || replay[0].Request.ID != claims[0].Request.ID {
		t.Fatalf("expiry identity changed: %v %v", replay, err)
	}
	if err := s.ConfirmDeleted(ctx, claims[0].Request); err != nil {
		t.Fatal(err)
	}
	claims, err = s.ClaimExpired(ctx, 10)
	if err != nil || len(claims) != 0 {
		t.Fatalf("deleted sandbox claimed: %v %v", claims, err)
	}
}

func TestConcurrentExpiryAndExtensionHaveOneWinner(t *testing.T) {
	s := testStore(t)
	for i := 0; i < 20; i++ {
		id := fmt.Sprintf("sandbox-%d", i)
		b := Sandbox{Tenant: "tenant", ExternalID: id, ActorAtespace: "space", ActorName: id, TemplateAlias: "python", TimeoutSeconds: 1}
		r := Request{Tenant: b.Tenant, ExternalID: id, ID: "create-" + id, Kind: "create", Digest: strings.Repeat("a", 64)}
		if _, err := s.ReserveCreate(t.Context(), b, r); err != nil {
			t.Fatal(err)
		}
		if err := s.BindActor(t.Context(), b.Tenant, id, "uid-"+id); err != nil {
			t.Fatal(err)
		}
		if _, err := s.Pool.Exec(t.Context(), `UPDATE aenv_bridge.sandboxes SET expires_at=clock_timestamp()-interval '1 second' WHERE tenant=$1 AND external_id=$2`, b.Tenant, id); err != nil {
			t.Fatal(err)
		}
		start := make(chan struct{})
		extended := make(chan error, 1)
		type result struct {
			claims []Expiry
			err    error
		}
		claimed := make(chan result, 1)
		go func() { <-start; extended <- s.Extend(t.Context(), b.Tenant, id, 1, 300) }()
		go func() { <-start; claims, err := s.ClaimExpired(t.Context(), 1); claimed <- result{claims, err} }()
		close(start)
		extensionErr, capture := <-extended, <-claimed
		if capture.err != nil {
			t.Fatal(capture.err)
		}
		if extensionErr == nil {
			if len(capture.claims) != 0 {
				t.Fatal("both extension and expiry committed")
			}
		} else {
			if !errors.Is(extensionErr, ErrConflict) || len(capture.claims) != 1 || capture.claims[0].Sandbox.ExternalID != id {
				t.Fatalf("unexpected race outcome: %v %+v", extensionErr, capture)
			}
			if err := s.ConfirmDeleted(t.Context(), capture.claims[0].Request); err != nil {
				t.Fatal(err)
			}
		}
	}
}

func TestPolicyPreparationIsImmutableAcrossConcurrentRetries(t *testing.T) {
	s := testStore(t)
	ctx := t.Context()
	b := Sandbox{Tenant: "tenant", ExternalID: "sandbox", ActorAtespace: "space", ActorName: "actor", TemplateAlias: "python", TimeoutSeconds: 300}
	r := Request{Tenant: b.Tenant, ID: "create", ExternalID: b.ExternalID, Kind: "create", Digest: strings.Repeat("a", 64)}
	if _, err := s.ReserveCreate(ctx, b, r); err != nil {
		t.Fatal(err)
	}
	r.ID = "policy"
	r.Kind = "policy"
	if _, err := s.Reserve(ctx, r); err != nil {
		t.Fatal(err)
	}
	type outcome struct {
		r   Request
		err error
	}
	results := make(chan outcome, 12)
	for i := 0; i < 12; i++ {
		go func() {
			prepared := []byte(fmt.Sprintf(`{"revision":%d}`, i))
			got, err := s.Prepare(ctx, r, prepared)
			results <- outcome{got, err}
		}()
	}
	var first string
	for i := 0; i < 12; i++ {
		got := <-results
		if got.err != nil {
			t.Fatal(got.err)
		}
		if first == "" {
			first = string(got.r.Result)
		}
		if string(got.r.Result) != first {
			t.Fatal("concurrent retries changed preparation")
		}
	}
	got, err := s.Reserve(ctx, r)
	if err != nil || string(got.Result) != first {
		t.Fatal("restart lost preparation", err)
	}
	wrong := r
	wrong.Digest = strings.Repeat("b", 64)
	if _, err = s.Prepare(ctx, wrong, []byte(`{"revision":99}`)); !errors.Is(err, ErrConflict) {
		t.Fatal("preparation ignored digest", err)
	}
	if err = s.Complete(ctx, r, false, []byte(`{}`)); err != nil {
		t.Fatal(err)
	}
	got, err = s.Prepare(ctx, r, []byte(`{"revision":99}`))
	if err != nil || got.State != "completed" || string(got.Result) != "{}" {
		t.Fatal("completed request reopened", err)
	}
}

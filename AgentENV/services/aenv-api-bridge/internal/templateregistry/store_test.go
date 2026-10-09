package templateregistry

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"os"
	"testing"
	"time"

	"agentenv/services/aenv-api-bridge/internal/creation"
	"agentenv/services/aenv-api-bridge/internal/metadata"
	"errors"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"strings"
)

func databaseRequired() bool {
	return os.Getenv("CI") == "true" || os.Getenv("REQUIRE_CATALOG_DATABASE") == "true"
}
func testRegistry(t *testing.T) *Store {
	t.Helper()
	dsn := os.Getenv("AENV_CATALOG_TEST_DSN")
	if dsn == "" {
		if databaseRequired() {
			t.Fatal("AENV_CATALOG_TEST_DSN is required for catalog database tests")
		}
		t.Skip("catalog database tests not executed: AENV_CATALOG_TEST_DSN absent")
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
	name := "aenv_registry_test_" + hex.EncodeToString(token)
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

func profile(id, alias string) creation.Template {
	return creation.Template{Tenant: "tenant", ID: id, Name: "native-" + id, Alias: alias, NativeUID: "uid-" + id, NativeDigest: strings.Repeat("a", 64), EnvdPort: 49983, EnvdVersion: "v1", CPUCount: 1, MemoryMB: 128, DiskSizeMB: 1024}
}
func TestPublishAndLookupSurviveRestartWithoutStealingReferences(t *testing.T) {
	registry := testRegistry(t)
	ctx := t.Context()
	now := time.Now().UTC().Truncate(time.Microsecond)
	p := profile("first", "python")
	if err := registry.Publish(ctx, p, now, now); err != nil {
		t.Fatal(err)
	}
	restarted := &Store{Pool: registry.Pool}
	if err := restarted.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err := restarted.Publish(ctx, p, now, now); err != nil {
		t.Fatal(err)
	}
	got, err := restarted.Lookup(ctx, "tenant", "python")
	if err != nil || got != p {
		t.Fatalf("lookup: %+v %v", got, err)
	}
	if _, err := restarted.Get(ctx, "other", "python"); !errors.Is(err, metadata.ErrNotFound) {
		t.Fatal("tenant leak", err)
	}
	modified := p
	modified.MemoryMB = 256
	if err := restarted.Publish(ctx, modified, now, now); !errors.Is(err, metadata.ErrConflict) {
		t.Fatal("immutable profile replaced", err)
	}
	if err := restarted.Publish(ctx, profile("second", "python"), now, now); !errors.Is(err, metadata.ErrConflict) {
		t.Fatal("alias stolen", err)
	}
	if _, err := restarted.Get(ctx, "tenant", "second"); !errors.Is(err, metadata.ErrNotFound) {
		t.Fatal("partial publish committed", err)
	}
	if err := restarted.Publish(ctx, profile("python", "third"), now, now); !errors.Is(err, metadata.ErrConflict) {
		t.Fatal("ID shadowed alias", err)
	}
	got, err = restarted.Lookup(ctx, "tenant", "first")
	if err != nil || got != p {
		t.Fatal(got, err)
	}
}
func TestRegistryPageUsesStableTiesAndTenantScope(t *testing.T) {
	registry := testRegistry(t)
	ctx := t.Context()
	now := time.Now().UTC().Truncate(time.Microsecond)
	for _, id := range []string{"a", "b", "c"} {
		if err := registry.Publish(ctx, profile(id, "alias-"+id), now, now); err != nil {
			t.Fatal(err)
		}
	}
	other := profile("z", "alias-z")
	other.Tenant = "other"
	if err := registry.Publish(ctx, other, now, now); err != nil {
		t.Fatal(err)
	}
	page, err := registry.Page(ctx, "tenant", nil, 2)
	if err != nil || len(page) != 2 || page[0].Profile.ID != "c" || page[1].Profile.ID != "b" {
		t.Fatal(page, err)
	}
	cursor := &Cursor{Tenant: "tenant", ID: "b", Time: now}
	page, err = registry.Page(ctx, "tenant", cursor, 2)
	if err != nil || len(page) != 1 || page[0].Profile.ID != "a" {
		t.Fatal(page, err)
	}
	cursor.Tenant = "other"
	if _, err := registry.Page(ctx, "tenant", cursor, 2); err == nil {
		t.Fatal("foreign cursor accepted")
	}
}
func TestMigrationRejectsModifiedDefinition(t *testing.T) {
	registry := testRegistry(t)
	if _, err := registry.Pool.Exec(t.Context(), `UPDATE aenv_bridge.template_registry_schema SET digest='modified'`); err != nil {
		t.Fatal(err)
	}
	if err := registry.Migrate(t.Context()); err == nil {
		t.Fatal("modified schema accepted")
	}
}

func TestRegistryRejectsCorruptStoredProfiles(t *testing.T) {
	for _, mutation := range []string{
		`UPDATE aenv_bridge.template_registry SET profile=jsonb_set(profile,'{memoryMB}','256')`,
		`UPDATE aenv_bridge.template_registry SET profile_digest='bad'`,
		`UPDATE aenv_bridge.template_registry SET profile=jsonb_set(profile,'{tenant}','"other"')`,
		`UPDATE aenv_bridge.template_registry SET profile=jsonb_set(profile,'{id}','"other"')`,
	} {
		t.Run(mutation, func(t *testing.T) {
			registry := testRegistry(t)
			now := time.Now().UTC()
			if err := registry.Publish(t.Context(), profile("first", "python"), now, now); err != nil {
				t.Fatal(err)
			}
			if _, err := registry.Pool.Exec(t.Context(), mutation); err != nil {
				t.Fatal(err)
			}
			if got, err := registry.Get(t.Context(), "tenant", "python"); err == nil || got.Profile.ID != "" {
				t.Fatal("corrupt profile returned", got, err)
			}
			if got, err := registry.Page(t.Context(), "tenant", nil, 10); err == nil || len(got) != 0 {
				t.Fatal("corrupt page returned", got, err)
			}
		})
	}
}

func TestReplacementAliasIsAtomicAndRejectsLateBuilds(t *testing.T) {
	s := testRegistry(t)
	ctx := t.Context()
	now := time.Now().UTC()
	first := profile("first", "python")
	second := profile("second", "python")
	if err := s.Publish(ctx, first, now, now); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := s.PublishReplacingAlias(ctx, second, now, now, "first"); err != nil {
			t.Fatal(err)
		}
	}
	if got, err := s.Lookup(ctx, "tenant", "python"); err != nil || got != second {
		t.Fatal(got, err)
	}
	if got, err := s.Lookup(ctx, "tenant", "first"); err != nil || got != first {
		t.Fatal("old ID moved", got, err)
	}
	late := profile("late", "python")
	if err := s.PublishReplacingAlias(ctx, late, now, now, "first"); !errors.Is(err, metadata.ErrConflict) {
		t.Fatal("late build replaced alias", err)
	}
	if _, err := s.Get(ctx, "tenant", "late"); !errors.Is(err, metadata.ErrNotFound) {
		t.Fatal("failed replacement partially committed", err)
	}
	missing := profile("missing", "absent")
	if err := s.PublishReplacingAlias(ctx, missing, now, now, "first"); !errors.Is(err, metadata.ErrConflict) {
		t.Fatal("missing expected alias accepted", err)
	}
	shadow := profile("shadow", "first")
	if err := s.PublishReplacingAlias(ctx, shadow, now, now, "first"); !errors.Is(err, metadata.ErrConflict) {
		t.Fatal("template ID replaced", err)
	}
}

package catalog

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func databaseRequired() bool {
	return os.Getenv("CI") == "true" || os.Getenv("REQUIRE_CATALOG_DATABASE") == "true"
}
func testCatalog(t *testing.T) *Catalog {
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
	name := "aenv_catalog_test_" + hex.EncodeToString(token)
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
	catalog := &Catalog{Pool: pool, GracePeriod: time.Hour}
	if err := catalog.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err := catalog.Migrate(ctx); err != nil {
		t.Fatal("migration not idempotent", err)
	}
	return catalog
}

type deleteFunc func(context.Context, string) error

func (f deleteFunc) Delete(ctx context.Context, key string) error { return f(ctx, key) }
func readyOwner(t *testing.T, c *Catalog, op string, owner Owner, layer Layer) {
	t.Helper()
	ctx := t.Context()
	if err := c.Reserve(ctx, owner.Tenant, op, []Layer{layer}); err != nil {
		t.Fatal(err)
	}
	if err := c.ConfirmUploaded(ctx, owner.Tenant, op, layer); err != nil {
		t.Fatal(err)
	}
	if err := c.CommitOwner(ctx, op, owner); err != nil {
		t.Fatal(err)
	}
}
func expireGC(t *testing.T, c *Catalog) {
	t.Helper()
	if _, err := c.Pool.Exec(t.Context(), `UPDATE aenv_bridge.catalog_layers SET delete_after=now()-interval '1 second' WHERE delete_after IS NOT NULL`); err != nil {
		t.Fatal(err)
	}
}
func TestCatalogReferenceLifecycleDatabase(t *testing.T) {
	c := testCatalog(t)
	ctx := t.Context()
	layer := Layer{Digest: strings.Repeat("a", 64), Size: 1024}
	owner := Owner{Tenant: "tenant", Kind: "snapshot", UID: "snapshot-1"}
	if err := c.Reserve(ctx, owner.Tenant, "op", []Layer{layer}); err != nil {
		t.Fatal(err)
	}
	if err := c.CommitOwner(ctx, "op", owner); err == nil {
		t.Fatal("unverified object committed")
	}
	readyOwner(t, c, "op", owner, layer)
	if err := c.CommitOwner(ctx, "op", owner); err != nil {
		t.Fatal(err)
	}
	changed := layer
	changed.Size++
	if err := c.Reserve(ctx, owner.Tenant, "op", []Layer{changed}); err == nil {
		t.Fatal("operation payload changed")
	}
	pin := Pin{Tenant: "tenant", ActorUID: "actor", Generation: 1, WorkerPodUID: "pod", WorkerEpoch: 1, ExecutorInstanceID: "executor"}
	if err := c.PinOwner(ctx, owner, pin); err != nil {
		t.Fatal(err)
	}
	if err := c.EnqueueRelease(ctx, "release", owner); err != nil {
		t.Fatal(err)
	}
	if processed, err := c.ProcessRelease(ctx); err != nil || !processed {
		t.Fatal("release failed", err)
	}
	deleted := 0
	objects := deleteFunc(func(context.Context, string) error { deleted++; return nil })
	expireGC(t, c)
	if collected, err := c.CollectOne(ctx, objects); err != nil || collected || deleted != 0 {
		t.Fatal("live runtime pin did not protect layer", err)
	}
	wrong := pin
	wrong.Generation++
	if err := c.Unpin(ctx, wrong); err == nil {
		t.Fatal("wrong generation unpinned runtime")
	}
	if err := c.Unpin(ctx, pin); err != nil {
		t.Fatal(err)
	}
	if err := c.PinOwner(ctx, owner, pin); err == nil {
		t.Fatal("stopped runtime or released owner resurrected")
	}
	if collected, err := c.CollectOne(ctx, objects); err != nil || collected {
		t.Fatal("GC ignored grace period", err)
	}
	expireGC(t, c)
	if collected, err := c.CollectOne(ctx, objects); err != nil || !collected || deleted != 1 {
		t.Fatal("unreferenced layer not collected", err)
	}
}
func TestCatalogGCSerializesRetainDatabase(t *testing.T) {
	c := testCatalog(t)
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	layer := Layer{Digest: strings.Repeat("b", 64), Size: 10}
	owner := Owner{Tenant: "tenant", Kind: "template", UID: "template"}
	readyOwner(t, c, "first", owner, layer)
	if err := c.EnqueueRelease(ctx, "release", owner); err != nil {
		t.Fatal(err)
	}
	if _, err := c.ProcessRelease(ctx); err != nil {
		t.Fatal(err)
	}
	expireGC(t, c)
	entered := make(chan struct{})
	finish := make(chan struct{})
	collected := make(chan error, 1)
	go func() {
		_, err := c.CollectOne(ctx, deleteFunc(func(ctx context.Context, key string) error {
			close(entered)
			select {
			case <-finish:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		}))
		collected <- err
	}()
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	retained := make(chan error, 1)
	go func() { retained <- c.Reserve(ctx, "tenant", "second", []Layer{layer}) }()
	select {
	case err := <-retained:
		t.Fatal("retain bypassed deletion lock", err)
	case <-time.After(100 * time.Millisecond):
	}
	close(finish)
	if err := <-collected; err != nil {
		t.Fatal(err)
	}
	if err := <-retained; err != nil {
		t.Fatal(err)
	}
	if err := c.CommitOwner(ctx, "second", Owner{Tenant: "tenant", Kind: "snapshot", UID: "new"}); err == nil {
		t.Fatal("deleted object reused without upload verification")
	}
}

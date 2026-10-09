// Package catalog retains shared layers independently of Actor placement.
package catalog

import (
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed schema.sql
var schema string

type Catalog struct {
	Pool        *pgxpool.Pool
	GracePeriod time.Duration
}
type Layer struct {
	Digest string `json:"digest"`
	Size   int64  `json:"size"`
}
type Owner struct{ Tenant, Kind, UID string }
type Pin struct {
	Tenant, ActorUID, WorkerPodUID, ExecutorInstanceID string
	Generation, WorkerEpoch                            uint64
}
type Deleter interface {
	Delete(context.Context, string) error
}

func ObjectKey(digest string) (string, error) {
	if !validDigest(digest) {
		return "", fmt.Errorf("invalid layer digest")
	}
	return "sha256/" + digest[:2] + "/" + digest, nil
}
func validDigest(value string) bool {
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == 32 && strings.ToLower(value) == value
}
func identity(values ...string) error {
	for _, value := range values {
		if value == "" || len(value) > 256 || strings.ContainsAny(value, "\x00\r\n") {
			return fmt.Errorf("invalid catalog identity")
		}
	}
	return nil
}
func canonicalLayers(layers []Layer) ([]Layer, string, error) {
	if len(layers) == 0 || len(layers) > 100000 {
		return nil, "", fmt.Errorf("bounded nonempty layer set required")
	}
	sorted := append([]Layer(nil), layers...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Digest < sorted[j].Digest })
	for i, layer := range sorted {
		if !validDigest(layer.Digest) || layer.Size < 0 {
			return nil, "", fmt.Errorf("invalid layer descriptor")
		}
		if i > 0 && sorted[i-1].Digest == layer.Digest {
			return nil, "", fmt.Errorf("duplicate layer")
		}
	}
	wire, err := json.Marshal(sorted)
	if err != nil {
		return nil, "", err
	}
	digest := sha256.Sum256(wire)
	return sorted, hex.EncodeToString(digest[:]), nil
}
func (c *Catalog) Migrate(ctx context.Context) error {
	tx, err := c.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock(718304241)"); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `CREATE SCHEMA IF NOT EXISTS aenv_bridge; CREATE TABLE IF NOT EXISTS aenv_bridge.catalog_schema(version integer PRIMARY KEY,digest text NOT NULL)`); err != nil {
		return err
	}
	sum := sha256.Sum256([]byte(schema))
	expected := hex.EncodeToString(sum[:])
	var version int
	var stored string
	err = tx.QueryRow(ctx, `SELECT version,digest FROM aenv_bridge.catalog_schema ORDER BY version DESC LIMIT 1`).Scan(&version, &stored)
	if err == nil {
		if version != 1 || stored != expected {
			return fmt.Errorf("unsupported or modified catalog migration")
		}
		return tx.Commit(ctx)
	}
	if err != pgx.ErrNoRows {
		return err
	}
	if _, err := tx.Exec(ctx, schema); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO aenv_bridge.catalog_schema(version,digest) VALUES(1,$1)`, expected); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
func lockLayer(ctx context.Context, tx pgx.Tx, digest string) error {
	_, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtextextended($1, 0))", "aenv-catalog-layer:"+digest)
	return err
}

// Reserve must commit before uploading objects or submitting the control-plane
// snapshot mutation. Unknown execution results retain these references.
func (c *Catalog) Reserve(ctx context.Context, tenant, operation string, layers []Layer) error {
	if err := identity(tenant, operation); err != nil {
		return err
	}
	layers, digest, err := canonicalLayers(layers)
	if err != nil {
		return err
	}
	tx, err := c.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `INSERT INTO aenv_bridge.catalog_operations(tenant,operation_id,payload_digest,state) VALUES($1,$2,$3,'reserved') ON CONFLICT DO NOTHING`, tenant, operation, digest); err != nil {
		return err
	}
	var previous, state string
	if err := tx.QueryRow(ctx, `SELECT payload_digest,state FROM aenv_bridge.catalog_operations WHERE tenant=$1 AND operation_id=$2 FOR UPDATE`, tenant, operation).Scan(&previous, &state); err != nil {
		return err
	}
	if previous != digest {
		return fmt.Errorf("operation reused with different layer set")
	}
	if state == "committed" {
		return tx.Commit(ctx)
	}
	if state != "reserved" {
		return fmt.Errorf("released operation cannot be resurrected")
	}
	for _, layer := range layers {
		if err := lockLayer(ctx, tx, layer.Digest); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO aenv_bridge.catalog_layers(digest,size_bytes) VALUES($1,$2) ON CONFLICT DO NOTHING`, layer.Digest, layer.Size); err != nil {
			return err
		}
		var size int64
		if err := tx.QueryRow(ctx, `SELECT size_bytes FROM aenv_bridge.catalog_layers WHERE digest=$1`, layer.Digest).Scan(&size); err != nil {
			return err
		}
		if size != layer.Size {
			return fmt.Errorf("layer size changed for immutable digest")
		}
		if _, err := tx.Exec(ctx, `INSERT INTO aenv_bridge.catalog_operation_refs(tenant,operation_id,digest) VALUES($1,$2,$3) ON CONFLICT DO NOTHING`, tenant, operation, layer.Digest); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE aenv_bridge.catalog_layers SET delete_after=NULL WHERE digest=$1`, layer.Digest); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

// ConfirmUploaded is called by the authenticated uploader after it has verified
// the object's SHA256 and length. It is not exposed as an unauthenticated API.
func (c *Catalog) ConfirmUploaded(ctx context.Context, tenant, operation string, layer Layer) error {
	if err := identity(tenant, operation); err != nil {
		return err
	}
	if !validDigest(layer.Digest) || layer.Size < 0 {
		return fmt.Errorf("invalid verified layer")
	}
	tx, err := c.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err := lockLayer(ctx, tx, layer.Digest); err != nil {
		return err
	}
	tag, err := tx.Exec(ctx, `UPDATE aenv_bridge.catalog_layers l SET uploaded=true WHERE digest=$1 AND size_bytes=$2 AND EXISTS(SELECT 1 FROM aenv_bridge.catalog_operation_refs r WHERE r.tenant=$3 AND r.operation_id=$4 AND r.digest=l.digest)`, layer.Digest, layer.Size, tenant, operation)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return fmt.Errorf("verified upload has no matching reservation")
	}
	if _, err := tx.Exec(ctx, `UPDATE aenv_bridge.catalog_operation_refs SET verified=true WHERE tenant=$1 AND operation_id=$2 AND digest=$3`, tenant, operation, layer.Digest); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
func operationLayers(ctx context.Context, tx pgx.Tx, tenant, operation string) ([]string, error) {
	rows, err := tx.Query(ctx, `SELECT digest FROM aenv_bridge.catalog_operation_refs WHERE tenant=$1 AND operation_id=$2 ORDER BY digest`, tenant, operation)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return pgx.CollectRows(rows, pgx.RowTo[string])
}

// CommitOwner transfers operation references to a durable owner atomically,
// after the corresponding control-plane resource has committed.
func (c *Catalog) CommitOwner(ctx context.Context, operation string, owner Owner) error {
	if err := identity(owner.Tenant, owner.Kind, owner.UID, operation); err != nil {
		return err
	}
	tx, err := c.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var state string
	var kind, uid *string
	if err := tx.QueryRow(ctx, `SELECT state,owner_kind,owner_uid FROM aenv_bridge.catalog_operations WHERE tenant=$1 AND operation_id=$2 FOR UPDATE`, owner.Tenant, operation).Scan(&state, &kind, &uid); err != nil {
		return err
	}
	if state == "committed" {
		if kind == nil || uid == nil || *kind != owner.Kind || *uid != owner.UID {
			return fmt.Errorf("operation committed to another owner")
		}
		return tx.Commit(ctx)
	}
	if state != "reserved" {
		return fmt.Errorf("operation no longer reserved")
	}
	if released, err := lockOwner(ctx, tx, owner); err != nil {
		return err
	} else if released {
		return fmt.Errorf("released owner cannot be resurrected")
	}
	digests, err := operationLayers(ctx, tx, owner.Tenant, operation)
	if err != nil {
		return err
	}
	if len(digests) == 0 {
		return fmt.Errorf("empty reserved layer set")
	}
	for _, digest := range digests {
		if err := lockLayer(ctx, tx, digest); err != nil {
			return err
		}
		var ready bool
		if err := tx.QueryRow(ctx, `SELECT l.uploaded AND r.verified FROM aenv_bridge.catalog_layers l JOIN aenv_bridge.catalog_operation_refs r USING(digest) WHERE l.digest=$1 AND r.tenant=$2 AND r.operation_id=$3`, digest, owner.Tenant, operation).Scan(&ready); err != nil {
			return err
		}
		if !ready {
			return fmt.Errorf("layer upload not confirmed")
		}
		if _, err := tx.Exec(ctx, `INSERT INTO aenv_bridge.catalog_owners(tenant,kind,uid,digest) VALUES($1,$2,$3,$4) ON CONFLICT DO NOTHING`, owner.Tenant, owner.Kind, owner.UID, digest); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE aenv_bridge.catalog_operations SET state='committed',owner_kind=$3,owner_uid=$4 WHERE tenant=$1 AND operation_id=$2`, owner.Tenant, operation, owner.Kind, owner.UID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM aenv_bridge.catalog_operation_refs WHERE tenant=$1 AND operation_id=$2`, owner.Tenant, operation); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
func validatePin(pin Pin) error {
	if err := identity(pin.Tenant, pin.ActorUID, pin.WorkerPodUID, pin.ExecutorInstanceID); err != nil {
		return err
	}
	if pin.Generation == 0 || pin.WorkerEpoch == 0 || pin.Generation > math.MaxInt64 || pin.WorkerEpoch > math.MaxInt64 {
		return fmt.Errorf("complete bounded execution fence required")
	}
	return nil
}

// PinOwner holds a snapshot/template/volume's layers while an Actor uses them.
// A new allocation cannot replace pins from an unconfirmed old allocation.
func (c *Catalog) PinOwner(ctx context.Context, owner Owner, pin Pin) error {
	if err := validatePin(pin); err != nil {
		return err
	}
	if err := identity(owner.Tenant, owner.Kind, owner.UID); err != nil {
		return err
	}
	if pin.Tenant != owner.Tenant {
		return fmt.Errorf("cross-tenant pin denied")
	}
	tx, err := c.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err := lockRuntime(ctx, tx, pin, false); err != nil {
		return err
	}
	if released, err := lockOwner(ctx, tx, owner); err != nil {
		return err
	} else if released {
		return fmt.Errorf("cannot pin released owner")
	}
	rows, err := tx.Query(ctx, `SELECT digest FROM aenv_bridge.catalog_owners WHERE tenant=$1 AND kind=$2 AND uid=$3 ORDER BY digest`, owner.Tenant, owner.Kind, owner.UID)
	if err != nil {
		return err
	}
	digests, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return err
	}
	if len(digests) == 0 {
		return fmt.Errorf("owner has no retained layers")
	}
	for _, digest := range digests {
		if err := lockLayer(ctx, tx, digest); err != nil {
			return err
		}
		tag, err := tx.Exec(ctx, `INSERT INTO aenv_bridge.catalog_pins(tenant,actor_uid,generation,worker_pod_uid,worker_epoch,executor_instance_id,digest)
   SELECT $1,$2,$3,$4,$5,$6,$7 WHERE EXISTS(SELECT 1 FROM aenv_bridge.catalog_owners WHERE tenant=$1 AND kind=$8 AND uid=$9 AND digest=$7)
   ON CONFLICT(tenant,actor_uid,digest) DO UPDATE SET generation=EXCLUDED.generation
   WHERE catalog_pins.generation=EXCLUDED.generation AND catalog_pins.worker_pod_uid=EXCLUDED.worker_pod_uid AND catalog_pins.worker_epoch=EXCLUDED.worker_epoch AND catalog_pins.executor_instance_id=EXCLUDED.executor_instance_id`, pin.Tenant, pin.ActorUID, int64(pin.Generation), pin.WorkerPodUID, int64(pin.WorkerEpoch), pin.ExecutorInstanceID, digest, owner.Kind, owner.UID)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return fmt.Errorf("owner released or previous runtime pin is still live")
		}
		if _, err := tx.Exec(ctx, `UPDATE aenv_bridge.catalog_layers SET delete_after=NULL WHERE digest=$1`, digest); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

const unreferenced = `NOT EXISTS(SELECT 1 FROM aenv_bridge.catalog_owners o WHERE o.digest=l.digest) AND NOT EXISTS(SELECT 1 FROM aenv_bridge.catalog_operation_refs r WHERE r.digest=l.digest) AND NOT EXISTS(SELECT 1 FROM aenv_bridge.catalog_pins p WHERE p.digest=l.digest)`

func (c *Catalog) deferGC(ctx context.Context, tx pgx.Tx, digest string) error {
	if c.GracePeriod <= 0 {
		return fmt.Errorf("positive GC grace period required")
	}
	_, err := tx.Exec(ctx, `UPDATE aenv_bridge.catalog_layers l SET delete_after=COALESCE(delete_after,now()+$2::interval) WHERE digest=$1 AND `+unreferenced, digest, fmt.Sprintf("%f seconds", c.GracePeriod.Seconds()))
	return err
}
func (c *Catalog) EnqueueRelease(ctx context.Context, id string, owner Owner) error {
	if err := identity(id, owner.Tenant, owner.Kind, owner.UID); err != nil {
		return err
	}
	tag, err := c.Pool.Exec(ctx, `INSERT INTO aenv_bridge.catalog_outbox(release_id,tenant,owner_kind,owner_uid) VALUES($1,$2,$3,$4)
 ON CONFLICT(release_id) DO UPDATE SET release_id=EXCLUDED.release_id WHERE catalog_outbox.tenant=EXCLUDED.tenant AND catalog_outbox.owner_kind=EXCLUDED.owner_kind AND catalog_outbox.owner_uid=EXCLUDED.owner_uid`, id, owner.Tenant, owner.Kind, owner.UID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return fmt.Errorf("release identity reused for another owner")
	}
	return nil
}
func (c *Catalog) ProcessRelease(ctx context.Context) (bool, error) {
	tx, err := c.Pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx)
	var id string
	var owner Owner
	err = tx.QueryRow(ctx, `SELECT release_id,tenant,owner_kind,owner_uid FROM aenv_bridge.catalog_outbox WHERE NOT completed ORDER BY created_at LIMIT 1 FOR UPDATE SKIP LOCKED`).Scan(&id, &owner.Tenant, &owner.Kind, &owner.UID)
	if err == pgx.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if _, err := lockOwner(ctx, tx, owner); err != nil {
		return false, err
	}
	if _, err := tx.Exec(ctx, `UPDATE aenv_bridge.catalog_owner_state SET released=true WHERE tenant=$1 AND kind=$2 AND uid=$3`, owner.Tenant, owner.Kind, owner.UID); err != nil {
		return false, err
	}
	rows, err := tx.Query(ctx, `SELECT digest FROM aenv_bridge.catalog_owners WHERE tenant=$1 AND kind=$2 AND uid=$3 ORDER BY digest`, owner.Tenant, owner.Kind, owner.UID)
	if err != nil {
		return false, err
	}
	digests, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return false, err
	}
	for _, digest := range digests {
		if err := lockLayer(ctx, tx, digest); err != nil {
			return false, err
		}
	}
	if _, err := tx.Exec(ctx, `DELETE FROM aenv_bridge.catalog_owners WHERE tenant=$1 AND kind=$2 AND uid=$3`, owner.Tenant, owner.Kind, owner.UID); err != nil {
		return false, err
	}
	for _, digest := range digests {
		if err := c.deferGC(ctx, tx, digest); err != nil {
			return false, err
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE aenv_bridge.catalog_outbox SET completed=true WHERE release_id=$1`, id); err != nil {
		return false, err
	}
	return true, tx.Commit(ctx)
}

// CollectOne rechecks references under the same layer lock used by all retain
// operations. A failed transaction or unavailable catalog never authorizes a
// deletion. Object deletion must be idempotent, including after a lost commit.
func (c *Catalog) CollectOne(ctx context.Context, objects Deleter) (bool, error) {
	if objects == nil {
		return false, fmt.Errorf("object deleter required")
	}
	tx, err := c.Pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx)
	var digest string
	err = tx.QueryRow(ctx, `SELECT digest FROM aenv_bridge.catalog_layers WHERE delete_after<=now() ORDER BY delete_after LIMIT 1`).Scan(&digest)
	if err == pgx.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if err := lockLayer(ctx, tx, digest); err != nil {
		return false, err
	}
	var eligible bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM aenv_bridge.catalog_layers l WHERE digest=$1 AND delete_after<=now() AND `+unreferenced+`)`, digest).Scan(&eligible); err != nil {
		return false, err
	}
	if !eligible {
		return false, nil
	}
	key, err := ObjectKey(digest)
	if err != nil {
		return false, err
	}
	deleteCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if err := objects.Delete(deleteCtx, key); err != nil {
		return false, err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM aenv_bridge.catalog_layers WHERE digest=$1`, digest); err != nil {
		return false, err
	}
	return true, tx.Commit(ctx)
}

func lockOwner(ctx context.Context, tx pgx.Tx, owner Owner) (bool, error) {
	if _, err := tx.Exec(ctx, `INSERT INTO aenv_bridge.catalog_owner_state(tenant,kind,uid) VALUES($1,$2,$3) ON CONFLICT DO NOTHING`, owner.Tenant, owner.Kind, owner.UID); err != nil {
		return false, err
	}
	var released bool
	err := tx.QueryRow(ctx, `SELECT released FROM aenv_bridge.catalog_owner_state WHERE tenant=$1 AND kind=$2 AND uid=$3 FOR UPDATE`, owner.Tenant, owner.Kind, owner.UID).Scan(&released)
	return released, err
}
func lockRuntime(ctx context.Context, tx pgx.Tx, pin Pin, stopping bool) error {
	if _, err := tx.Exec(ctx, `INSERT INTO aenv_bridge.catalog_runtime_state(tenant,actor_uid,generation,worker_pod_uid,worker_epoch,executor_instance_id) VALUES($1,$2,$3,$4,$5,$6) ON CONFLICT DO NOTHING`, pin.Tenant, pin.ActorUID, int64(pin.Generation), pin.WorkerPodUID, int64(pin.WorkerEpoch), pin.ExecutorInstanceID); err != nil {
		return err
	}
	var generation, epoch int64
	var pod, instance string
	var stopped bool
	if err := tx.QueryRow(ctx, `SELECT generation,worker_pod_uid,worker_epoch,executor_instance_id,stopped FROM aenv_bridge.catalog_runtime_state WHERE tenant=$1 AND actor_uid=$2 FOR UPDATE`, pin.Tenant, pin.ActorUID).Scan(&generation, &pod, &epoch, &instance, &stopped); err != nil {
		return err
	}
	same := generation == int64(pin.Generation) && epoch == int64(pin.WorkerEpoch) && pod == pin.WorkerPodUID && instance == pin.ExecutorInstanceID
	if !same {
		if stopping || !stopped || generation >= int64(pin.Generation) {
			return fmt.Errorf("runtime pin fence mismatch or old allocation not stopped")
		}
	} else if stopped && !stopping {
		return fmt.Errorf("stopped runtime cannot acquire new pins")
	}
	_, err := tx.Exec(ctx, `UPDATE aenv_bridge.catalog_runtime_state SET generation=$3,worker_pod_uid=$4,worker_epoch=$5,executor_instance_id=$6,stopped=$7 WHERE tenant=$1 AND actor_uid=$2`, pin.Tenant, pin.ActorUID, int64(pin.Generation), pin.WorkerPodUID, int64(pin.WorkerEpoch), pin.ExecutorInstanceID, stopping)
	return err
}

// Unpin requires confirmed runtime termination or node fencing. Timeouts and
// lease expiry are not termination evidence and must never call this method.
func (c *Catalog) Unpin(ctx context.Context, pin Pin) error {
	if err := validatePin(pin); err != nil {
		return err
	}
	tx, err := c.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err := lockRuntime(ctx, tx, pin, true); err != nil {
		return err
	}
	rows, err := tx.Query(ctx, `SELECT digest FROM aenv_bridge.catalog_pins WHERE tenant=$1 AND actor_uid=$2 ORDER BY digest`, pin.Tenant, pin.ActorUID)
	if err != nil {
		return err
	}
	digests, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return err
	}
	for _, digest := range digests {
		if err := lockLayer(ctx, tx, digest); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(ctx, `DELETE FROM aenv_bridge.catalog_pins WHERE tenant=$1 AND actor_uid=$2 AND generation=$3 AND worker_pod_uid=$4 AND worker_epoch=$5 AND executor_instance_id=$6`, pin.Tenant, pin.ActorUID, int64(pin.Generation), pin.WorkerPodUID, int64(pin.WorkerEpoch), pin.ExecutorInstanceID); err != nil {
		return err
	}
	for _, digest := range digests {
		if err := c.deferGC(ctx, tx, digest); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

// ReleaseReservation is explicit cancellation after the control plane confirms
// it will not commit the operation. Uncertain operations retain references.
func (c *Catalog) ReleaseReservation(ctx context.Context, tenant, operation string) error {
	if err := identity(tenant, operation); err != nil {
		return err
	}
	tx, err := c.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var state string
	if err := tx.QueryRow(ctx, `SELECT state FROM aenv_bridge.catalog_operations WHERE tenant=$1 AND operation_id=$2 FOR UPDATE`, tenant, operation).Scan(&state); err != nil {
		return err
	}
	if state == "committed" {
		return fmt.Errorf("committed operation requires durable owner release")
	}
	if state == "released" {
		return tx.Commit(ctx)
	}
	digests, err := operationLayers(ctx, tx, tenant, operation)
	if err != nil {
		return err
	}
	for _, digest := range digests {
		if err := lockLayer(ctx, tx, digest); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(ctx, `DELETE FROM aenv_bridge.catalog_operation_refs WHERE tenant=$1 AND operation_id=$2`, tenant, operation); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE aenv_bridge.catalog_operations SET state='released' WHERE tenant=$1 AND operation_id=$2`, tenant, operation); err != nil {
		return err
	}
	for _, digest := range digests {
		if err := c.deferGC(ctx, tx, digest); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

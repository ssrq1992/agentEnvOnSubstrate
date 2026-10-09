package templateregistry

import (
	"bytes"
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"agentenv/services/aenv-api-bridge/internal/metadata"
	"github.com/jackc/pgx/v5"
)

//go:embed builds.sql
var buildsSchema string

type Build struct {
	Tenant, ID, RequestKey, Digest, State, Owner, Actor string
	Payload                                             json.RawMessage
	Generation                                          int64
	LeaseUntil                                          *time.Time
	CancelRequested                                     bool
	CreatedAt, UpdatedAt                                time.Time
}

func (s *Store) migrateBuilds(ctx context.Context) error {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(718304250)`); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `CREATE TABLE IF NOT EXISTS aenv_bridge.template_build_schema(version integer PRIMARY KEY,digest text NOT NULL)`); err != nil {
		return err
	}
	sum := sha256.Sum256([]byte(buildsSchema))
	digest := hex.EncodeToString(sum[:])
	var version int
	var stored string
	err = tx.QueryRow(ctx, `SELECT version,digest FROM aenv_bridge.template_build_schema ORDER BY version DESC LIMIT 1`).Scan(&version, &stored)
	if err == nil {
		if version != 1 || stored != digest {
			return fmt.Errorf("unsupported or modified template build migration")
		}
		return tx.Commit(ctx)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	if _, err = tx.Exec(ctx, buildsSchema); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO aenv_bridge.template_build_schema VALUES(1,$1)`, digest); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

const buildColumns = `tenant,build_id,request_key,payload,digest,state,generation,owner,lease_until,actor_name,cancel_requested,created_at,updated_at`

func scanBuild(row pgx.Row) (Build, error) {
	var b Build
	err := row.Scan(&b.Tenant, &b.ID, &b.RequestKey, &b.Payload, &b.Digest, &b.State, &b.Generation, &b.Owner, &b.LeaseUntil, &b.Actor, &b.CancelRequested, &b.CreatedAt, &b.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Build{}, metadata.ErrNotFound
	}
	if err != nil {
		return Build{}, err
	}
	canonical, err := canonicalBuildPayload(b.Payload)
	if err != nil {
		return Build{}, fmt.Errorf("invalid stored build input: %w", err)
	}
	sum := sha256.Sum256(canonical)
	if b.Digest != hex.EncodeToString(sum[:]) {
		return Build{}, fmt.Errorf("build input integrity failure")
	}
	return b, nil
}

// ReserveBuild freezes the complete resolved build input before allocation.
// A duplicate request key returns its original ID; changed input is a conflict.
func (s *Store) ReserveBuild(ctx context.Context, tenant, id, key string, payload json.RawMessage) (Build, error) {
	if !component(tenant) || !component(id) || !component(key) || len(payload) > 1<<20 {
		return Build{}, fmt.Errorf("invalid build identity or input")
	}
	// Preserve JSON numbers exactly (image sizes and numeric identifiers may
	// exceed float64 precision).
	var canonical json.RawMessage
	canonical, err := canonicalBuildPayload(payload)
	if err != nil {
		return Build{}, err
	}
	sum := sha256.Sum256(canonical)
	digest := hex.EncodeToString(sum[:])
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return Build{}, err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, "aenv-template-build:"+tenant); err != nil {
		return Build{}, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO aenv_bridge.template_build_jobs(tenant,build_id,request_key,payload,digest,state) VALUES($1,$2,$3,$4,$5,'queued') ON CONFLICT DO NOTHING`, tenant, id, key, canonical, digest); err != nil {
		return Build{}, err
	}
	b, err := scanBuild(tx.QueryRow(ctx, `SELECT `+buildColumns+` FROM aenv_bridge.template_build_jobs WHERE tenant=$1 AND request_key=$2`, tenant, key))
	if errors.Is(err, metadata.ErrNotFound) {
		return Build{}, metadata.ErrConflict
	}
	if err != nil {
		return Build{}, err
	}
	if b.Digest != digest {
		return Build{}, metadata.ErrConflict
	}
	if err = tx.Commit(ctx); err != nil {
		return Build{}, err
	}
	return b, nil
}

func (s *Store) Build(ctx context.Context, tenant, id string) (Build, error) {
	return scanBuild(s.Pool.QueryRow(ctx, `SELECT `+buildColumns+` FROM aenv_bridge.template_build_jobs WHERE tenant=$1 AND build_id=$2`, tenant, id))
}

// ClaimBuild only claims work that has never been dispatched. Expired work is
// not re-executed: its external operation must be inspected or fenced first.
func (s *Store) ClaimBuild(ctx context.Context, owner string, lease time.Duration) (Build, error) {
	if !component(owner) || lease < time.Second || lease > time.Hour {
		return Build{}, fmt.Errorf("invalid build lease")
	}
	return scanBuild(s.Pool.QueryRow(ctx, `UPDATE aenv_bridge.template_build_jobs SET state='allocating',owner=$1,generation=generation+1,lease_until=clock_timestamp()+$2*interval '1 millisecond',updated_at=clock_timestamp() WHERE (tenant,build_id)=(SELECT tenant,build_id FROM aenv_bridge.template_build_jobs WHERE state='queued' AND NOT cancel_requested ORDER BY created_at,tenant,build_id FOR UPDATE SKIP LOCKED LIMIT 1) RETURNING `+buildColumns, owner, lease.Milliseconds()))
}

func allowedBuildTransition(from, to string) bool {
	if to == "uncertain" || to == "failed" {
		return from == "allocating" || from == "executing" || from == "capturing" || from == "publishing"
	}
	return (from == "allocating" && to == "executing") || (from == "executing" && to == "capturing") || (from == "capturing" && to == "publishing") || (from == "publishing" && to == "ready")
}

// AdvanceBuild requires the active generation and lease. State changes persist
// an allocated Actor identity so reconciliation never allocates a second VM.
func (s *Store) AdvanceBuild(ctx context.Context, b Build, next, actor string, lease time.Duration) (Build, error) {
	if !allowedBuildTransition(b.State, next) || !component(b.Owner) || lease < time.Second || lease > time.Hour {
		return Build{}, fmt.Errorf("invalid build transition")
	}
	if actor == "" {
		actor = b.Actor
	}
	if next != "failed" && next != "uncertain" && !component(actor) {
		return Build{}, fmt.Errorf("allocated Actor required")
	}
	got, err := scanBuild(s.Pool.QueryRow(ctx, `UPDATE aenv_bridge.template_build_jobs SET state=$6,actor_name=$7,lease_until=clock_timestamp()+$8*interval '1 millisecond',updated_at=clock_timestamp() WHERE tenant=$1 AND build_id=$2 AND generation=$3 AND owner=$4 AND state=$5 AND lease_until>clock_timestamp() AND (actor_name='' OR actor_name=$7) AND (NOT cancel_requested OR $6 IN ('failed','uncertain')) RETURNING `+buildColumns, b.Tenant, b.ID, b.Generation, b.Owner, b.State, next, actor, lease.Milliseconds()))
	if errors.Is(err, metadata.ErrNotFound) {
		return Build{}, metadata.ErrConflict
	}
	return got, err
}

// RequestBuildCancellation never declares an in-flight Actor stopped. Its
// cleanup acknowledgement is a separate lifecycle operation.
func (s *Store) RequestBuildCancellation(ctx context.Context, tenant, id string) (Build, error) {
	return scanBuild(s.Pool.QueryRow(ctx, `UPDATE aenv_bridge.template_build_jobs SET cancel_requested=true,state=CASE WHEN state='queued' THEN 'cancelled' ELSE state END,updated_at=clock_timestamp() WHERE tenant=$1 AND build_id=$2 RETURNING `+buildColumns, tenant, id))
}

// ExpireBuildLeases retains Actor identity and prevents replay after a crash.
func (s *Store) ExpireBuildLeases(ctx context.Context) (int64, error) {
	result, err := s.Pool.Exec(ctx, `UPDATE aenv_bridge.template_build_jobs SET state='uncertain',updated_at=clock_timestamp() WHERE state IN ('allocating','executing','capturing','publishing') AND lease_until<=clock_timestamp()`)
	return result.RowsAffected(), err
}

func canonicalBuildPayload(payload []byte) (json.RawMessage, error) {
	if !json.Valid(payload) {
		return nil, fmt.Errorf("invalid build JSON")
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	if _, ok := value.(map[string]any); !ok {
		return nil, fmt.Errorf("build input must be an object")
	}
	return json.Marshal(value)
}

type BuildLog struct {
	Sequence         int64
	EventID, Message string
	CreatedAt        time.Time
}

// AppendBuildLog deduplicates retried events and fences late executor output.
func (s *Store) AppendBuildLog(ctx context.Context, b Build, event, message string) (BuildLog, error) {
	if !component(event) || len(message) > 16<<10 {
		return BuildLog{}, fmt.Errorf("invalid build log")
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return BuildLog{}, err
	}
	defer tx.Rollback(ctx)
	current, err := scanBuild(tx.QueryRow(ctx, `SELECT `+buildColumns+` FROM aenv_bridge.template_build_jobs WHERE tenant=$1 AND build_id=$2 FOR UPDATE`, b.Tenant, b.ID))
	if err != nil {
		return BuildLog{}, err
	}
	if current.Generation != b.Generation || current.Owner != b.Owner || current.Owner == "" {
		return BuildLog{}, metadata.ErrConflict
	}
	var l BuildLog
	err = tx.QueryRow(ctx, `SELECT sequence,event_id,message,created_at FROM aenv_bridge.template_build_logs WHERE tenant=$1 AND build_id=$2 AND event_id=$3`, b.Tenant, b.ID, event).Scan(&l.Sequence, &l.EventID, &l.Message, &l.CreatedAt)
	if err == nil {
		if l.Message != message {
			return BuildLog{}, metadata.ErrConflict
		}
		return l, tx.Commit(ctx)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return BuildLog{}, err
	}
	// Use database time for the lease comparison, like the state transitions.
	var active bool
	if err = tx.QueryRow(ctx, `SELECT lease_until>clock_timestamp() AND state IN ('allocating','executing','capturing','publishing') FROM aenv_bridge.template_build_jobs WHERE tenant=$1 AND build_id=$2`, b.Tenant, b.ID).Scan(&active); err != nil {
		return BuildLog{}, err
	}
	if !active {
		return BuildLog{}, metadata.ErrConflict
	}
	var count, size int64
	if err = tx.QueryRow(ctx, `SELECT count(*),coalesce(sum(octet_length(message)),0) FROM aenv_bridge.template_build_logs WHERE tenant=$1 AND build_id=$2`, b.Tenant, b.ID).Scan(&count, &size); err != nil {
		return BuildLog{}, err
	}
	if count >= 32768 || size+int64(len(message)) > 8<<20 {
		return BuildLog{}, fmt.Errorf("build log capacity exceeded")
	}
	err = tx.QueryRow(ctx, `INSERT INTO aenv_bridge.template_build_logs(tenant,build_id,sequence,event_id,message) VALUES($1,$2,$3,$4,$5) RETURNING sequence,event_id,message,created_at`, b.Tenant, b.ID, count+1, event, message).Scan(&l.Sequence, &l.EventID, &l.Message, &l.CreatedAt)
	if err != nil {
		return BuildLog{}, err
	}
	return l, tx.Commit(ctx)
}

func (s *Store) BuildLogs(ctx context.Context, tenant, id string, after int64, limit int) ([]BuildLog, error) {
	if after < 0 || limit < 1 || limit > 1000 {
		return nil, fmt.Errorf("invalid build log page")
	}
	if _, err := s.Build(ctx, tenant, id); err != nil {
		return nil, err
	}
	rows, err := s.Pool.Query(ctx, `SELECT sequence,event_id,message,created_at FROM aenv_bridge.template_build_logs WHERE tenant=$1 AND build_id=$2 AND sequence>$3 ORDER BY sequence LIMIT $4`, tenant, id, after, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []BuildLog{}
	for rows.Next() {
		var l BuildLog
		if err = rows.Scan(&l.Sequence, &l.EventID, &l.Message, &l.CreatedAt); err != nil {
			return nil, err
		}
		result = append(result, l)
	}
	return result, rows.Err()
}

// RenewBuild extends only a still-valid lease. An expired executor cannot
// revive its authority; cancellation may still need time to stop its Actor.
func (s *Store) RenewBuild(ctx context.Context, b Build, lease time.Duration) (Build, error) {
	if !component(b.Owner) || lease < time.Second || lease > time.Hour {
		return Build{}, fmt.Errorf("invalid build lease")
	}
	got, err := scanBuild(s.Pool.QueryRow(ctx, `UPDATE aenv_bridge.template_build_jobs SET lease_until=clock_timestamp()+$6*interval '1 millisecond',updated_at=clock_timestamp() WHERE tenant=$1 AND build_id=$2 AND generation=$3 AND owner=$4 AND state=$5 AND state IN ('allocating','executing','capturing','publishing') AND lease_until>clock_timestamp() RETURNING `+buildColumns, b.Tenant, b.ID, b.Generation, b.Owner, b.State, lease.Milliseconds()))
	if errors.Is(err, metadata.ErrNotFound) {
		return Build{}, metadata.ErrConflict
	}
	return got, err
}

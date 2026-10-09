// Package metadata stores compatibility identity and intent, never Worker
// placement or runtime status. Substrate remains authoritative for both.
package metadata

import (
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed schema.sql
var schema string

//go:embed suspend_schema.sql
var suspendSchema string

//go:embed resume_schema.sql
var resumeSchema string

//go:embed access_schema.sql
var accessSchema string

//go:embed autopause_schema.sql
var autoPauseSchema string

//go:embed creation_schema.sql
var creationSchema string

//go:embed fork_schema.sql
var forkSchema string

//go:embed extension_schema.sql
var extensionSchema string

//go:embed capture_schema.sql
var captureSchema string
var ErrConflict = errors.New("metadata precondition conflict")
var ErrNotFound = errors.New("sandbox not found")

type Store struct{ Pool *pgxpool.Pool }
type Sandbox struct {
	Tenant, ExternalID, ActorAtespace, ActorName, ActorUID, TemplateAlias string
	TimeoutSeconds                                                        int
	ExpiresAt                                                             time.Time
	CreatedAt                                                             time.Time
	Revision                                                              int64
	Deleted                                                               bool
}
type Request struct {
	Tenant, ID, ExternalID, Kind, Digest, State string
	Result                                      []byte
}

func identity(values ...string) error {
	for _, v := range values {
		if v == "" || len(v) > 256 || strings.ContainsAny(v, "\x00\r\n") {
			return fmt.Errorf("invalid metadata identity")
		}
	}
	return nil
}
func validateRequest(r Request) error {
	if err := identity(r.Tenant, r.ID, r.ExternalID); err != nil {
		return err
	}
	switch r.Kind {
	case "create", "delete", "suspend", "resume", "timeout", "fork", "capture", "policy", "extension":
	default:
		return fmt.Errorf("unknown request kind")
	}
	digest, err := hex.DecodeString(r.Digest)
	if err != nil || len(digest) != 32 || strings.ToLower(r.Digest) != r.Digest {
		return fmt.Errorf("canonical payload digest required")
	}
	return nil
}
func (s *Store) Migrate(ctx context.Context) error {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(718304242)"); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `CREATE SCHEMA IF NOT EXISTS aenv_bridge; CREATE TABLE IF NOT EXISTS aenv_bridge.metadata_schema(version integer PRIMARY KEY,digest text NOT NULL)`); err != nil {
		return err
	}

	migrations := []string{schema, suspendSchema, resumeSchema, accessSchema, autoPauseSchema, creationSchema, forkSchema, extensionSchema, captureSchema}
	rows, err := tx.Query(ctx, `SELECT version,digest FROM aenv_bridge.metadata_schema ORDER BY version`)
	if err != nil {
		return err
	}
	applied := 0
	for rows.Next() {
		var version int
		var digest string
		if err = rows.Scan(&version, &digest); err != nil {
			rows.Close()
			return err
		}
		if version != applied+1 || version > len(migrations) {
			rows.Close()
			return fmt.Errorf("unsupported metadata migration")
		}
		sum := sha256.Sum256([]byte(migrations[version-1]))
		if digest != hex.EncodeToString(sum[:]) {
			rows.Close()
			return fmt.Errorf("modified metadata migration")
		}
		applied++
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return err
	}
	for i := applied; i < len(migrations); i++ {
		if _, err = tx.Exec(ctx, migrations[i]); err != nil {
			return err
		}
		sum := sha256.Sum256([]byte(migrations[i]))
		if _, err = tx.Exec(ctx, `INSERT INTO aenv_bridge.metadata_schema VALUES($1,$2)`, i+1, hex.EncodeToString(sum[:])); err != nil {
			return err
		}
	}

	return tx.Commit(ctx)
}

// ReserveCreate commits identity and request intent before any control-plane
// call. A timed-out call leaves pending intent for reconciliation, never a
// second randomly named Actor. IDs and Actor names must be stable on retry.
func (s *Store) ReserveCreate(ctx context.Context, b Sandbox, r Request) (Request, error) {
	if err := validateRequest(r); err != nil {
		return Request{}, err
	}
	if err := identity(b.Tenant, b.ExternalID, b.ActorAtespace, b.ActorName, b.TemplateAlias); err != nil {
		return Request{}, err
	}
	if r.Kind != "create" || r.Tenant != b.Tenant || r.ExternalID != b.ExternalID || b.TimeoutSeconds < 0 || uint64(b.TimeoutSeconds) > 4294967295 {
		return Request{}, fmt.Errorf("invalid creation metadata")
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return Request{}, err
	}
	defer tx.Rollback(ctx)
	// The first transaction wins the immutable external-to-Actor mapping.
	_, err = tx.Exec(ctx, `INSERT INTO aenv_bridge.sandboxes(tenant,external_id,actor_atespace,actor_name,template_alias,timeout_seconds,expires_at) VALUES($1,$2,$3,$4,$5,$6,clock_timestamp()+make_interval(secs=>$6::bigint::double precision)) ON CONFLICT(tenant,external_id) DO NOTHING`, b.Tenant, b.ExternalID, b.ActorAtespace, b.ActorName, b.TemplateAlias, b.TimeoutSeconds)
	if err != nil {
		return Request{}, err
	}
	var existing Sandbox
	err = tx.QueryRow(ctx, `SELECT actor_atespace,actor_name,template_alias,timeout_seconds,deleted FROM aenv_bridge.sandboxes WHERE tenant=$1 AND external_id=$2 FOR UPDATE`, b.Tenant, b.ExternalID).Scan(&existing.ActorAtespace, &existing.ActorName, &existing.TemplateAlias, &existing.TimeoutSeconds, &existing.Deleted)
	if err != nil {
		return Request{}, err
	}
	if existing.ActorAtespace != b.ActorAtespace || existing.ActorName != b.ActorName || existing.TemplateAlias != b.TemplateAlias {
		return Request{}, ErrConflict
	}
	result, err := reserve(ctx, tx, r)
	if err != nil {
		return Request{}, err
	}
	if existing.Deleted && result.State == "pending" {
		return Request{}, ErrConflict
	}
	if err = tx.Commit(ctx); err != nil {
		return Request{}, err
	}
	return result, nil
}
func reserve(ctx context.Context, tx pgx.Tx, r Request) (Request, error) {
	_, err := tx.Exec(ctx, `INSERT INTO aenv_bridge.requests(tenant,request_id,external_id,kind,payload_digest) VALUES($1,$2,$3,$4,$5) ON CONFLICT(tenant,request_id) DO NOTHING`, r.Tenant, r.ID, r.ExternalID, r.Kind, r.Digest)
	if err != nil {
		return Request{}, err
	}
	stored := Request{Tenant: r.Tenant, ID: r.ID}
	err = tx.QueryRow(ctx, `SELECT external_id,kind,payload_digest,state,result FROM aenv_bridge.requests WHERE tenant=$1 AND request_id=$2`, r.Tenant, r.ID).Scan(&stored.ExternalID, &stored.Kind, &stored.Digest, &stored.State, &stored.Result)
	if err != nil {
		return Request{}, err
	}
	if stored.ExternalID != r.ExternalID || stored.Kind != r.Kind || stored.Digest != r.Digest {
		return Request{}, ErrConflict
	}
	return stored, nil
}
func (s *Store) Reserve(ctx context.Context, r Request) (Request, error) {
	if err := validateRequest(r); err != nil {
		return Request{}, err
	}
	if r.Kind == "create" {
		return Request{}, fmt.Errorf("creation requires ReserveCreate")
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return Request{}, err
	}
	defer tx.Rollback(ctx)
	var found bool
	err = tx.QueryRow(ctx, `SELECT true FROM aenv_bridge.sandboxes WHERE tenant=$1 AND external_id=$2 AND NOT deleted FOR UPDATE`, r.Tenant, r.ExternalID).Scan(&found)
	if errors.Is(err, pgx.ErrNoRows) {
		return Request{}, ErrNotFound
	}
	if err != nil {
		return Request{}, err
	}
	if r.Kind == "delete" {
		var held bool
		if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM aenv_bridge.requests WHERE tenant=$1 AND external_id=$2 AND kind='capture' AND state='pending')`, r.Tenant, r.ExternalID).Scan(&held); err != nil {
			return Request{}, err
		}
		if held {
			return Request{}, ErrConflict
		}
	}
	result, err := reserve(ctx, tx, r)
	if err != nil {
		return Request{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return Request{}, err
	}
	return result, nil
}

// Complete stores only a confirmed control-plane result. Transport failures
// must leave the request pending. Result bytes exclude bearer credentials.
func (s *Store) Complete(ctx context.Context, r Request, rejected bool, result []byte) error {
	if err := validateRequest(r); err != nil {
		return err
	}
	if len(result) > 1<<20 {
		return fmt.Errorf("result exceeds limit")
	}
	state := "completed"
	if rejected {
		state = "rejected"
	}
	tag, err := s.Pool.Exec(ctx, `UPDATE aenv_bridge.requests SET state=$6,result=$7,updated_at=clock_timestamp() WHERE tenant=$1 AND request_id=$2 AND external_id=$3 AND kind=$4 AND payload_digest=$5 AND (state='pending' OR (state=$6 AND result IS NOT DISTINCT FROM $7))`, r.Tenant, r.ID, r.ExternalID, r.Kind, r.Digest, state, result)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return ErrConflict
	}
	return nil
}
func (s *Store) Get(ctx context.Context, tenant, id string) (Sandbox, error) {
	if err := identity(tenant, id); err != nil {
		return Sandbox{}, err
	}
	b := Sandbox{Tenant: tenant, ExternalID: id}
	err := s.Pool.QueryRow(ctx, `SELECT actor_atespace,actor_name,COALESCE(actor_uid,''),template_alias,timeout_seconds,expires_at,revision,deleted,created_at FROM aenv_bridge.sandboxes WHERE tenant=$1 AND external_id=$2 AND NOT deleted`, tenant, id).Scan(&b.ActorAtespace, &b.ActorName, &b.ActorUID, &b.TemplateAlias, &b.TimeoutSeconds, &b.ExpiresAt, &b.Revision, &b.Deleted, &b.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Sandbox{}, ErrNotFound
	}
	return b, err
}

// BindActor is called only after Substrate returns the created Actor UID.
func (s *Store) BindActor(ctx context.Context, tenant, id, uid string) error {
	if err := identity(tenant, id, uid); err != nil {
		return err
	}
	tag, err := s.Pool.Exec(ctx, `UPDATE aenv_bridge.sandboxes SET actor_uid=$3 WHERE tenant=$1 AND external_id=$2 AND NOT deleted AND (actor_uid IS NULL OR actor_uid=$3)`, tenant, id, uid)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return ErrConflict
	}
	return nil
}

// Extend serializes against a durable expiry claim as well as the revision.
// Once deletion is claimed, an uncertain RPC must not allow resurrection. Expiry must be enforced through Substrate, never
// by deleting compatibility metadata or starting/stopping VMs directly.
func (s *Store) Extend(ctx context.Context, tenant, id string, revision int64, seconds int) error {
	if err := identity(tenant, id); err != nil {
		return err
	}
	if revision <= 0 || seconds < 0 || uint64(seconds) > 4294967295 {
		return fmt.Errorf("invalid timeout precondition")
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	// Lock separately: under READ COMMITTED the subsequent statement sees any
	// expiry decision committed while this transaction waited for the row.
	var current int64
	err = tx.QueryRow(ctx, `SELECT revision FROM aenv_bridge.sandboxes WHERE tenant=$1 AND external_id=$2 AND NOT deleted FOR UPDATE`, tenant, id).Scan(&current)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrConflict
	}
	if err != nil {
		return err
	}
	if current != revision {
		return ErrConflict
	}
	tag, err := tx.Exec(ctx, `UPDATE aenv_bridge.sandboxes SET timeout_seconds=$4,expires_at=clock_timestamp()+make_interval(secs=>$4::bigint::double precision),revision=revision+1 WHERE tenant=$1 AND external_id=$2 AND revision=$3 AND NOT deleted AND NOT EXISTS(SELECT 1 FROM aenv_bridge.expiry_claims e WHERE e.tenant=$1 AND e.external_id=$2) AND NOT EXISTS(SELECT 1 FROM aenv_bridge.suspend_intents p WHERE p.tenant=$1 AND p.external_id=$2) AND NOT EXISTS(SELECT 1 FROM aenv_bridge.resume_intents p WHERE p.tenant=$1 AND p.external_id=$2)`, tenant, id, revision, seconds)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return ErrConflict
	}
	return tx.Commit(ctx)
}

// ConfirmDeleted records an acknowledged control-plane deletion and the
// compatibility tombstone in one transaction. It must never be used merely
// because an HTTP or RPC deadline expired.
func (s *Store) ConfirmDeleted(ctx context.Context, r Request) error {
	if err := validateRequest(r); err != nil {
		return err
	}
	if r.Kind != "delete" {
		return fmt.Errorf("deletion intent required")
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var deleted bool
	err = tx.QueryRow(ctx, `SELECT deleted FROM aenv_bridge.sandboxes WHERE tenant=$1 AND external_id=$2 FOR UPDATE`, r.Tenant, r.ExternalID).Scan(&deleted)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	var digest, kind, external, state string
	err = tx.QueryRow(ctx, `SELECT payload_digest,kind,external_id,state FROM aenv_bridge.requests WHERE tenant=$1 AND request_id=$2 FOR UPDATE`, r.Tenant, r.ID).Scan(&digest, &kind, &external, &state)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrConflict
	}
	if err != nil {
		return err
	}
	if digest != r.Digest || kind != r.Kind || external != r.ExternalID || state == "rejected" {
		return ErrConflict
	}
	if state == "completed" && !deleted {
		return ErrConflict
	}
	if !deleted {
		if _, err = tx.Exec(ctx, `UPDATE aenv_bridge.sandboxes SET deleted=true,revision=revision+1 WHERE tenant=$1 AND external_id=$2`, r.Tenant, r.ExternalID); err != nil {
			return err
		}
	}
	if _, err = tx.Exec(ctx, `UPDATE aenv_bridge.requests SET state='completed',result=NULL,updated_at=clock_timestamp() WHERE tenant=$1 AND request_id=$2`, r.Tenant, r.ID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// Prepare freezes a pending request's non-secret execution preconditions before
// any remote mutation. Concurrent retries use the first committed preparation.
// Completed requests are returned unchanged and must not execute again.
func (s *Store) Prepare(ctx context.Context, r Request, prepared []byte) (Request, error) {
	if err := validateRequest(r); err != nil {
		return Request{}, err
	}
	if r.Kind != "policy" || len(prepared) == 0 || len(prepared) > 1<<20 {
		return Request{}, fmt.Errorf("invalid policy preparation")
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return Request{}, err
	}
	defer tx.Rollback(ctx)
	stored := Request{Tenant: r.Tenant, ID: r.ID}
	err = tx.QueryRow(ctx, `SELECT external_id,kind,payload_digest,state,result FROM aenv_bridge.requests WHERE tenant=$1 AND request_id=$2 FOR UPDATE`, r.Tenant, r.ID).Scan(&stored.ExternalID, &stored.Kind, &stored.Digest, &stored.State, &stored.Result)
	if errors.Is(err, pgx.ErrNoRows) {
		return Request{}, ErrNotFound
	}
	if err != nil {
		return Request{}, err
	}
	if stored.ExternalID != r.ExternalID || stored.Kind != r.Kind || stored.Digest != r.Digest {
		return Request{}, ErrConflict
	}
	if stored.State == "pending" && len(stored.Result) == 0 {
		_, err = tx.Exec(ctx, `UPDATE aenv_bridge.requests SET result=$3,updated_at=clock_timestamp() WHERE tenant=$1 AND request_id=$2`, r.Tenant, r.ID, prepared)
		if err != nil {
			return Request{}, err
		}
		stored.Result = append([]byte(nil), prepared...)
	}
	if err = tx.Commit(ctx); err != nil {
		return Request{}, err
	}
	return stored, nil
}

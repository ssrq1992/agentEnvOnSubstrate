package metadata

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"time"
)

type Capture struct {
	Request  Request
	Source   Sandbox
	Prepared []byte
}

func (s *Store) ReserveCapture(ctx context.Context, b Sandbox, r Request, prepared []byte, alias string) (Capture, error) {
	if err := validateRequest(r); err != nil {
		return Capture{}, err
	}
	if r.Kind != "capture" || r.Tenant != b.Tenant || r.ExternalID != b.ExternalID || b.ActorUID == "" || len(prepared) == 0 || len(prepared) > 1<<20 {
		return Capture{}, ErrConflict
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return Capture{}, err
	}
	defer tx.Rollback(ctx)
	var uid string
	err = tx.QueryRow(ctx, `SELECT actor_uid FROM aenv_bridge.sandboxes WHERE tenant=$1 AND external_id=$2 AND NOT deleted FOR UPDATE`, b.Tenant, b.ExternalID).Scan(&uid)
	if errors.Is(err, pgx.ErrNoRows) {
		return Capture{}, ErrNotFound
	}
	if err != nil {
		return Capture{}, err
	}
	if uid != b.ActorUID {
		return Capture{}, ErrConflict
	}
	var held bool
	err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM aenv_bridge.expiry_claims WHERE tenant=$1 AND external_id=$2) OR EXISTS(SELECT 1 FROM aenv_bridge.suspend_intents WHERE tenant=$1 AND external_id=$2) OR EXISTS(SELECT 1 FROM aenv_bridge.resume_intents WHERE tenant=$1 AND external_id=$2) OR EXISTS(SELECT 1 FROM aenv_bridge.requests WHERE tenant=$1 AND external_id=$2 AND state='pending' AND kind IN ('extension','capture','delete') AND request_id<>$3) OR EXISTS(SELECT 1 FROM aenv_bridge.fork_jobs j JOIN aenv_bridge.requests r USING(tenant,request_id) WHERE j.tenant=$1 AND j.external_id=$2 AND NOT j.captured AND r.state='pending')`, b.Tenant, b.ExternalID, r.ID).Scan(&held)
	if err != nil {
		return Capture{}, err
	}
	if held {
		return Capture{}, ErrConflict
	}
	stored, err := reserve(ctx, tx, r)
	if err != nil {
		return Capture{}, err
	}
	if alias != "" {
		if _, err = tx.Exec(ctx, `INSERT INTO aenv_bridge.snapshot_names VALUES($1,$2,$3) ON CONFLICT DO NOTHING`, r.Tenant, alias, r.ID); err != nil {
			return Capture{}, err
		}
		var owner string
		if err = tx.QueryRow(ctx, `SELECT request_id FROM aenv_bridge.snapshot_names WHERE tenant=$1 AND name=$2`, r.Tenant, alias).Scan(&owner); err != nil {
			return Capture{}, err
		}
		if owner != r.ID {
			return Capture{}, ErrConflict
		}
	}
	if _, err = tx.Exec(ctx, `INSERT INTO aenv_bridge.capture_jobs(tenant,request_id,external_id,actor_uid,prepared) VALUES($1,$2,$3,$4,$5) ON CONFLICT DO NOTHING`, r.Tenant, r.ID, r.ExternalID, b.ActorUID, prepared); err != nil {
		return Capture{}, err
	}
	out := Capture{Request: stored, Source: b}
	if err = tx.QueryRow(ctx, `SELECT prepared FROM aenv_bridge.capture_jobs WHERE tenant=$1 AND request_id=$2 AND actor_uid=$3`, r.Tenant, r.ID, b.ActorUID).Scan(&out.Prepared); err != nil {
		return Capture{}, err
	}
	return out, tx.Commit(ctx)
}
func (s *Store) Capture(ctx context.Context, r Request) (Capture, error) {
	if err := validateRequest(r); err != nil {
		return Capture{}, err
	}
	out := Capture{Request: r}
	out.Source.Tenant = r.Tenant
	out.Source.ExternalID = r.ExternalID
	err := s.Pool.QueryRow(ctx, `SELECT r.external_id,r.kind,r.payload_digest,r.state,r.result,j.actor_uid,j.prepared,s.actor_atespace,s.actor_name FROM aenv_bridge.capture_jobs j JOIN aenv_bridge.requests r USING(tenant,request_id) JOIN aenv_bridge.sandboxes s ON s.tenant=j.tenant AND s.external_id=j.external_id WHERE j.tenant=$1 AND j.request_id=$2`, r.Tenant, r.ID).Scan(&out.Request.ExternalID, &out.Request.Kind, &out.Request.Digest, &out.Request.State, &out.Request.Result, &out.Source.ActorUID, &out.Prepared, &out.Source.ActorAtespace, &out.Source.ActorName)
	if errors.Is(err, pgx.ErrNoRows) {
		return Capture{}, ErrNotFound
	}
	if err != nil {
		return Capture{}, err
	}
	if out.Request.ExternalID != r.ExternalID || out.Request.Kind != r.Kind || out.Request.Digest != r.Digest {
		return Capture{}, ErrConflict
	}
	return out, nil
}
func (s *Store) ConfirmCapture(ctx context.Context, i Capture, id, tagUID string, record json.RawMessage, created time.Time) error {
	if err := validateRequest(i.Request); err != nil {
		return err
	}
	if identity(id, tagUID) != nil || !json.Valid(record) || len(record) > 1<<20 || created.IsZero() {
		return ErrConflict
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var kind, digest, state, uid string
	err = tx.QueryRow(ctx, `SELECT r.kind,r.payload_digest,r.state,j.actor_uid FROM aenv_bridge.requests r JOIN aenv_bridge.capture_jobs j USING(tenant,request_id) WHERE r.tenant=$1 AND r.request_id=$2 FOR UPDATE OF r`, i.Request.Tenant, i.Request.ID).Scan(&kind, &digest, &state, &uid)
	if err != nil {
		return err
	}
	if kind != "capture" || digest != i.Request.Digest || uid != i.Source.ActorUID || state == "rejected" {
		return ErrConflict
	}
	if _, err = tx.Exec(ctx, `INSERT INTO aenv_bridge.snapshot_records VALUES($1,$2,$3,$4,$5,$6) ON CONFLICT DO NOTHING`, i.Request.Tenant, id, i.Request.ID, tagUID, record, created); err != nil {
		return err
	}
	var storedTag, storedID string
	var equal bool
	err = tx.QueryRow(ctx, `SELECT snapshot_id,tag_uid,record=$3::jsonb FROM aenv_bridge.snapshot_records WHERE tenant=$1 AND request_id=$2`, i.Request.Tenant, i.Request.ID, record).Scan(&storedID, &storedTag, &equal)
	if err != nil {
		return err
	}
	if storedID != id || storedTag != tagUID || !equal {
		return ErrConflict
	}
	if _, err = tx.Exec(ctx, `UPDATE aenv_bridge.requests SET state='completed',result=$3,updated_at=clock_timestamp() WHERE tenant=$1 AND request_id=$2`, i.Request.Tenant, i.Request.ID, record); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
func (s *Store) ClaimCaptures(ctx context.Context, limit int) ([]Capture, error) {
	if limit < 1 || limit > 100 {
		return nil, ErrConflict
	}
	rows, err := s.Pool.Query(ctx, `WITH claims AS (SELECT j.tenant,j.request_id FROM aenv_bridge.capture_jobs j JOIN aenv_bridge.requests r USING(tenant,request_id) WHERE r.state='pending' AND j.retry_after<=clock_timestamp() ORDER BY j.retry_after,j.tenant,j.request_id LIMIT $1 FOR UPDATE OF j SKIP LOCKED), picked AS (UPDATE aenv_bridge.capture_jobs j SET retry_after=clock_timestamp()+interval '30 seconds' FROM claims c WHERE j.tenant=c.tenant AND j.request_id=c.request_id RETURNING j.*) SELECT j.tenant,j.request_id,j.external_id,j.actor_uid,j.prepared,r.payload_digest FROM picked j JOIN aenv_bridge.requests r USING(tenant,request_id)`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Capture{}
	for rows.Next() {
		var c Capture
		c.Request.Kind = "capture"
		if err = rows.Scan(&c.Request.Tenant, &c.Request.ID, &c.Request.ExternalID, &c.Source.ActorUID, &c.Prepared, &c.Request.Digest); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}
func (s *Store) Snapshot(ctx context.Context, tenant, id string) (json.RawMessage, error) {
	var record json.RawMessage
	var err error
	if parsed, e := uuid.Parse(id); e == nil {
		err = s.Pool.QueryRow(ctx, `SELECT record FROM aenv_bridge.snapshot_records WHERE tenant=$1 AND snapshot_id=$2`, tenant, parsed.String()).Scan(&record)
	} else {
		err = s.Pool.QueryRow(ctx, `SELECT r.record FROM aenv_bridge.snapshot_records r JOIN aenv_bridge.snapshot_names n USING(tenant,request_id) WHERE r.tenant=$1 AND n.name=$2`, tenant, id).Scan(&record)
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return record, err
}
func (s *Store) SnapshotPage(ctx context.Context, tenant string, before *time.Time, id string, limit int) ([]json.RawMessage, error) {
	if limit < 1 || limit > 1000 {
		return nil, ErrConflict
	}
	rows, err := s.Pool.Query(ctx, `SELECT record FROM aenv_bridge.snapshot_records WHERE tenant=$1 AND ($2::timestamptz IS NULL OR (created_at,snapshot_id)<($2::timestamptz,$3::text)) ORDER BY created_at DESC,snapshot_id DESC LIMIT $4`, tenant, before, id, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []json.RawMessage{}
	for rows.Next() {
		var record json.RawMessage
		if err = rows.Scan(&record); err != nil {
			return nil, err
		}
		out = append(out, record)
	}
	return out, rows.Err()
}

func (s *Store) SnapshotPageFiltered(ctx context.Context, tenant string, before *time.Time, id string, limit int, source, name string) ([]json.RawMessage, error) {
	if limit < 1 || limit > 1000 {
		return nil, ErrConflict
	}
	rows, err := s.Pool.Query(ctx, `SELECT r.record FROM aenv_bridge.snapshot_records r WHERE r.tenant=$1 AND ($2::timestamptz IS NULL OR (r.created_at,r.snapshot_id)<($2::timestamptz,$3::text)) AND ($5='' OR r.record->'Source'->>'ExternalID'=$5) AND ($6='' OR r.snapshot_id=$6 OR EXISTS(SELECT 1 FROM aenv_bridge.snapshot_names n WHERE n.tenant=r.tenant AND n.request_id=r.request_id AND n.name=$6)) ORDER BY r.created_at DESC,r.snapshot_id DESC LIMIT $4`, tenant, before, id, limit, source, name)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []json.RawMessage{}
	for rows.Next() {
		var record json.RawMessage
		if err = rows.Scan(&record); err != nil {
			return nil, err
		}
		out = append(out, record)
	}
	return out, rows.Err()
}

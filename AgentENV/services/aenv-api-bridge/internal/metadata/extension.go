package metadata

import (
	"context"
	"github.com/jackc/pgx/v5"
)

type Extension struct {
	Request  Request
	Sandbox  Sandbox
	Prepared []byte
}

// ReserveExtension commits the frozen runtime request and expiry hold together.
// Sandbox row locking orders it against expiry, pause and online fork capture.
func (s *Store) ReserveExtension(ctx context.Context, b Sandbox, r Request, prepared []byte) (Extension, error) {
	if err := validateRequest(r); err != nil {
		return Extension{}, err
	}
	if r.Kind != "extension" || r.Tenant != b.Tenant || r.ExternalID != b.ExternalID || b.ActorUID == "" || len(prepared) == 0 || len(prepared) > 1<<20 {
		return Extension{}, ErrConflict
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return Extension{}, err
	}
	defer tx.Rollback(ctx)
	var uid, space, name string
	err = tx.QueryRow(ctx, `SELECT COALESCE(actor_uid,''),actor_atespace,actor_name FROM aenv_bridge.sandboxes WHERE tenant=$1 AND external_id=$2 AND NOT deleted FOR UPDATE`, b.Tenant, b.ExternalID).Scan(&uid, &space, &name)
	if err == pgx.ErrNoRows {
		return Extension{}, ErrNotFound
	}
	if err != nil {
		return Extension{}, err
	}
	if uid != b.ActorUID || space != b.ActorAtespace || name != b.ActorName {
		return Extension{}, ErrConflict
	}
	var held bool
	err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM aenv_bridge.expiry_claims WHERE tenant=$1 AND external_id=$2) OR EXISTS(SELECT 1 FROM aenv_bridge.suspend_intents WHERE tenant=$1 AND external_id=$2) OR EXISTS(SELECT 1 FROM aenv_bridge.resume_intents WHERE tenant=$1 AND external_id=$2) OR EXISTS(SELECT 1 FROM aenv_bridge.fork_jobs j JOIN aenv_bridge.requests r USING(tenant,request_id) WHERE j.tenant=$1 AND j.external_id=$2 AND NOT j.captured AND r.state='pending') OR EXISTS(SELECT 1 FROM aenv_bridge.requests WHERE tenant=$1 AND external_id=$2 AND kind IN ('extension','capture') AND state='pending' AND request_id<>$3)`, b.Tenant, b.ExternalID, r.ID).Scan(&held)
	if err != nil {
		return Extension{}, err
	}
	if held {
		return Extension{}, ErrConflict
	}
	stored, err := reserve(ctx, tx, r)
	if err != nil {
		return Extension{}, err
	}
	_, err = tx.Exec(ctx, `INSERT INTO aenv_bridge.extension_jobs(tenant,request_id,external_id,actor_uid,prepared) VALUES($1,$2,$3,$4,$5) ON CONFLICT DO NOTHING`, r.Tenant, r.ID, r.ExternalID, b.ActorUID, prepared)
	if err != nil {
		return Extension{}, err
	}
	out := Extension{Request: stored, Sandbox: b}
	err = tx.QueryRow(ctx, `SELECT prepared FROM aenv_bridge.extension_jobs WHERE tenant=$1 AND request_id=$2 AND actor_uid=$3`, r.Tenant, r.ID, b.ActorUID).Scan(&out.Prepared)
	if err != nil {
		return out, err
	}
	return out, tx.Commit(ctx)
}
func (s *Store) Extension(ctx context.Context, r Request) (Extension, error) {
	if err := validateRequest(r); err != nil {
		return Extension{}, err
	}
	out := Extension{Request: r}
	err := s.Pool.QueryRow(ctx, `SELECT r.external_id,r.kind,r.payload_digest,r.state,r.result,j.actor_uid,j.prepared,s.actor_atespace,s.actor_name FROM aenv_bridge.extension_jobs j JOIN aenv_bridge.requests r USING(tenant,request_id) JOIN aenv_bridge.sandboxes s ON s.tenant=j.tenant AND s.external_id=j.external_id WHERE j.tenant=$1 AND j.request_id=$2`, r.Tenant, r.ID).Scan(&out.Request.ExternalID, &out.Request.Kind, &out.Request.Digest, &out.Request.State, &out.Request.Result, &out.Sandbox.ActorUID, &out.Prepared, &out.Sandbox.ActorAtespace, &out.Sandbox.ActorName)
	if err == pgx.ErrNoRows {
		return out, ErrNotFound
	}
	if err != nil {
		return out, err
	}
	if out.Request.ExternalID != r.ExternalID || out.Request.Kind != r.Kind || out.Request.Digest != r.Digest {
		return out, ErrConflict
	}
	out.Sandbox.Tenant = r.Tenant
	out.Sandbox.ExternalID = r.ExternalID
	return out, nil
}
func (s *Store) ClaimExtensions(ctx context.Context, limit int) ([]Extension, error) {
	if limit < 1 || limit > 100 {
		return nil, ErrConflict
	}
	rows, err := s.Pool.Query(ctx, `WITH claims AS (SELECT j.tenant,j.request_id FROM aenv_bridge.extension_jobs j JOIN aenv_bridge.requests r USING(tenant,request_id) JOIN aenv_bridge.sandboxes s ON s.tenant=j.tenant AND s.external_id=j.external_id WHERE r.state='pending' AND NOT s.deleted AND j.retry_after<=clock_timestamp() ORDER BY j.retry_after LIMIT $1 FOR UPDATE OF j SKIP LOCKED), picked AS (UPDATE aenv_bridge.extension_jobs j SET retry_after=clock_timestamp()+interval '30 seconds' FROM claims c WHERE j.tenant=c.tenant AND j.request_id=c.request_id RETURNING j.*) SELECT j.tenant,j.request_id,j.external_id,j.actor_uid,j.prepared,r.payload_digest,r.state,r.result,s.actor_atespace,s.actor_name FROM picked j JOIN aenv_bridge.requests r USING(tenant,request_id) JOIN aenv_bridge.sandboxes s ON s.tenant=j.tenant AND s.external_id=j.external_id`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Extension
	for rows.Next() {
		var i Extension
		if err = rows.Scan(&i.Request.Tenant, &i.Request.ID, &i.Request.ExternalID, &i.Sandbox.ActorUID, &i.Prepared, &i.Request.Digest, &i.Request.State, &i.Request.Result, &i.Sandbox.ActorAtespace, &i.Sandbox.ActorName); err != nil {
			return nil, err
		}
		i.Request.Kind = "extension"
		i.Sandbox.Tenant = i.Request.Tenant
		i.Sandbox.ExternalID = i.Request.ExternalID
		out = append(out, i)
	}
	return out, rows.Err()
}

package metadata

import (
	"context"
	"github.com/jackc/pgx/v5"
)

type Fork struct {
	Request  Request
	ActorUID string
	Prepared []byte
	Captured bool
	Snapshot []byte
}

// ReserveFork freezes the parent allocation, child identities and launch intent
// before capture. Concurrent retries retain the first committed preparation.
func (s *Store) ReserveFork(ctx context.Context, b Sandbox, r Request, prepared []byte) (Fork, error) {
	if err := validateRequest(r); err != nil {
		return Fork{}, err
	}
	if r.Kind != "fork" || b.ActorUID == "" || r.Tenant != b.Tenant || r.ExternalID != b.ExternalID || len(prepared) == 0 || len(prepared) > 1<<20 {
		return Fork{}, ErrConflict
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return Fork{}, err
	}
	defer tx.Rollback(ctx)
	var uid string
	err = tx.QueryRow(ctx, `SELECT COALESCE(actor_uid,'') FROM aenv_bridge.sandboxes WHERE tenant=$1 AND external_id=$2 AND NOT deleted FOR UPDATE`, b.Tenant, b.ExternalID).Scan(&uid)
	if err == pgx.ErrNoRows {
		return Fork{}, ErrNotFound
	}
	if err != nil {
		return Fork{}, err
	}
	if uid != b.ActorUID {
		return Fork{}, ErrConflict
	}
	var held bool
	err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM aenv_bridge.requests x WHERE x.tenant=$1 AND x.external_id=$2 AND x.kind IN ('extension','capture') AND x.state='pending') OR EXISTS(SELECT 1 FROM aenv_bridge.expiry_claims WHERE tenant=$1 AND external_id=$2) OR EXISTS(SELECT 1 FROM aenv_bridge.suspend_intents WHERE tenant=$1 AND external_id=$2) OR EXISTS(SELECT 1 FROM aenv_bridge.resume_intents WHERE tenant=$1 AND external_id=$2)`, b.Tenant, b.ExternalID).Scan(&held)
	if err != nil {
		return Fork{}, err
	}
	if held {
		return Fork{}, ErrConflict
	}
	stored, err := reserve(ctx, tx, r)
	if err != nil {
		return Fork{}, err
	}
	_, err = tx.Exec(ctx, `INSERT INTO aenv_bridge.fork_jobs(tenant,request_id,external_id,actor_uid,prepared) VALUES($1,$2,$3,$4,$5) ON CONFLICT DO NOTHING`, r.Tenant, r.ID, r.ExternalID, b.ActorUID, prepared)
	if err != nil {
		return Fork{}, err
	}
	out := Fork{Request: stored}
	err = tx.QueryRow(ctx, `SELECT actor_uid,prepared,captured,snapshot FROM aenv_bridge.fork_jobs WHERE tenant=$1 AND request_id=$2`, r.Tenant, r.ID).Scan(&out.ActorUID, &out.Prepared, &out.Captured, &out.Snapshot)
	if err != nil {
		return out, err
	}
	if out.ActorUID != b.ActorUID {
		return out, ErrConflict
	}
	return out, tx.Commit(ctx)
}
func (s *Store) ConfirmForkCaptured(ctx context.Context, i Fork, snapshot []byte) error {
	if len(snapshot) == 0 || len(snapshot) > 1<<20 {
		return ErrConflict
	}
	tag, err := s.Pool.Exec(ctx, `UPDATE aenv_bridge.fork_jobs SET captured=true,snapshot=$4 WHERE tenant=$1 AND request_id=$2 AND actor_uid=$3 AND (snapshot IS NULL OR snapshot=$4)`, i.Request.Tenant, i.Request.ID, i.ActorUID, snapshot)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return ErrConflict
	}
	return nil
}
func (s *Store) ClaimForks(ctx context.Context, limit int) ([]Fork, error) {
	if limit < 1 || limit > 100 {
		return nil, ErrConflict
	}
	rows, err := s.Pool.Query(ctx, `WITH claims AS (SELECT j.tenant,j.request_id FROM aenv_bridge.fork_jobs j JOIN aenv_bridge.requests r USING(tenant,request_id) WHERE r.state='pending' AND j.retry_after<=clock_timestamp() ORDER BY j.retry_after LIMIT $1 FOR UPDATE OF j SKIP LOCKED), picked AS (UPDATE aenv_bridge.fork_jobs j SET retry_after=clock_timestamp()+interval '30 seconds' FROM claims c WHERE j.tenant=c.tenant AND j.request_id=c.request_id RETURNING j.*) SELECT j.tenant,j.request_id,j.external_id,j.actor_uid,j.prepared,j.captured,j.snapshot,r.payload_digest,r.state FROM picked j JOIN aenv_bridge.requests r USING(tenant,request_id)`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Fork
	for rows.Next() {
		var i Fork
		i.Request.Kind = "fork"
		if err := rows.Scan(&i.Request.Tenant, &i.Request.ID, &i.Request.ExternalID, &i.ActorUID, &i.Prepared, &i.Captured, &i.Snapshot, &i.Request.Digest, &i.Request.State); err != nil {
			return nil, err
		}
		out = append(out, i)
	}
	return out, rows.Err()
}

func (s *Store) Fork(ctx context.Context, r Request) (Fork, error) {
	if err := validateRequest(r); err != nil {
		return Fork{}, err
	}
	out := Fork{Request: r}
	var digest, external string
	err := s.Pool.QueryRow(ctx, `SELECT j.actor_uid,j.prepared,j.captured,j.snapshot,r.state,r.result,r.payload_digest,r.external_id FROM aenv_bridge.fork_jobs j JOIN aenv_bridge.requests r USING(tenant,request_id) WHERE j.tenant=$1 AND j.request_id=$2`, r.Tenant, r.ID).Scan(&out.ActorUID, &out.Prepared, &out.Captured, &out.Snapshot, &out.Request.State, &out.Request.Result, &digest, &external)
	if err == pgx.ErrNoRows {
		return out, ErrNotFound
	}
	if err != nil {
		return out, err
	}
	if digest != r.Digest || external != r.ExternalID {
		return out, ErrConflict
	}
	return out, nil
}

type ForkChild struct {
	Index  int
	ID     string
	Failed bool
}

func (s *Store) RecordForkChild(ctx context.Context, i Fork, child ForkChild) error {
	if child.Index < 0 || child.Index >= 100 || identity(child.ID) != nil {
		return ErrConflict
	}
	tag, err := s.Pool.Exec(ctx, `INSERT INTO aenv_bridge.fork_children(tenant,request_id,child_index,child_id,failed) SELECT $1,$2,$3,$4,$5 FROM aenv_bridge.requests WHERE tenant=$1 AND request_id=$2 AND external_id=$6 AND kind='fork' AND payload_digest=$7 AND state='pending' ON CONFLICT(tenant,request_id,child_index) DO UPDATE SET child_id=EXCLUDED.child_id WHERE fork_children.child_id=EXCLUDED.child_id AND fork_children.failed=EXCLUDED.failed`, i.Request.Tenant, i.Request.ID, child.Index, child.ID, child.Failed, i.Request.ExternalID, i.Request.Digest)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return ErrConflict
	}
	return nil
}
func (s *Store) ForkChildren(ctx context.Context, i Fork) ([]ForkChild, error) {
	rows, err := s.Pool.Query(ctx, `SELECT child_index,child_id,failed FROM aenv_bridge.fork_children WHERE tenant=$1 AND request_id=$2 ORDER BY child_index`, i.Request.Tenant, i.Request.ID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ForkChild
	for rows.Next() {
		var c ForkChild
		if err := rows.Scan(&c.Index, &c.ID, &c.Failed); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

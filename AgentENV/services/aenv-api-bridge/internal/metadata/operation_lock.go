package metadata

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// LockOperation serializes bridge creation and deletion across replicas. The
// transaction carries no uncommitted metadata writes while calling Substrate.
func (s *Store) LockOperation(ctx context.Context, tenant, id string) (func(), error) {
	if err := identity(tenant, id); err != nil {
		return nil, err
	}
	key, _ := json.Marshal([]string{tenant, id})
	// Lock waiters use dedicated connections so they cannot exhaust the data
	// pool and prevent the lock holder from committing its work.
	conn, err := pgx.ConnectConfig(ctx, s.Pool.Config().ConnConfig.Copy())
	if err != nil {
		return nil, err
	}
	closeConn := func() {
		closing, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = conn.Close(closing)
	}
	tx, err := conn.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		closeConn()
		return nil, err
	}
	rollback := func() {
		closing, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = tx.Rollback(closing)
	}
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, string(key)); err != nil {
		rollback()
		closeConn()
		return nil, err
	}
	return func() { rollback(); closeConn() }, nil
}
func (s *Store) Creation(ctx context.Context, tenant, id string) (Creation, error) {
	var i Creation
	i.Sandbox.Tenant = tenant
	i.Sandbox.ExternalID = id
	err := s.Pool.QueryRow(ctx, `SELECT s.actor_atespace,s.actor_name,COALESCE(s.actor_uid,''),s.template_alias,s.timeout_seconds,j.request_id,r.payload_digest,r.state,j.prepared FROM aenv_bridge.creation_jobs j JOIN aenv_bridge.sandboxes s USING(tenant,external_id) JOIN aenv_bridge.requests r ON r.tenant=j.tenant AND r.request_id=j.request_id WHERE s.tenant=$1 AND s.external_id=$2 AND NOT s.deleted`, tenant, id).Scan(&i.Sandbox.ActorAtespace, &i.Sandbox.ActorName, &i.Sandbox.ActorUID, &i.Sandbox.TemplateAlias, &i.Sandbox.TimeoutSeconds, &i.Request.ID, &i.Request.Digest, &i.Request.State, &i.Prepared)
	if err == pgx.ErrNoRows {
		return i, ErrNotFound
	}
	if err != nil {
		return i, err
	}
	i.Request.Tenant = tenant
	i.Request.ExternalID = id
	i.Request.Kind = "create"
	if i.Request.State == "rejected" {
		return i, fmt.Errorf("%w: creation rejected", ErrConflict)
	}
	return i, nil
}

package metadata

import (
	"context"
	"encoding/json"
	"github.com/jackc/pgx/v5"
)

// RejectSnapshotCreation stops reconciliation before attempting UID-bound
// cleanup. Only a pending child of the expected captured Tag can be rejected.
func (s *Store) RejectSnapshotCreation(ctx context.Context, b Sandbox, tagUID string) (Sandbox, error) {
	if b.Tenant == "" || b.ExternalID == "" || tagUID == "" {
		return Sandbox{}, ErrConflict
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return b, err
	}
	defer tx.Rollback(ctx)
	var state string
	var prepared []byte
	err = tx.QueryRow(ctx, `SELECT s.actor_atespace,s.actor_name,COALESCE(s.actor_uid,''),s.template_alias,s.deleted,r.state,j.prepared FROM aenv_bridge.sandboxes s JOIN aenv_bridge.creation_jobs j USING(tenant,external_id) JOIN aenv_bridge.requests r ON r.tenant=j.tenant AND r.request_id=j.request_id WHERE s.tenant=$1 AND s.external_id=$2 FOR UPDATE OF s,r`, b.Tenant, b.ExternalID).Scan(&b.ActorAtespace, &b.ActorName, &b.ActorUID, &b.TemplateAlias, &b.Deleted, &state, &prepared)
	if err == pgx.ErrNoRows {
		return b, ErrNotFound
	}
	if err != nil {
		return b, err
	}
	var plan struct{ SourceTagUID string }
	if err := json.Unmarshal(prepared, &plan); err != nil {
		return b, err
	}
	if plan.SourceTagUID != tagUID || b.ActorUID == "" || (state != "pending" && state != "rejected") {
		return b, ErrConflict
	}
	_, err = tx.Exec(ctx, `UPDATE aenv_bridge.requests r SET state='rejected',updated_at=clock_timestamp() FROM aenv_bridge.creation_jobs j WHERE r.tenant=j.tenant AND r.request_id=j.request_id AND j.tenant=$1 AND j.external_id=$2`, b.Tenant, b.ExternalID)
	if err != nil {
		return b, err
	}
	return b, tx.Commit(ctx)
}
func (s *Store) ConfirmSnapshotCreationFailed(ctx context.Context, b Sandbox) error {
	tag, err := s.Pool.Exec(ctx, `UPDATE aenv_bridge.sandboxes s SET deleted=true,revision=revision+CASE WHEN deleted THEN 0 ELSE 1 END WHERE tenant=$1 AND external_id=$2 AND actor_uid=$3 AND EXISTS(SELECT 1 FROM aenv_bridge.creation_jobs j JOIN aenv_bridge.requests r USING(tenant,request_id) WHERE j.tenant=s.tenant AND j.external_id=s.external_id AND r.state='rejected')`, b.Tenant, b.ExternalID, b.ActorUID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return ErrConflict
	}
	return nil
}

// SnapshotCreationConfirmed also recognizes completed children that expired or
// were deleted before the fork coordinator stored its child receipt.
func (s *Store) SnapshotCreationConfirmed(ctx context.Context, b Sandbox, tagUID string) (bool, error) {
	var prepared []byte
	var state string
	err := s.Pool.QueryRow(ctx, `SELECT j.prepared,r.state FROM aenv_bridge.creation_jobs j JOIN aenv_bridge.requests r USING(tenant,request_id) WHERE j.tenant=$1 AND j.external_id=$2`, b.Tenant, b.ExternalID).Scan(&prepared, &state)
	if err == pgx.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	var plan struct{ SourceTagUID string }
	if err := json.Unmarshal(prepared, &plan); err != nil {
		return false, err
	}
	if plan.SourceTagUID != tagUID {
		return false, ErrConflict
	}
	return state == "completed", nil
}

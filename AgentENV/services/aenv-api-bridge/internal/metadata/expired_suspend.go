package metadata

import (
	"context"
	"errors"
	"github.com/jackc/pgx/v5"
	"math"
)

// PrepareExpiredSuspend transfers the committed timeout claim into the same
// durable hold used by SDK pause. No interval exists in which resume/extension
// can rearm the timer while a suspension outcome is unknown.
func (s *Store) PrepareExpiredSuspend(ctx context.Context, claim Expiry, generation uint64) (SuspendIntent, error) {
	b, r := claim.Sandbox, claim.Request
	if r.Kind != "suspend" || r.Tenant != b.Tenant || r.ExternalID != b.ExternalID || b.ActorUID == "" || generation > math.MaxInt64 {
		return SuspendIntent{}, ErrConflict
	}
	if err := validateRequest(r); err != nil {
		return SuspendIntent{}, err
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return SuspendIntent{}, err
	}
	defer tx.Rollback(ctx)
	var uid string
	err = tx.QueryRow(ctx, `SELECT actor_uid FROM aenv_bridge.sandboxes WHERE tenant=$1 AND external_id=$2 AND NOT deleted FOR UPDATE`, b.Tenant, b.ExternalID).Scan(&uid)
	if errors.Is(err, pgx.ErrNoRows) {
		return SuspendIntent{}, ErrNotFound
	}
	if err != nil {
		return SuspendIntent{}, err
	}
	if uid != b.ActorUID {
		return SuspendIntent{}, ErrConflict
	}
	var storedID, digest, state string
	err = tx.QueryRow(ctx, `SELECT r.request_id,r.payload_digest,r.state FROM aenv_bridge.requests r WHERE r.tenant=$1 AND r.request_id=$2 AND r.external_id=$3 AND r.kind='suspend'`, r.Tenant, r.ID, r.ExternalID).Scan(&storedID, &digest, &state)
	if errors.Is(err, pgx.ErrNoRows) {
		return SuspendIntent{}, ErrConflict
	}
	if err != nil {
		return SuspendIntent{}, err
	}
	if digest != r.Digest || state == "rejected" {
		return SuspendIntent{}, ErrConflict
	}
	var existing int64
	err = tx.QueryRow(ctx, `SELECT assignment_generation FROM aenv_bridge.suspend_intents WHERE tenant=$1 AND external_id=$2 AND request_id=$3`, b.Tenant, b.ExternalID, r.ID).Scan(&existing)
	if err == nil {
		generation = uint64(existing)
	} else if errors.Is(err, pgx.ErrNoRows) {
		var current string
		err = tx.QueryRow(ctx, `SELECT request_id FROM aenv_bridge.expiry_claims WHERE tenant=$1 AND external_id=$2`, b.Tenant, b.ExternalID).Scan(&current)
		if errors.Is(err, pgx.ErrNoRows) {
			return SuspendIntent{}, ErrConflict
		}
		if err != nil {
			return SuspendIntent{}, err
		}
		if current != r.ID {
			return SuspendIntent{}, ErrConflict
		}
		if _, err = tx.Exec(ctx, `INSERT INTO aenv_bridge.suspend_intents VALUES($1,$2,$3,$4,clock_timestamp()+interval '30 seconds')`, b.Tenant, b.ExternalID, r.ID, int64(generation)); err != nil {
			return SuspendIntent{}, err
		}
		if _, err = tx.Exec(ctx, `DELETE FROM aenv_bridge.expiry_claims WHERE tenant=$1 AND external_id=$2 AND request_id=$3`, b.Tenant, b.ExternalID, r.ID); err != nil {
			return SuspendIntent{}, err
		}
	} else {
		return SuspendIntent{}, err
	}
	r.State = state
	if err = tx.Commit(ctx); err != nil {
		return SuspendIntent{}, err
	}
	return SuspendIntent{Sandbox: b, Request: r, Generation: generation}, nil
}

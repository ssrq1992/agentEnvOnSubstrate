package metadata

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/jackc/pgx/v5"
	"math"
)

type SuspendIntent struct {
	Sandbox    Sandbox
	Request    Request
	Generation uint64
}

// ReserveSuspend orders a persistent pause against expiry on the sandbox row.
// Calls for the same live allocation join a single intent, including after an
// HTTP or process failure. It holds expiry until an explicit resume commits.
func (s *Store) ReserveSuspend(ctx context.Context, b Sandbox, generation uint64) (SuspendIntent, error) {
	if err := identity(b.Tenant, b.ExternalID, b.ActorUID, b.ActorAtespace, b.ActorName); err != nil {
		return SuspendIntent{}, err
	}
	if generation > math.MaxInt64 {
		return SuspendIntent{}, fmt.Errorf("invalid assignment generation")
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return SuspendIntent{}, err
	}
	defer tx.Rollback(ctx)
	var uid, space, name string
	err = tx.QueryRow(ctx, `SELECT actor_uid,actor_atespace,actor_name FROM aenv_bridge.sandboxes WHERE tenant=$1 AND external_id=$2 AND NOT deleted FOR UPDATE`, b.Tenant, b.ExternalID).Scan(&uid, &space, &name)
	if errors.Is(err, pgx.ErrNoRows) {
		return SuspendIntent{}, ErrNotFound
	}
	if err != nil {
		return SuspendIntent{}, err
	}
	if uid != b.ActorUID || space != b.ActorAtespace || name != b.ActorName {
		return SuspendIntent{}, ErrConflict
	}
	var claimed bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM aenv_bridge.requests x WHERE x.tenant=$1 AND x.external_id=$2 AND x.kind IN ('extension','capture') AND x.state='pending') OR EXISTS(SELECT 1 FROM aenv_bridge.expiry_claims WHERE tenant=$1 AND external_id=$2) OR EXISTS(SELECT 1 FROM aenv_bridge.fork_jobs j JOIN aenv_bridge.requests r USING(tenant,request_id) WHERE j.tenant=$1 AND j.external_id=$2 AND NOT j.captured AND r.state='pending') OR EXISTS(SELECT 1 FROM aenv_bridge.resume_intents WHERE tenant=$1 AND external_id=$2)`, b.Tenant, b.ExternalID).Scan(&claimed); err != nil {
		return SuspendIntent{}, err
	}
	if claimed {
		return SuspendIntent{}, ErrConflict
	}
	intent := SuspendIntent{Sandbox: b, Generation: generation, Request: Request{Tenant: b.Tenant, ExternalID: b.ExternalID, Kind: "suspend"}}
	var storedGeneration int64
	err = tx.QueryRow(ctx, `SELECT p.assignment_generation,r.request_id,r.payload_digest,r.state FROM aenv_bridge.suspend_intents p JOIN aenv_bridge.requests r ON r.tenant=p.tenant AND r.request_id=p.request_id WHERE p.tenant=$1 AND p.external_id=$2`, b.Tenant, b.ExternalID).Scan(&storedGeneration, &intent.Request.ID, &intent.Request.Digest, &intent.Request.State)
	if err == nil {
		if generation != 0 && uint64(storedGeneration) != generation {
			return SuspendIntent{}, ErrConflict
		}
		intent.Generation = uint64(storedGeneration)
	} else if errors.Is(err, pgx.ErrNoRows) {
		payload, _ := json.Marshal(struct {
			Tenant, ID, UID string
			Generation      uint64
		}{b.Tenant, b.ExternalID, b.ActorUID, generation})
		sum := sha256.Sum256(payload)
		intent.Request.Digest = hex.EncodeToString(sum[:])
		intent.Request.ID = "suspend-" + intent.Request.Digest
		intent.Request, err = reserve(ctx, tx, intent.Request)
		if err != nil {
			return SuspendIntent{}, err
		}
		_, err = tx.Exec(ctx, `INSERT INTO aenv_bridge.suspend_intents VALUES($1,$2,$3,$4,clock_timestamp()+interval '30 seconds')`, b.Tenant, b.ExternalID, intent.Request.ID, int64(generation))
		if err != nil {
			return SuspendIntent{}, err
		}
	} else {
		return SuspendIntent{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return SuspendIntent{}, err
	}
	return intent, nil
}
func (s *Store) ConfirmSuspended(ctx context.Context, intent SuspendIntent) error {
	if err := validateRequest(intent.Request); err != nil {
		return err
	}
	if intent.Request.Kind != "suspend" || intent.Request.Tenant != intent.Sandbox.Tenant || intent.Request.ExternalID != intent.Sandbox.ExternalID || intent.Generation > math.MaxInt64 {
		return ErrConflict
	}
	// The hold must still be current. A late completion after resume must not
	// reinstall a timer hold or overwrite another lifecycle receipt.
	tag, err := s.Pool.Exec(ctx, `UPDATE aenv_bridge.requests r SET state='completed',result=NULL,updated_at=clock_timestamp() FROM aenv_bridge.suspend_intents p,aenv_bridge.sandboxes b WHERE r.tenant=$1 AND r.request_id=$2 AND r.external_id=$3 AND r.payload_digest=$4 AND r.kind='suspend' AND r.state IN ('pending','completed') AND p.tenant=r.tenant AND p.external_id=r.external_id AND p.request_id=r.request_id AND p.assignment_generation=$5 AND b.tenant=p.tenant AND b.external_id=p.external_id AND b.actor_uid=$6 AND NOT b.deleted`, intent.Request.Tenant, intent.Request.ID, intent.Request.ExternalID, intent.Request.Digest, int64(intent.Generation), intent.Sandbox.ActorUID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return ErrConflict
	}
	return nil
}
func (s *Store) ClaimSuspends(ctx context.Context, limit int) ([]SuspendIntent, error) {
	if limit < 1 || limit > 1000 {
		return nil, fmt.Errorf("invalid suspend batch")
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	rows, err := tx.Query(ctx, `SELECT b.tenant,b.external_id,b.actor_atespace,b.actor_name,b.actor_uid,p.request_id,p.assignment_generation,r.payload_digest FROM aenv_bridge.suspend_intents p JOIN aenv_bridge.sandboxes b USING(tenant,external_id) JOIN aenv_bridge.requests r ON r.tenant=p.tenant AND r.request_id=p.request_id WHERE NOT b.deleted AND r.state='pending' AND p.retry_after<=clock_timestamp() ORDER BY p.retry_after LIMIT $1 FOR UPDATE OF p SKIP LOCKED`, limit)
	if err != nil {
		return nil, err
	}
	var out []SuspendIntent
	for rows.Next() {
		var i SuspendIntent
		var generation int64
		if err = rows.Scan(&i.Sandbox.Tenant, &i.Sandbox.ExternalID, &i.Sandbox.ActorAtespace, &i.Sandbox.ActorName, &i.Sandbox.ActorUID, &i.Request.ID, &generation, &i.Request.Digest); err != nil {
			rows.Close()
			return nil, err
		}
		i.Generation = uint64(generation)
		i.Request.Tenant = i.Sandbox.Tenant
		i.Request.ExternalID = i.Sandbox.ExternalID
		i.Request.Kind = "suspend"
		i.Request.State = "pending"
		out = append(out, i)
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return nil, err
	}
	for _, i := range out {
		if _, err = tx.Exec(ctx, `UPDATE aenv_bridge.suspend_intents SET retry_after=clock_timestamp()+interval '30 seconds' WHERE tenant=$1 AND external_id=$2`, i.Sandbox.Tenant, i.Sandbox.ExternalID); err != nil {
			return nil, err
		}
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}
	return out, nil
}

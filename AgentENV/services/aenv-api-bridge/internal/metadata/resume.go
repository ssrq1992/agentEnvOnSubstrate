package metadata

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/jackc/pgx/v5"
)

type ResumeIntent struct {
	Sandbox    Sandbox
	Request    Request
	SourceURI  string
	Timeout    int
	ExtendOnly bool
}

func (s *Store) ReserveResume(ctx context.Context, b Sandbox, source string, seconds int, extendOnly bool) (ResumeIntent, error) {
	if err := identity(b.Tenant, b.ExternalID, b.ActorUID, b.ActorAtespace, b.ActorName); err != nil {
		return ResumeIntent{}, err
	}
	if seconds < 0 || uint64(seconds) > 4294967295 || len(source) > 4096 {
		return ResumeIntent{}, fmt.Errorf("invalid resume preparation")
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return ResumeIntent{}, err
	}
	defer tx.Rollback(ctx)
	var uid, space, name string
	var revision int64
	err = tx.QueryRow(ctx, `SELECT actor_uid,actor_atespace,actor_name,revision FROM aenv_bridge.sandboxes WHERE tenant=$1 AND external_id=$2 AND NOT deleted FOR UPDATE`, b.Tenant, b.ExternalID).Scan(&uid, &space, &name, &revision)
	if errors.Is(err, pgx.ErrNoRows) {
		return ResumeIntent{}, ErrNotFound
	}
	if err != nil {
		return ResumeIntent{}, err
	}
	if uid != b.ActorUID || space != b.ActorAtespace || name != b.ActorName {
		return ResumeIntent{}, ErrConflict
	}
	var blocked bool
	err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM aenv_bridge.expiry_claims WHERE tenant=$1 AND external_id=$2) OR EXISTS(SELECT 1 FROM aenv_bridge.suspend_intents p JOIN aenv_bridge.requests r ON r.tenant=p.tenant AND r.request_id=p.request_id WHERE p.tenant=$1 AND p.external_id=$2 AND r.state!='completed')`, b.Tenant, b.ExternalID).Scan(&blocked)
	if err != nil {
		return ResumeIntent{}, err
	}
	if blocked {
		return ResumeIntent{}, ErrConflict
	}
	intent := ResumeIntent{Sandbox: b, SourceURI: source, Timeout: seconds, ExtendOnly: extendOnly, Request: Request{Tenant: b.Tenant, ExternalID: b.ExternalID, Kind: "resume"}}
	var existingSource string
	var existingTimeout int
	var existingExtend bool
	err = tx.QueryRow(ctx, `SELECT p.source_snapshot_uri,p.timeout_seconds,p.extend_only,r.request_id,r.payload_digest,r.state FROM aenv_bridge.resume_intents p JOIN aenv_bridge.requests r ON r.tenant=p.tenant AND r.request_id=p.request_id WHERE p.tenant=$1 AND p.external_id=$2`, b.Tenant, b.ExternalID).Scan(&existingSource, &existingTimeout, &existingExtend, &intent.Request.ID, &intent.Request.Digest, &intent.Request.State)
	if err == nil {
		if existingSource != source || existingTimeout != seconds {
			return ResumeIntent{}, ErrConflict
		}
		// After an unknown successful resume, the Actor may already be RUNNING.
		// Keep the original replace-vs-extend decision instead of changing payload.
		intent.ExtendOnly = existingExtend
	} else if errors.Is(err, pgx.ErrNoRows) {
		payload, _ := json.Marshal(struct {
			Tenant, ID, UID, Source string
			Revision                int64
			Timeout                 int
			ExtendOnly              bool
		}{b.Tenant, b.ExternalID, b.ActorUID, source, revision, seconds, extendOnly})
		sum := sha256.Sum256(payload)
		intent.Request.Digest = hex.EncodeToString(sum[:])
		intent.Request.ID = "resume-" + intent.Request.Digest
		intent.Request, err = reserve(ctx, tx, intent.Request)
		if err != nil {
			return ResumeIntent{}, err
		}
		_, err = tx.Exec(ctx, `INSERT INTO aenv_bridge.resume_intents VALUES($1,$2,$3,$4,$5,$6,clock_timestamp()+interval '30 seconds')`, b.Tenant, b.ExternalID, intent.Request.ID, source, seconds, extendOnly)
		if err != nil {
			return ResumeIntent{}, err
		}
	} else {
		return ResumeIntent{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return ResumeIntent{}, err
	}
	return intent, nil
}
func (s *Store) ConfirmResumed(ctx context.Context, i ResumeIntent) error {
	if err := validateRequest(i.Request); err != nil {
		return err
	}
	if i.Request.Kind != "resume" || i.Request.Tenant != i.Sandbox.Tenant || i.Request.ExternalID != i.Sandbox.ExternalID {
		return ErrConflict
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var uid string
	err = tx.QueryRow(ctx, `SELECT actor_uid FROM aenv_bridge.sandboxes WHERE tenant=$1 AND external_id=$2 AND NOT deleted FOR UPDATE`, i.Sandbox.Tenant, i.Sandbox.ExternalID).Scan(&uid)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if uid != i.Sandbox.ActorUID {
		return ErrConflict
	}
	var state, digest string
	err = tx.QueryRow(ctx, `SELECT state,payload_digest FROM aenv_bridge.requests WHERE tenant=$1 AND request_id=$2 AND external_id=$3 AND kind='resume' FOR UPDATE`, i.Request.Tenant, i.Request.ID, i.Request.ExternalID).Scan(&state, &digest)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrConflict
	}
	if err != nil {
		return err
	}
	if digest != i.Request.Digest {
		return ErrConflict
	}
	if state == "completed" {
		return tx.Commit(ctx)
	}
	if state != "pending" {
		return ErrConflict
	}
	var current bool
	err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM aenv_bridge.resume_intents WHERE tenant=$1 AND external_id=$2 AND request_id=$3 AND source_snapshot_uri=$4 AND timeout_seconds=$5 AND extend_only=$6)`, i.Request.Tenant, i.Request.ExternalID, i.Request.ID, i.SourceURI, i.Timeout, i.ExtendOnly).Scan(&current)
	if err != nil {
		return err
	}
	if !current {
		return ErrConflict
	}
	_, err = tx.Exec(ctx, `WITH deadline AS (SELECT clock_timestamp()+make_interval(secs=>$3::bigint::double precision) AS value) UPDATE aenv_bridge.sandboxes SET timeout_seconds=CASE WHEN $4 AND expires_at>=deadline.value THEN timeout_seconds ELSE $3 END,expires_at=CASE WHEN $4 THEN GREATEST(expires_at,deadline.value) ELSE deadline.value END,revision=revision+1 FROM deadline WHERE tenant=$1 AND external_id=$2`, i.Request.Tenant, i.Request.ExternalID, i.Timeout, i.ExtendOnly)
	if err != nil {
		return err
	}
	for _, table := range []string{"suspend_intents", "resume_intents"} {
		if _, err = tx.Exec(ctx, `DELETE FROM aenv_bridge.`+table+` WHERE tenant=$1 AND external_id=$2`, i.Request.Tenant, i.Request.ExternalID); err != nil {
			return err
		}
	}
	_, err = tx.Exec(ctx, `UPDATE aenv_bridge.requests SET state='completed',result=NULL,updated_at=clock_timestamp() WHERE tenant=$1 AND request_id=$2`, i.Request.Tenant, i.Request.ID)
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Store) ClaimResumes(ctx context.Context, limit int) ([]ResumeIntent, error) {
	if limit < 1 || limit > 1000 {
		return nil, fmt.Errorf("invalid resume batch")
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	rows, err := tx.Query(ctx, `SELECT b.tenant,b.external_id,b.actor_atespace,b.actor_name,b.actor_uid,p.request_id,p.source_snapshot_uri,p.timeout_seconds,p.extend_only,r.payload_digest FROM aenv_bridge.resume_intents p JOIN aenv_bridge.sandboxes b USING(tenant,external_id) JOIN aenv_bridge.requests r ON r.tenant=p.tenant AND r.request_id=p.request_id WHERE NOT b.deleted AND r.state='pending' AND p.retry_after<=clock_timestamp() ORDER BY p.retry_after LIMIT $1 FOR UPDATE OF p SKIP LOCKED`, limit)
	if err != nil {
		return nil, err
	}
	var out []ResumeIntent
	for rows.Next() {
		var i ResumeIntent
		if err = rows.Scan(&i.Sandbox.Tenant, &i.Sandbox.ExternalID, &i.Sandbox.ActorAtespace, &i.Sandbox.ActorName, &i.Sandbox.ActorUID, &i.Request.ID, &i.SourceURI, &i.Timeout, &i.ExtendOnly, &i.Request.Digest); err != nil {
			rows.Close()
			return nil, err
		}
		i.Request.Tenant = i.Sandbox.Tenant
		i.Request.ExternalID = i.Sandbox.ExternalID
		i.Request.Kind = "resume"
		i.Request.State = "pending"
		out = append(out, i)
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return nil, err
	}
	for _, i := range out {
		if _, err = tx.Exec(ctx, `UPDATE aenv_bridge.resume_intents SET retry_after=clock_timestamp()+interval '30 seconds' WHERE tenant=$1 AND external_id=$2`, i.Sandbox.Tenant, i.Sandbox.ExternalID); err != nil {
			return nil, err
		}
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}
	return out, nil
}

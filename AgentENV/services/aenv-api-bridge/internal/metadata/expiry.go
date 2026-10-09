package metadata

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
)

type Expiry struct {
	Sandbox Sandbox
	Request Request
}

// ClaimExpired persists deletion intent before contacting Substrate. A retry
// returns the same intent, including after a process restart or unknown RPC
// outcome. Row locks order this decision against Extend; no wall-clock race
// can delete a sandbox whose extension committed first.
func (s *Store) ClaimExpired(ctx context.Context, limit int) ([]Expiry, error) {
	if limit < 1 || limit > 1000 {
		return nil, fmt.Errorf("expiry batch must be between 1 and 1000")
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	rows, err := tx.Query(ctx, `SELECT s.tenant,s.external_id,s.actor_atespace,s.actor_name,s.actor_uid,s.template_alias,s.timeout_seconds,s.expires_at,s.revision FROM aenv_bridge.sandboxes s LEFT JOIN aenv_bridge.expiry_claims e ON e.tenant=s.tenant AND e.external_id=s.external_id WHERE NOT s.deleted AND NOT EXISTS(SELECT 1 FROM aenv_bridge.requests x WHERE x.tenant=s.tenant AND x.external_id=s.external_id AND x.kind IN ('extension','capture') AND x.state='pending') AND NOT EXISTS(SELECT 1 FROM aenv_bridge.requests c JOIN aenv_bridge.creation_jobs j ON j.tenant=c.tenant AND j.request_id=c.request_id WHERE c.tenant=s.tenant AND c.external_id=s.external_id AND c.kind='create' AND c.state='pending') AND NOT EXISTS(SELECT 1 FROM aenv_bridge.fork_jobs j JOIN aenv_bridge.requests r USING(tenant,request_id) WHERE j.tenant=s.tenant AND j.external_id=s.external_id AND NOT j.captured AND r.state='pending') AND NOT EXISTS(SELECT 1 FROM aenv_bridge.suspend_intents p WHERE p.tenant=s.tenant AND p.external_id=s.external_id) AND NOT EXISTS(SELECT 1 FROM aenv_bridge.resume_intents p WHERE p.tenant=s.tenant AND p.external_id=s.external_id) AND s.actor_uid IS NOT NULL AND s.expires_at<=clock_timestamp() AND (e.retry_after IS NULL OR e.retry_after<=clock_timestamp()) ORDER BY s.expires_at,s.tenant,s.external_id LIMIT $1 FOR UPDATE OF s SKIP LOCKED`, limit)
	if err != nil {
		return nil, err
	}
	var result []Expiry
	for rows.Next() {
		var b Sandbox
		if err = rows.Scan(&b.Tenant, &b.ExternalID, &b.ActorAtespace, &b.ActorName, &b.ActorUID, &b.TemplateAlias, &b.TimeoutSeconds, &b.ExpiresAt, &b.Revision); err != nil {
			rows.Close()
			return nil, err
		}
		result = append(result, Expiry{Sandbox: b})
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return nil, err
	}
	// Recheck with a fresh READ COMMITTED snapshot after taking the sandbox
	// locks: a pause may have committed just after the selection snapshot.
	eligible := result[:0]
	for _, claim := range result {
		var held bool
		if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM aenv_bridge.requests x WHERE x.tenant=$1 AND x.external_id=$2 AND x.kind IN ('extension','capture') AND x.state='pending') OR EXISTS(SELECT 1 FROM aenv_bridge.requests c JOIN aenv_bridge.creation_jobs j ON j.tenant=c.tenant AND j.request_id=c.request_id WHERE c.tenant=$1 AND c.external_id=$2 AND c.kind='create' AND c.state='pending') OR EXISTS(SELECT 1 FROM aenv_bridge.fork_jobs j JOIN aenv_bridge.requests r USING(tenant,request_id) WHERE j.tenant=$1 AND j.external_id=$2 AND NOT j.captured AND r.state='pending') OR EXISTS(SELECT 1 FROM aenv_bridge.suspend_intents WHERE tenant=$1 AND external_id=$2) OR EXISTS(SELECT 1 FROM aenv_bridge.resume_intents WHERE tenant=$1 AND external_id=$2)`, claim.Sandbox.Tenant, claim.Sandbox.ExternalID).Scan(&held); err != nil {
			return nil, err
		}
		if !held {
			eligible = append(eligible, claim)
		}
	}
	result = eligible
	for i := range result {
		b := result[i].Sandbox
		payload, _ := json.Marshal(struct {
			Tenant, ID, UID string
			Revision        int64
		}{b.Tenant, b.ExternalID, b.ActorUID, b.Revision})
		sum := sha256.Sum256(payload)
		digest := hex.EncodeToString(sum[:])
		kind := "delete"
		var autoPause bool
		if err = tx.QueryRow(ctx, `SELECT COALESCE((SELECT auto_pause FROM aenv_bridge.sandbox_access WHERE tenant=$1 AND external_id=$2),false)`, b.Tenant, b.ExternalID).Scan(&autoPause); err != nil {
			return nil, err
		}
		if autoPause {
			kind = "suspend"
		}
		r := Request{Tenant: b.Tenant, ExternalID: b.ExternalID, ID: "expiry-" + digest, Kind: kind, Digest: digest}
		r, err = reserve(ctx, tx, r)
		if err != nil {
			return nil, err
		}
		if r.State != "pending" {
			return nil, ErrConflict
		}
		_, err = tx.Exec(ctx, `INSERT INTO aenv_bridge.expiry_claims(tenant,external_id,request_id,retry_after) VALUES($1,$2,$3,clock_timestamp()+interval '30 seconds') ON CONFLICT(tenant,external_id) DO UPDATE SET retry_after=EXCLUDED.retry_after`, b.Tenant, b.ExternalID, r.ID)
		if err != nil {
			return nil, err
		}
		var stored string
		err = tx.QueryRow(ctx, `SELECT request_id FROM aenv_bridge.expiry_claims WHERE tenant=$1 AND external_id=$2`, b.Tenant, b.ExternalID).Scan(&stored)
		if err != nil {
			return nil, err
		}
		if stored != r.ID {
			return nil, ErrConflict
		}
		result[i].Request = r
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}
	return result, nil
}

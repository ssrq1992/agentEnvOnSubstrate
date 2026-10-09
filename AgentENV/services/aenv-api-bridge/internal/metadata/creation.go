package metadata

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/jackc/pgx/v5"
)

// Profile is compatibility presentation data. Runtime state remains in Substrate.
type Profile struct {
	TemplateID  string            `json:"templateID"`
	Alias       string            `json:"alias,omitempty"`
	EnvdVersion string            `json:"envdVersion"`
	CPUCount    int               `json:"cpuCount"`
	MemoryMB    int               `json:"memoryMB"`
	DiskSizeMB  int               `json:"diskSizeMB"`
	Metadata    map[string]string `json:"metadata,omitempty"`
}
type Creation struct {
	Sandbox  Sandbox
	Request  Request
	Prepared []byte
}

// ReserveCreation atomically retains a full non-secret preparation before any
// remote template/Actor mutation. The pending create also holds timeout expiry.
func (s *Store) ReserveCreation(ctx context.Context, b Sandbox, r Request, prepared []byte) (Creation, error) {
	if err := validateRequest(r); err != nil {
		return Creation{}, err
	}
	if r.Kind != "create" || r.Tenant != b.Tenant || r.ExternalID != b.ExternalID || len(prepared) == 0 || len(prepared) > 1<<20 || b.TimeoutSeconds < 0 || uint64(b.TimeoutSeconds) > 4294967295 {
		return Creation{}, ErrConflict
	}
	if err := identity(b.Tenant, b.ExternalID, b.ActorAtespace, b.ActorName, b.TemplateAlias); err != nil {
		return Creation{}, err
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return Creation{}, err
	}
	defer tx.Rollback(ctx)
	_, err = tx.Exec(ctx, `INSERT INTO aenv_bridge.sandboxes(tenant,external_id,actor_atespace,actor_name,template_alias,timeout_seconds,expires_at) VALUES($1,$2,$3,$4,$5,$6,clock_timestamp()+make_interval(secs=>$6::bigint::double precision)) ON CONFLICT(tenant,external_id) DO NOTHING`, b.Tenant, b.ExternalID, b.ActorAtespace, b.ActorName, b.TemplateAlias, b.TimeoutSeconds)
	if err != nil {
		return Creation{}, err
	}
	var name, space, alias string
	var deleted bool
	err = tx.QueryRow(ctx, `SELECT actor_name,actor_atespace,template_alias,deleted FROM aenv_bridge.sandboxes WHERE tenant=$1 AND external_id=$2 FOR UPDATE`, b.Tenant, b.ExternalID).Scan(&name, &space, &alias, &deleted)
	if err != nil {
		return Creation{}, err
	}
	if name != b.ActorName || space != b.ActorAtespace || alias != b.TemplateAlias || deleted {
		return Creation{}, ErrConflict
	}
	stored, err := reserve(ctx, tx, r)
	if err != nil {
		return Creation{}, err
	}
	if stored.State == "rejected" {
		return Creation{}, ErrConflict
	}
	_, err = tx.Exec(ctx, `INSERT INTO aenv_bridge.creation_jobs(tenant,external_id,request_id,prepared) VALUES($1,$2,$3,$4) ON CONFLICT(tenant,external_id) DO NOTHING`, b.Tenant, b.ExternalID, r.ID, prepared)
	if err != nil {
		return Creation{}, err
	}
	var requestID string
	var frozen []byte
	err = tx.QueryRow(ctx, `SELECT request_id,prepared FROM aenv_bridge.creation_jobs WHERE tenant=$1 AND external_id=$2`, b.Tenant, b.ExternalID).Scan(&requestID, &frozen)
	if err != nil {
		return Creation{}, err
	}
	if requestID != r.ID {
		return Creation{}, ErrConflict
	}
	if err = tx.Commit(ctx); err != nil {
		return Creation{}, err
	}
	return Creation{Sandbox: b, Request: stored, Prepared: frozen}, nil
}
func (s *Store) ConfirmCreated(ctx context.Context, i Creation, uid string, p Profile) error {
	if uid == "" || i.Request.Kind != "create" || p.CPUCount < 1 || p.MemoryMB < 1 || p.DiskSizeMB < 1 || p.EnvdVersion == "" {
		return ErrConflict
	}
	profile, err := json.Marshal(p)
	if err != nil {
		return err
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var currentUID, state, digest string
	err = tx.QueryRow(ctx, `SELECT COALESCE(s.actor_uid,''),r.state,r.payload_digest FROM aenv_bridge.sandboxes s JOIN aenv_bridge.requests r ON r.tenant=s.tenant AND r.external_id=s.external_id WHERE s.tenant=$1 AND s.external_id=$2 AND r.request_id=$3 AND r.kind='create' AND NOT s.deleted FOR UPDATE OF s,r`, i.Sandbox.Tenant, i.Sandbox.ExternalID, i.Request.ID).Scan(&currentUID, &state, &digest)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrConflict
	}
	if err != nil {
		return err
	}
	if currentUID != uid || digest != i.Request.Digest || state == "rejected" {
		return ErrConflict
	}
	if state == "completed" {
		return tx.Commit(ctx)
	}
	if _, err = tx.Exec(ctx, `INSERT INTO aenv_bridge.sandbox_profiles VALUES($1,$2,$3)`, i.Sandbox.Tenant, i.Sandbox.ExternalID, profile); err != nil {
		return err
	}
	// TTL starts when the runtime is confirmed, not while assets are downloaded.
	if _, err = tx.Exec(ctx, `UPDATE aenv_bridge.sandboxes SET expires_at=clock_timestamp()+make_interval(secs=>timeout_seconds::double precision),revision=revision+1 WHERE tenant=$1 AND external_id=$2`, i.Sandbox.Tenant, i.Sandbox.ExternalID); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `UPDATE aenv_bridge.requests SET state='completed',result=NULL,updated_at=clock_timestamp() WHERE tenant=$1 AND request_id=$2`, i.Request.Tenant, i.Request.ID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
func (s *Store) ClaimCreations(ctx context.Context, limit int) ([]Creation, error) {
	if limit < 1 || limit > 100 {
		return nil, fmt.Errorf("invalid creation batch")
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	rows, err := tx.Query(ctx, `SELECT s.tenant,s.external_id,s.actor_atespace,s.actor_name,COALESCE(s.actor_uid,''),s.template_alias,s.timeout_seconds,j.request_id,r.payload_digest,j.prepared FROM aenv_bridge.creation_jobs j JOIN aenv_bridge.sandboxes s USING(tenant,external_id) JOIN aenv_bridge.requests r ON r.tenant=j.tenant AND r.request_id=j.request_id WHERE NOT s.deleted AND r.state='pending' AND j.retry_after<=clock_timestamp() ORDER BY j.retry_after LIMIT $1 FOR UPDATE OF j SKIP LOCKED`, limit)
	if err != nil {
		return nil, err
	}
	var out []Creation
	for rows.Next() {
		var i Creation
		err = rows.Scan(&i.Sandbox.Tenant, &i.Sandbox.ExternalID, &i.Sandbox.ActorAtespace, &i.Sandbox.ActorName, &i.Sandbox.ActorUID, &i.Sandbox.TemplateAlias, &i.Sandbox.TimeoutSeconds, &i.Request.ID, &i.Request.Digest, &i.Prepared)
		if err != nil {
			rows.Close()
			return nil, err
		}
		i.Request.Tenant = i.Sandbox.Tenant
		i.Request.ExternalID = i.Sandbox.ExternalID
		i.Request.Kind = "create"
		i.Request.State = "pending"
		out = append(out, i)
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return nil, err
	}
	for _, i := range out {
		if _, err = tx.Exec(ctx, `UPDATE aenv_bridge.creation_jobs SET retry_after=clock_timestamp()+interval '30 seconds' WHERE tenant=$1 AND external_id=$2`, i.Sandbox.Tenant, i.Sandbox.ExternalID); err != nil {
			return nil, err
		}
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}
	return out, nil
}
func (s *Store) Profile(ctx context.Context, b Sandbox) (Profile, error) {
	var p Profile
	var raw []byte
	err := s.Pool.QueryRow(ctx, `SELECT p.profile FROM aenv_bridge.sandbox_profiles p JOIN aenv_bridge.sandboxes s USING(tenant,external_id) WHERE s.tenant=$1 AND s.external_id=$2 AND s.actor_uid=$3 AND NOT s.deleted`, b.Tenant, b.ExternalID, b.ActorUID).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return p, ErrNotFound
	}
	if err != nil {
		return p, err
	}
	return p, json.Unmarshal(raw, &p)
}

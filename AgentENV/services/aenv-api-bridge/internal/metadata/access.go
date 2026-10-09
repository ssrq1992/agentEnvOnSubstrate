package metadata

import (
	"context"
	"errors"
	"fmt"
	"github.com/jackc/pgx/v5"
)

// Access contains immutable creation options, not runtime location or state.
type Access struct {
	AutoPause          bool `json:"autoPause"`
	Secure             bool `json:"secure"`
	AllowPublicTraffic bool `json:"allowPublicTraffic"`
	AutoResume         bool `json:"autoResume"`
	EnvdPort           int  `json:"envdPort"`
}

func (s *Store) PutAccess(ctx context.Context, b Sandbox, a Access) error {
	if b.ActorUID == "" || a.EnvdPort < 1 || a.EnvdPort > 65535 {
		return fmt.Errorf("invalid access configuration")
	}
	// A retry may confirm the same settings, but cannot silently change them.
	tag, err := s.Pool.Exec(ctx, `INSERT INTO aenv_bridge.sandbox_access(tenant,external_id,secure,allow_public_traffic,auto_resume,envd_port,auto_pause)
 SELECT tenant,external_id,$4,$5,$6,$7,$8 FROM aenv_bridge.sandboxes WHERE tenant=$1 AND external_id=$2 AND actor_uid=$3 AND NOT deleted
 ON CONFLICT(tenant,external_id) DO UPDATE SET secure=EXCLUDED.secure
 WHERE sandbox_access.secure=EXCLUDED.secure AND sandbox_access.allow_public_traffic=EXCLUDED.allow_public_traffic AND sandbox_access.auto_resume=EXCLUDED.auto_resume AND sandbox_access.envd_port=EXCLUDED.envd_port AND sandbox_access.auto_pause=EXCLUDED.auto_pause`, b.Tenant, b.ExternalID, b.ActorUID, a.Secure, a.AllowPublicTraffic, a.AutoResume, a.EnvdPort, a.AutoPause)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return ErrConflict
	}
	return nil
}
func (s *Store) Access(ctx context.Context, b Sandbox) (Access, error) {
	var a Access
	err := s.Pool.QueryRow(ctx, `SELECT a.secure,a.allow_public_traffic,a.auto_resume,a.envd_port,a.auto_pause FROM aenv_bridge.sandbox_access a JOIN aenv_bridge.sandboxes s USING(tenant,external_id) WHERE s.tenant=$1 AND s.external_id=$2 AND s.actor_uid=$3 AND NOT s.deleted`, b.Tenant, b.ExternalID, b.ActorUID).Scan(&a.Secure, &a.AllowPublicTraffic, &a.AutoResume, &a.EnvdPort, &a.AutoPause)
	if errors.Is(err, pgx.ErrNoRows) {
		return a, ErrNotFound
	}
	return a, err
}

// LookupExternal fails closed when a host name is ambiguous across tenants.
// The authenticated token is checked after lookup; it never selects a tenant.
func (s *Store) LookupExternal(ctx context.Context, id string) (Sandbox, error) {
	if err := identity(id); err != nil {
		return Sandbox{}, ErrNotFound
	}
	rows, err := s.Pool.Query(ctx, `SELECT tenant FROM aenv_bridge.sandboxes WHERE external_id=$1 AND NOT deleted AND actor_uid IS NOT NULL AND NOT EXISTS(SELECT 1 FROM aenv_bridge.requests c JOIN aenv_bridge.creation_jobs j ON j.tenant=c.tenant AND j.request_id=c.request_id WHERE c.tenant=sandboxes.tenant AND c.external_id=sandboxes.external_id AND c.kind='create' AND c.state='pending') LIMIT 2`, id)
	if err != nil {
		return Sandbox{}, err
	}
	var tenants []string
	for rows.Next() {
		var tenant string
		if err = rows.Scan(&tenant); err != nil {
			rows.Close()
			return Sandbox{}, err
		}
		tenants = append(tenants, tenant)
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return Sandbox{}, err
	}
	if len(tenants) != 1 {
		return Sandbox{}, ErrNotFound
	}
	return s.Get(ctx, tenants[0], id)
}

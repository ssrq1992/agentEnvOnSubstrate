package metadata

import (
	"context"
	"fmt"
)

// ListPage reads only confirmed compatibility records. Placement/state are
// fetched from Substrate by the API, never inferred from these rows.
func (s *Store) ListPage(ctx context.Context, tenant, after string, limit int) ([]Sandbox, error) {
	if tenant == "" || limit < 1 || limit > 1000 {
		return nil, fmt.Errorf("invalid sandbox list page")
	}
	rows, err := s.Pool.Query(ctx, `SELECT s.external_id,s.actor_atespace,s.actor_name,s.actor_uid,s.template_alias,s.timeout_seconds,s.expires_at,s.revision,s.created_at FROM aenv_bridge.sandboxes s JOIN aenv_bridge.sandbox_profiles p USING(tenant,external_id) WHERE s.tenant=$1 AND s.external_id>$2 AND NOT s.deleted ORDER BY s.external_id LIMIT $3`, tenant, after, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Sandbox
	for rows.Next() {
		b := Sandbox{Tenant: tenant}
		if err = rows.Scan(&b.ExternalID, &b.ActorAtespace, &b.ActorName, &b.ActorUID, &b.TemplateAlias, &b.TimeoutSeconds, &b.ExpiresAt, &b.Revision, &b.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

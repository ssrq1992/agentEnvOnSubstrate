package lifecycle

import (
	"agentenv/services/aenv-api-bridge/internal/auth"
	"agentenv/services/aenv-api-bridge/internal/metadata"
	"context"
)

// PauseExpired first freezes the allocation in the durable timeout claim.
// The regular lifecycle reconciler retries the resulting suspension hold.
func (s *Service) PauseExpired(ctx context.Context, t auth.Tenant, claim metadata.Expiry) error {
	if claim.Sandbox.Tenant != t.ID || claim.Sandbox.ActorAtespace != t.Atespace {
		return metadata.ErrConflict
	}
	generation, err := s.Control.SuspendGeneration(ctx, t, claim.Sandbox)
	if err != nil {
		return err
	}
	i, err := s.Store.PrepareExpiredSuspend(ctx, claim, generation)
	if err != nil {
		return err
	}
	if i.Request.State == "completed" {
		return nil
	}
	return s.execute(ctx, t, i)
}

package lifecycle

import (
	"agentenv/services/aenv-api-bridge/internal/auth"
	"agentenv/services/aenv-api-bridge/internal/metadata"
	"context"
	"fmt"
	pb "github.com/agent-substrate/substrate/pkg/proto/ateapipb"
)

func (s *Service) Connect(ctx context.Context, t auth.Tenant, b metadata.Sandbox, seconds int, onlyPaused bool) (*pb.ConnectActorResponse, bool, error) {
	if s.Store == nil || s.Control == nil {
		return nil, false, fmt.Errorf("lifecycle dependencies required")
	}
	if t.ID == "" || t.ID != b.Tenant || t.Atespace != b.ActorAtespace {
		return nil, false, metadata.ErrNotFound
	}
	source, running, err := s.Control.ResumeSource(ctx, t, b)
	if err != nil {
		return nil, false, err
	}
	if onlyPaused && running {
		return nil, false, metadata.ErrConflict
	}
	intent, err := s.Store.ReserveResume(ctx, b, source, seconds, running)
	if err != nil {
		return nil, false, err
	}
	if err = s.executeResume(ctx, t, intent); err != nil {
		return nil, false, err
	}
	// Tokens are fetched after committing the timer and never enter a receipt.
	connection, err := s.Control.Connect(ctx, t, b)
	return connection, !intent.ExtendOnly, err
}
func (s *Service) executeResume(ctx context.Context, t auth.Tenant, i metadata.ResumeIntent) error {
	if i.Sandbox.Tenant != t.ID || i.Sandbox.ActorAtespace != t.Atespace || i.Sandbox.ActorUID == "" || i.Request.Tenant != t.ID || i.Request.ExternalID != i.Sandbox.ExternalID || i.Request.Kind != "resume" {
		return metadata.ErrConflict
	}
	if i.Request.State == "completed" {
		return nil
	}
	if i.Request.State != "pending" {
		return metadata.ErrConflict
	}
	if err := s.Control.ResumePrepared(ctx, t, i); err != nil {
		return err
	}
	return s.Store.ConfirmResumed(ctx, i)
}

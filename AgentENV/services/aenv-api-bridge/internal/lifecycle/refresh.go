package lifecycle

import (
	"context"
	"fmt"

	"agentenv/services/aenv-api-bridge/internal/auth"
	"agentenv/services/aenv-api-bridge/internal/metadata"
	pb "github.com/agent-substrate/substrate/pkg/proto/ateapipb"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type RefreshStore interface {
	LockOperation(context.Context, string, string) (func(), error)
	Get(context.Context, string, string) (metadata.Sandbox, error)
	RefreshMinimum(context.Context, metadata.Sandbox, int) error
}
type RefreshControl interface {
	Get(context.Context, auth.Tenant, metadata.Sandbox) (*pb.Actor, error)
}
type RefreshService struct {
	Store          RefreshStore
	Control        RefreshControl
	DefaultTimeout int
}

func (s *RefreshService) Refresh(ctx context.Context, t auth.Tenant, b metadata.Sandbox, duration *int) error {
	if s.Store == nil || s.Control == nil || s.DefaultTimeout < 0 || uint64(s.DefaultTimeout) > 4294967295 {
		return fmt.Errorf("refresh dependencies required")
	}
	if t.ID == "" || b.Tenant != t.ID || b.ActorAtespace != t.Atespace || b.ActorUID == "" {
		return metadata.ErrNotFound
	}
	seconds := s.DefaultTimeout
	if duration != nil {
		seconds = *duration
	}
	if seconds < 0 || uint64(seconds) > 4294967295 {
		return status.Error(codes.InvalidArgument, "invalid refresh duration")
	}
	unlock, err := s.Store.LockOperation(ctx, t.ID, b.ExternalID)
	if err != nil {
		return err
	}
	defer unlock()
	current, err := s.Store.Get(ctx, t.ID, b.ExternalID)
	if err != nil {
		return err
	}
	if current.ActorUID != b.ActorUID || current.ActorAtespace != t.Atespace || current.ActorName != b.ActorName {
		return metadata.ErrConflict
	}
	actor, err := s.Control.Get(ctx, t, current)
	if err != nil {
		return err
	}
	if actor.GetMetadata().GetUid() != current.ActorUID {
		return metadata.ErrNotFound
	}
	if actor.GetStatus().GetState() != pb.ActorState_ACTOR_STATE_RUNNING {
		return status.Error(codes.FailedPrecondition, "only running sandbox can be refreshed")
	}
	return s.Store.RefreshMinimum(ctx, current, seconds)
}

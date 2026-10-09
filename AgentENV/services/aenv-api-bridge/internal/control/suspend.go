package control

import (
	"agentenv/services/aenv-api-bridge/internal/auth"
	bridge "agentenv/services/aenv-api-bridge/internal/metadata"
	"context"
	"fmt"
	pb "github.com/agent-substrate/substrate/pkg/proto/ateapipb"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func (c *Client) SuspendGeneration(ctx context.Context, t auth.Tenant, b bridge.Sandbox) (uint64, error) {
	if b.ActorUID == "" {
		return 0, fmt.Errorf("confirmed Actor UID required")
	}
	actor, err := c.Get(ctx, t, b)
	if err != nil {
		return 0, err
	}
	switch actor.GetStatus().GetState() {
	case pb.ActorState_ACTOR_STATE_SUSPENDED:
		return 0, nil
	case pb.ActorState_ACTOR_STATE_RUNNING, pb.ActorState_ACTOR_STATE_SUSPENDING:
		generation := actor.GetStatus().GetWorkerAssignment().GetAssignmentGeneration()
		if generation == 0 {
			return 0, status.Error(codes.FailedPrecondition, "native assignment required")
		}
		return generation, nil
	default:
		return 0, status.Error(codes.FailedPrecondition, "Actor cannot be suspended in current state")
	}
}
func (c *Client) SuspendPrepared(ctx context.Context, t auth.Tenant, intent bridge.SuspendIntent) error {
	b := intent.Sandbox
	if b.ActorUID == "" {
		return fmt.Errorf("confirmed Actor UID required")
	}
	ctx, err := tenantContext(ctx, t, b)
	if err != nil {
		return err
	}
	generation := intent.Generation
	response, err := c.RPC.SuspendActor(ctx, &pb.SuspendActorRequest{Actor: ref(b), Uid: b.ActorUID, AssignmentGeneration: &generation})
	if err != nil {
		return err
	}
	actor := response.GetActor()
	if err = checkIdentity(actor, b); err != nil {
		return err
	}
	if actor.GetStatus().GetState() != pb.ActorState_ACTOR_STATE_SUSPENDED || actor.GetStatus().GetExternalSnapshot().GetSnapshotUri() == "" {
		return status.Error(codes.Unavailable, "persistent suspend was not confirmed")
	}
	return nil
}

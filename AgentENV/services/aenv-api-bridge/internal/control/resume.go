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

func (c *Client) ResumeSource(ctx context.Context, t auth.Tenant, b bridge.Sandbox) (string, bool, error) {
	if b.ActorUID == "" {
		return "", false, fmt.Errorf("confirmed Actor UID required")
	}
	actor, err := c.Get(ctx, t, b)
	if err != nil {
		return "", false, err
	}
	source := actor.GetStatus().GetExternalSnapshot().GetSnapshotUri()
	switch actor.GetStatus().GetState() {
	case pb.ActorState_ACTOR_STATE_RUNNING:
		return source, true, nil
	case pb.ActorState_ACTOR_STATE_SUSPENDED, pb.ActorState_ACTOR_STATE_RESUMING:
		if source == "" {
			return "", false, status.Error(codes.FailedPrecondition, "persistent source snapshot required")
		}
		return source, false, nil
	default:
		return "", false, status.Error(codes.FailedPrecondition, "Actor cannot resume in current state")
	}
}
func (c *Client) ResumePrepared(ctx context.Context, t auth.Tenant, i bridge.ResumeIntent) error {
	b := i.Sandbox
	if b.ActorUID == "" {
		return fmt.Errorf("confirmed Actor UID required")
	}
	ctx, err := tenantContext(ctx, t, b)
	if err != nil {
		return err
	}
	source := i.SourceURI
	response, err := c.RPC.ResumeActor(ctx, &pb.ResumeActorRequest{Actor: ref(b), Uid: b.ActorUID, SourceSnapshotUri: &source})
	if err != nil {
		return err
	}
	actor := response.GetActor()
	if err = checkIdentity(actor, b); err != nil {
		return err
	}
	if actor.GetStatus().GetState() != pb.ActorState_ACTOR_STATE_RUNNING || actor.GetStatus().GetWorkerAssignment().GetAssignmentGeneration() == 0 {
		return status.Error(codes.Unavailable, "running resume allocation was not confirmed")
	}
	return nil
}

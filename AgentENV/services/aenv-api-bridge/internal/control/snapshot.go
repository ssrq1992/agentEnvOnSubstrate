package control

import (
	"agentenv/services/aenv-api-bridge/internal/auth"
	bridge "agentenv/services/aenv-api-bridge/internal/metadata"
	"context"
	"fmt"
	pb "github.com/agent-substrate/substrate/pkg/proto/ateapipb"
)

// Capture freezes the observed allocation. Retrying the same destination never
// captures a newer incarnation or allocation after an ambiguous result.
func (c *Client) Capture(ctx context.Context, t auth.Tenant, b bridge.Sandbox, generation uint64, destination string) (*pb.Tag, error) {
	if b.ActorUID == "" || generation == 0 || destination == "" {
		return nil, fmt.Errorf("capture identity and destination required")
	}
	ctx, err := tenantContext(ctx, t, b)
	if err != nil {
		return nil, err
	}
	tag, err := c.RPC.CaptureActorSnapshot(ctx, &pb.CaptureActorSnapshotRequest{Actor: ref(b), Uid: b.ActorUID, AssignmentGeneration: generation, TagName: destination})
	if err != nil {
		return nil, err
	}
	if tag.GetMetadata().GetAtespace() != b.ActorAtespace || tag.GetMetadata().GetName() != destination || tag.GetStatus().GetCaptureActorUid() != b.ActorUID || tag.GetStatus().GetCaptureAssignment().GetAssignmentGeneration() != generation || tag.GetStatus().GetSnapshot().GetContentScope() != pb.SnapshotContentScope_SNAPSHOT_CONTENT_SCOPE_FULL || tag.GetStatus().GetSnapshot().GetSnapshotUri() == "" {
		return nil, fmt.Errorf("capture acknowledgment does not match requested source")
	}
	return tag, nil
}

package control

import (
	"agentenv/services/aenv-api-bridge/internal/auth"
	"agentenv/services/aenv-api-bridge/internal/extensions"
	bridge "agentenv/services/aenv-api-bridge/internal/metadata"
	"context"
	pb "github.com/agent-substrate/substrate/pkg/proto/ateapipb"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

func (c *Client) ExtensionParams(ctx context.Context, t auth.Tenant, b bridge.Sandbox) (*pb.ActorExtensionParams, error) {
	if b.ActorUID == "" {
		return nil, status.Error(codes.FailedPrecondition, "confirmed Actor UID required")
	}
	ctx, err := tenantContext(ctx, t, b)
	if err != nil {
		return nil, err
	}
	return c.RPC.GetActorExtensionParams(ctx, &pb.GetActorExtensionParamsRequest{Actor: ref(b), Uid: b.ActorUID})
}
func (c *Client) ApplyExtensionParams(ctx context.Context, t auth.Tenant, b bridge.Sandbox, req *pb.UpdateActorExtensionParamsRequest) (*pb.ActorExtensionParams, error) {
	if req == nil || req.Uid == "" || req.Uid != b.ActorUID || !proto.Equal(req.Actor, ref(b)) {
		return nil, status.Error(codes.InvalidArgument, "extension identity mismatch")
	}
	ctx, err := tenantContext(ctx, t, b)
	if err != nil {
		return nil, err
	}
	result, err := c.RPC.UpdateActorExtensionParams(ctx, proto.CloneOf(req))
	if err == nil {
		return result, nil
	}
	// A failed transport is not evidence of rejection. Only a persisted receipt
	// for this exact request can close the operation after an uncertain result.
	actor, readErr := c.RPC.GetActor(ctx, &pb.GetActorRequest{Actor: ref(b)})
	if readErr != nil || checkIdentity(actor, b) != nil {
		return nil, err
	}
	receipt := actor.GetStatus().GetAgentenvExtensionDelivery()
	if proto.Equal(receipt.GetLastRequest(), req) {
		if receipt.GetLastRejected() {
			return nil, extensions.ErrRejected
		}
		if receipt.GetLastResult() != nil {
			return proto.CloneOf(receipt.LastResult), nil
		}
	}
	return nil, err
}

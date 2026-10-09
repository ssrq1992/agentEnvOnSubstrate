package control

import (
	"agentenv/services/aenv-api-bridge/internal/extensions"
	"context"
	"errors"
	pb "github.com/agent-substrate/substrate/pkg/proto/ateapipb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"testing"
)

type extensionRPC struct {
	*controlStub
	request *pb.UpdateActorExtensionParamsRequest
	err     error
	result  *pb.ActorExtensionParams
}

func (s *extensionRPC) GetActorExtensionParams(ctx context.Context, _ *pb.GetActorExtensionParamsRequest, _ ...grpc.CallOption) (*pb.ActorExtensionParams, error) {
	s.record(ctx)
	return s.result, nil
}
func (s *extensionRPC) UpdateActorExtensionParams(ctx context.Context, r *pb.UpdateActorExtensionParamsRequest, _ ...grpc.CallOption) (*pb.ActorExtensionParams, error) {
	s.record(ctx)
	s.request = proto.CloneOf(r)
	return s.result, s.err
}
func TestExtensionUsesTenantCredentialsAndExactDurableReceipt(t *testing.T) {
	c, base, tnt, b := fixture(t)
	b.ActorUID = "actor-uid"
	req := &pb.UpdateActorExtensionParamsRequest{Actor: ref(b), Uid: b.ActorUID, AssignmentGeneration: 7, OperationId: "operation", ExpectedRevision: 2, PatchJson: `{"desired":true}`}
	rpc := &extensionRPC{controlStub: base, err: status.Error(codes.Unavailable, "unknown"), result: &pb.ActorExtensionParams{Revision: 3, Json: `{"approved":true}`}}
	c.RPC = rpc
	if _, err := c.ApplyExtensionParams(t.Context(), tnt, b, req); status.Code(err) != codes.Unavailable {
		t.Fatal("unknown mistaken for rejection", err)
	}
	base.actor.Status = &pb.ActorStatus{AgentenvExtensionDelivery: &pb.AgentENVExtensionDelivery{LastRequest: proto.CloneOf(req), LastResult: rpc.result}}
	got, err := c.ApplyExtensionParams(t.Context(), tnt, b, req)
	if err != nil || !proto.Equal(got, rpc.result) || !proto.Equal(rpc.request, req) {
		t.Fatal(got, err)
	}
	if len(base.authorization) != 1 || base.authorization[0] != "Bearer tenant-token" {
		t.Fatal("lost tenant credentials")
	}
	base.actor.Status.AgentenvExtensionDelivery.LastRejected = true
	if _, err = c.ApplyExtensionParams(t.Context(), tnt, b, req); !errors.Is(err, extensions.ErrRejected) {
		t.Fatal("confirmed rejection not returned", err)
	}
	base.actor.Status.AgentenvExtensionDelivery.LastRequest.PatchJson = `{}`
	if _, err = c.ApplyExtensionParams(t.Context(), tnt, b, req); status.Code(err) != codes.Unavailable {
		t.Fatal("other payload receipt used", err)
	}
	calls := base.calls
	b.Tenant = "other"
	if _, err = c.ApplyExtensionParams(t.Context(), tnt, b, req); err == nil || base.calls != calls {
		t.Fatal("cross tenant mutation")
	}
}

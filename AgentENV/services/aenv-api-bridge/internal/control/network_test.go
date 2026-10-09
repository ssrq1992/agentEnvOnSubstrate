package control

import (
	"context"
	pb "github.com/agent-substrate/substrate/pkg/proto/ateapipb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"testing"
)

type networkRPC struct {
	*controlStub
	current, result *pb.EgressPolicy
	pre             *pb.AgentENVPolicyPreconditions
}

func (s *networkRPC) GetActorEgressPolicy(ctx context.Context, _ *pb.GetActorEgressPolicyRequest, _ ...grpc.CallOption) (*pb.EgressPolicy, error) {
	s.record(ctx)
	if s.current == nil {
		return nil, status.Error(codes.NotFound, "missing")
	}
	return s.current, nil
}
func (s *networkRPC) CreateActorEgressPolicy(ctx context.Context, r *pb.CreateActorEgressPolicyRequest, _ ...grpc.CallOption) (*pb.EgressPolicy, error) {
	s.record(ctx)
	s.pre = r.AgentenvPreconditions
	return s.result, nil
}
func (s *networkRPC) UpdateActorEgressPolicy(ctx context.Context, r *pb.UpdateActorEgressPolicyRequest, _ ...grpc.CallOption) (*pb.EgressPolicy, error) {
	s.record(ctx)
	s.pre = r.AgentenvPreconditions
	return s.result, nil
}
func TestNetworkPolicyRequiresMatchingDurableACK(t *testing.T) {
	c, base, tenant, sandbox := fixture(t)
	sandbox.ActorUID = "actor-uid"
	policy := &pb.AgentENVNetworkPolicy{Base: pb.AgentENVNetworkPolicy_DENY}
	result := &pb.EgressPolicy{Metadata: &pb.ResourceMetadata{Uid: "policy-uid", Version: 1}, Agentenv: policy, AgentenvDelivery: &pb.AgentENVPolicyDelivery{Revision: 2, AppliedRevision: 2, PolicyUid: "policy-uid", PolicyVersion: 1, AppliedAssignment: &pb.WorkerAssignment{ExecutorInstanceId: "instance"}}}
	rpc := &networkRPC{controlStub: base, result: result}
	c.RPC = rpc
	for _, existing := range []bool{false, true} {
		if existing {
			rpc.current = proto.Clone(result).(*pb.EgressPolicy)
		}
		if err := c.ReplaceNetworkPolicy(t.Context(), tenant, sandbox, policy, 0); err != nil {
			t.Fatal(err)
		}
		if rpc.pre.GetActorUid() != sandbox.ActorUID || !rpc.pre.GetRequireRunning() || len(rpc.authorization) != 1 || rpc.authorization[0] != "Bearer tenant-token" {
			t.Fatal("identity or state preconditions missing")
		}
	}
	result.AgentenvDelivery.AppliedRevision = 1
	if err := c.ReplaceNetworkPolicy(t.Context(), tenant, sandbox, policy, 0); status.Code(err) != codes.Unavailable {
		t.Fatalf("accepted stale ACK: %v", err)
	}
	before := rpc.calls
	sandbox.Tenant = "other"
	if err := c.ReplaceNetworkPolicy(t.Context(), tenant, sandbox, policy, 0); err == nil || rpc.calls != before {
		t.Fatal("cross-tenant mutation")
	}
}

package control

import (
	"agentenv/services/aenv-api-bridge/internal/auth"
	bridge "agentenv/services/aenv-api-bridge/internal/metadata"
	"context"
	"fmt"
	pb "github.com/agent-substrate/substrate/pkg/proto/ateapipb"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

// ReplaceNetworkPolicy returns only after the executor has acknowledged the
// persisted revision. Actor incarnation and running state are checked under
// the control-plane lifecycle lease, not by a racy preliminary GetActor.
func (c *Client) ReplaceNetworkPolicy(ctx context.Context, t auth.Tenant, b bridge.Sandbox, policy *pb.AgentENVNetworkPolicy, expectedRevision uint64) error {
	if b.ActorUID == "" || policy == nil {
		return fmt.Errorf("confirmed Actor UID and policy required")
	}
	ctx, err := tenantContext(ctx, t, b)
	if err != nil {
		return err
	}
	conditions := &pb.AgentENVPolicyPreconditions{ActorUid: b.ActorUID, RequireRunning: true, ExpectedPolicyRevision: &expectedRevision}
	var result *pb.EgressPolicy
	for attempt := 0; attempt < 2; attempt++ {
		current, e := c.RPC.GetActorEgressPolicy(ctx, &pb.GetActorEgressPolicyRequest{Actor: ref(b)})
		if status.Code(e) == codes.NotFound {
			result, err = c.RPC.CreateActorEgressPolicy(ctx, &pb.CreateActorEgressPolicyRequest{Actor: ref(b), AgentenvPreconditions: conditions, EgressPolicy: &pb.EgressPolicy{Metadata: &pb.ResourceMetadata{Atespace: b.ActorAtespace, Name: "default"}, Agentenv: proto.Clone(policy).(*pb.AgentENVNetworkPolicy)}})
			if status.Code(err) == codes.AlreadyExists {
				continue
			}
		} else {
			if e != nil {
				return e
			}
			if current.GetAgentenv() == nil {
				return status.Error(codes.FailedPrecondition, "existing policy is not native AgentENV")
			}
			replacement := proto.Clone(current).(*pb.EgressPolicy)
			replacement.Agentenv = proto.Clone(policy).(*pb.AgentENVNetworkPolicy)
			replacement.AgentenvDelivery = nil
			result, err = c.RPC.UpdateActorEgressPolicy(ctx, &pb.UpdateActorEgressPolicyRequest{Actor: ref(b), AgentenvPreconditions: conditions, EgressPolicy: replacement})
		}
		if err != nil {
			return err
		}
		break
	}
	if err != nil {
		return err
	}
	d := result.GetAgentenvDelivery()
	if !proto.Equal(result.GetAgentenv(), policy) || d.GetRevision() == 0 || d.GetAppliedRevision() != d.GetRevision() || d.GetPolicyUid() != result.GetMetadata().GetUid() || d.GetPolicyVersion() != result.GetMetadata().GetVersion() || d.GetAppliedAssignment().GetExecutorInstanceId() == "" {
		return status.Error(codes.Unavailable, "network policy execution was not confirmed")
	}
	return nil
}

// NetworkPolicyRevision reads the durable revision before request preparation.
// The mutation rechecks it under the Actor lease.
func (c *Client) NetworkPolicyRevision(ctx context.Context, t auth.Tenant, b bridge.Sandbox) (uint64, error) {
	if b.ActorUID == "" {
		return 0, fmt.Errorf("confirmed Actor UID required")
	}
	actor, err := c.Get(ctx, t, b)
	if err != nil {
		return 0, err
	}
	if actor.GetStatus().GetState() != pb.ActorState_ACTOR_STATE_RUNNING {
		return 0, status.Error(codes.FailedPrecondition, "sandbox is not running")
	}
	return actor.GetStatus().GetAgentenvPolicyDelivery().GetRevision(), nil
}

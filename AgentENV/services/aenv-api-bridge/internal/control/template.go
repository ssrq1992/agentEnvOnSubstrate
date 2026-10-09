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

func (c *Client) Template(ctx context.Context, t auth.Tenant, name string) (*pb.ActorTemplate, error) {
	ctx, err := tenantContext(ctx, t, bridge.Sandbox{Tenant: t.ID, ActorAtespace: t.Atespace, ActorName: "template-lookup"})
	if err != nil {
		return nil, err
	}
	p, err := c.RPC.GetActorTemplate(ctx, &pb.GetActorTemplateRequest{ActorTemplate: &pb.ObjectRef{Atespace: t.Atespace, Name: name}})
	if err != nil {
		return nil, err
	}
	if p.GetMetadata().GetAtespace() != t.Atespace || p.GetMetadata().GetName() != name || p.GetMetadata().GetUid() == "" || p.GetSandboxConfig().GetSandboxClass() != pb.SandboxClass_SANDBOX_CLASS_AGENTENV {
		return nil, fmt.Errorf("invalid AgentENV template identity")
	}
	return p, nil
}

// EnsureTemplate publishes an immutable per-creation launch description.
// Retries verify all specification fields rather than accepting name collisions.
func (c *Client) EnsureTemplate(ctx context.Context, t auth.Tenant, p *pb.ActorTemplate) error {
	ctx, err := tenantContext(ctx, t, bridge.Sandbox{Tenant: t.ID, ActorAtespace: t.Atespace, ActorName: "template-create"})
	if err != nil {
		return err
	}
	if p.GetMetadata().GetAtespace() != t.Atespace {
		return fmt.Errorf("template tenant mismatch")
	}
	result, err := c.RPC.CreateActorTemplate(ctx, &pb.CreateActorTemplateRequest{ActorTemplate: p})
	if status.Code(err) == codes.AlreadyExists {
		result, err = c.RPC.GetActorTemplate(ctx, &pb.GetActorTemplateRequest{ActorTemplate: &pb.ObjectRef{Atespace: t.Atespace, Name: p.Metadata.Name}})
	}
	if err != nil {
		return err
	}
	actual := proto.CloneOf(result)
	actual.Metadata = proto.CloneOf(p.Metadata)
	actual.Status = nil
	expected := proto.CloneOf(p)
	expected.Status = nil
	if !proto.Equal(actual, expected) {
		return status.Error(codes.FailedPrecondition, "creation template differs from persisted preparation")
	}
	return nil
}
func (c *Client) InitialNetworkPolicy(ctx context.Context, t auth.Tenant, b bridge.Sandbox, p *pb.AgentENVNetworkPolicy) error {
	if b.ActorUID == "" || p == nil {
		return fmt.Errorf("confirmed actor and initial policy required")
	}
	ctx, err := tenantContext(ctx, t, b)
	if err != nil {
		return err
	}
	zero := uint64(0)
	condition := &pb.AgentENVPolicyPreconditions{ActorUid: b.ActorUID, ExpectedPolicyRevision: &zero}
	result, err := c.RPC.CreateActorEgressPolicy(ctx, &pb.CreateActorEgressPolicyRequest{Actor: ref(b), AgentenvPreconditions: condition, EgressPolicy: &pb.EgressPolicy{Metadata: &pb.ResourceMetadata{Atespace: t.Atespace, Name: "default"}, Agentenv: proto.CloneOf(p)}})
	if status.Code(err) == codes.AlreadyExists {
		result, err = c.RPC.GetActorEgressPolicy(ctx, &pb.GetActorEgressPolicyRequest{Actor: ref(b)})
	}
	if err != nil {
		return err
	}
	if !proto.Equal(result.GetAgentenv(), p) {
		return status.Error(codes.FailedPrecondition, "initial network policy changed")
	}
	return nil
}

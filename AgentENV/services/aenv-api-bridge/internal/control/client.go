// Package control calls Substrate with the tenant's authorized principal.
package control

import (
	"context"
	"fmt"
	"google.golang.org/protobuf/proto"
	"strings"

	"agentenv/services/aenv-api-bridge/internal/auth"
	bridge "agentenv/services/aenv-api-bridge/internal/metadata"
	pb "github.com/agent-substrate/substrate/pkg/proto/ateapipb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

type Client struct{ RPC pb.ControlClient }

// Dial uses server TLS and per-request OIDC tokens. A bridge client certificate
// would override the tenant JWT in ateapi's authentication chain.
func Dial(address, serverName, rootsPath string) (*Client, func() error, error) {
	if address == "" || serverName == "" || strings.Contains(address, "://") {
		return nil, nil, fmt.Errorf("explicit control address and TLS server name required")
	}
	transport, err := controlCredentials(serverName, rootsPath)
	if err != nil {
		return nil, nil, err
	}
	conn, err := grpc.NewClient(address, grpc.WithTransportCredentials(transport))
	if err != nil {
		return nil, nil, err
	}
	return &Client{RPC: pb.NewControlClient(conn)}, conn.Close, nil
}
func tenantContext(ctx context.Context, t auth.Tenant, b bridge.Sandbox) (context.Context, error) {
	if t.ID == "" || t.Atespace == "" || b.Tenant != t.ID || b.ActorAtespace != t.Atespace || b.ActorName == "" {
		return nil, fmt.Errorf("sandbox tenant mapping mismatch")
	}
	token, err := t.ControlToken()
	if err != nil {
		return nil, err
	}
	// Replace outgoing credentials; never append to inherited caller metadata.
	return metadata.NewOutgoingContext(ctx, metadata.Pairs("authorization", "Bearer "+token)), nil
}
func ref(b bridge.Sandbox) *pb.ObjectRef {
	return &pb.ObjectRef{Atespace: b.ActorAtespace, Name: b.ActorName}
}
func checkIdentity(actor *pb.Actor, b bridge.Sandbox) error {
	if actor.GetMetadata().GetAtespace() != b.ActorAtespace || actor.GetMetadata().GetName() != b.ActorName || actor.GetMetadata().GetUid() == "" {
		return fmt.Errorf("control returned unexpected Actor identity")
	}
	if b.ActorUID != "" && actor.GetMetadata().GetUid() != b.ActorUID {
		return fmt.Errorf("Actor name belongs to a different incarnation")
	}
	return nil
}
func (c *Client) Get(ctx context.Context, t auth.Tenant, b bridge.Sandbox) (*pb.Actor, error) {
	ctx, err := tenantContext(ctx, t, b)
	if err != nil {
		return nil, err
	}
	actor, err := c.RPC.GetActor(ctx, &pb.GetActorRequest{Actor: ref(b)})
	if err != nil {
		return nil, err
	}
	if err = checkIdentity(actor, b); err != nil {
		return nil, err
	}
	return actor, nil
}

// Create uses the name committed in metadata before the first call. An
// AlreadyExists response is reconciled against that identity and template.
func (c *Client) Create(ctx context.Context, t auth.Tenant, b bridge.Sandbox, template string) (*pb.Actor, error) {
	return c.create(ctx, t, b, template, nil, "")
}

// CreateFromSnapshot uses the same template as the captured source. Substrate
// owns child allocation and restores a new identity for each child.
func (c *Client) CreateFromSnapshot(ctx context.Context, t auth.Tenant, b bridge.Sandbox, template string, source *pb.ObjectRef, sourceUID string) (*pb.Actor, error) {
	if sourceUID == "" || source == nil || source.GetAtespace() != t.Atespace || source.GetName() == "" {
		return nil, fmt.Errorf("tenant snapshot reference required")
	}
	return c.create(ctx, t, b, template, source, sourceUID)
}

func (c *Client) create(ctx context.Context, t auth.Tenant, b bridge.Sandbox, template string, source *pb.ObjectRef, sourceUID string) (*pb.Actor, error) {
	if template == "" {
		return nil, fmt.Errorf("resolved template required")
	}

	if b.ActorUID != "" {
		actor, err := c.Get(ctx, t, b)
		if err != nil {
			return nil, err
		}
		if actor.GetActorTemplate().GetAtespace() != t.Atespace || actor.GetActorTemplate().GetName() != template {
			return nil, fmt.Errorf("creation template changed")
		}
		if !proto.Equal(actor.GetSourceTag(), source) || actor.GetSourceTagUid() != sourceUID {
			return nil, fmt.Errorf("creation snapshot changed")
		}
		return actor, nil
	}
	ctx, err := tenantContext(ctx, t, b)
	if err != nil {
		return nil, err
	}
	actor, err := c.RPC.CreateActor(ctx, &pb.CreateActorRequest{Actor: &pb.Actor{Metadata: &pb.ResourceMetadata{Atespace: b.ActorAtespace, Name: b.ActorName}, ActorTemplate: &pb.ObjectRef{Atespace: t.Atespace, Name: template}, SourceTag: source, SourceTagUid: sourceUID}})
	if status.Code(err) == codes.AlreadyExists {
		actor, err = c.RPC.GetActor(ctx, &pb.GetActorRequest{Actor: ref(b)})
	}
	if err != nil {
		return nil, err
	}
	if err = checkIdentity(actor, b); err != nil {
		return nil, err
	}
	if actor.GetActorTemplate().GetAtespace() != t.Atespace || actor.GetActorTemplate().GetName() != template {
		return nil, fmt.Errorf("persisted creation intent differs from existing Actor")
	}
	if !proto.Equal(actor.GetSourceTag(), source) || actor.GetSourceTagUid() != sourceUID {
		return nil, fmt.Errorf("persisted creation snapshot differs")
	}
	return actor, nil
}
func (c *Client) Delete(ctx context.Context, t auth.Tenant, b bridge.Sandbox) error {
	if b.ActorUID == "" {
		return fmt.Errorf("confirmed Actor UID required before deletion")
	}
	ctx, err := tenantContext(ctx, t, b)
	if err != nil {
		return err
	}
	_, err = c.RPC.DeleteActor(ctx, &pb.DeleteActorRequest{Actor: ref(b), AnyState: true, Options: &pb.DeleteOptions{Uid: b.ActorUID}})
	// Only this public control-plane NotFound is a successful deletion. An
	// executor transport failure must remain pending in compatibility metadata.
	if status.Code(err) == codes.NotFound {
		return nil
	}
	return err
}
func (c *Client) Suspend(ctx context.Context, t auth.Tenant, b bridge.Sandbox) (*pb.Actor, error) {
	if b.ActorUID == "" {
		return nil, fmt.Errorf("confirmed Actor UID required before lifecycle mutation")
	}
	ctx, err := tenantContext(ctx, t, b)
	if err != nil {
		return nil, err
	}
	response, err := c.RPC.SuspendActor(ctx, &pb.SuspendActorRequest{Actor: ref(b), Uid: b.ActorUID})
	if err != nil {
		return nil, err
	}
	if err = checkIdentity(response.GetActor(), b); err != nil {
		return nil, err
	}
	return response.Actor, nil
}
func (c *Client) Resume(ctx context.Context, t auth.Tenant, b bridge.Sandbox) (*pb.Actor, error) {
	if b.ActorUID == "" {
		return nil, fmt.Errorf("confirmed Actor UID required before lifecycle mutation")
	}
	ctx, err := tenantContext(ctx, t, b)
	if err != nil {
		return nil, err
	}
	response, err := c.RPC.ResumeActor(ctx, &pb.ResumeActorRequest{Actor: ref(b), Uid: b.ActorUID})
	if err != nil {
		return nil, err
	}
	if err = checkIdentity(response.GetActor(), b); err != nil {
		return nil, err
	}
	return response.Actor, nil
}

// Connect retrieves an allocation-local secret under the control-plane lease.
// The caller must return it directly to the authenticated SDK; it must not be
// included in durable request receipts, logs, or snapshot metadata.
func (c *Client) Connect(ctx context.Context, t auth.Tenant, b bridge.Sandbox) (*pb.ConnectActorResponse, error) {
	if b.ActorUID == "" {
		return nil, fmt.Errorf("confirmed Actor UID required before connection")
	}
	ctx, err := tenantContext(ctx, t, b)
	if err != nil {
		return nil, err
	}
	response, err := c.RPC.ConnectActor(ctx, &pb.ConnectActorRequest{Actor: ref(b), Uid: b.ActorUID})
	if err != nil {
		return nil, err
	}
	if err = checkIdentity(response.GetActor(), b); err != nil {
		return nil, err
	}
	if response.GetActor().GetStatus().GetState() != pb.ActorState_ACTOR_STATE_RUNNING || response.GetEnvdAccessToken() == "" || response.GetEnvdVersion() == "" {
		return nil, fmt.Errorf("control did not confirm a running connection")
	}
	return response, nil
}

// GuestMetrics is an instantaneous, allocation-fenced guest sample. It must
// not be presented as host usage or a historical series without a collector.
func (c *Client) GuestMetrics(ctx context.Context, t auth.Tenant, b bridge.Sandbox) (*pb.GetActorGuestMetricsResponse, error) {
	if b.ActorUID == "" {
		return nil, fmt.Errorf("confirmed Actor UID required before metrics lookup")
	}
	ctx, err := tenantContext(ctx, t, b)
	if err != nil {
		return nil, err
	}
	response, err := c.RPC.GetActorGuestMetrics(ctx, &pb.GetActorGuestMetricsRequest{Actor: ref(b), Uid: b.ActorUID})
	if err != nil {
		return nil, err
	}
	if response.GetActorUid() != b.ActorUID || response.GetAssignment().GetExecutorInstanceId() == "" || response.GetObservedAtUnixMillis() <= 0 {
		return nil, fmt.Errorf("control did not confirm metrics attribution")
	}
	return response, nil
}

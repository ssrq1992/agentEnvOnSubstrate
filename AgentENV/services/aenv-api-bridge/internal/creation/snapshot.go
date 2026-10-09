package creation

import (
	"agentenv/services/aenv-api-bridge/internal/auth"
	"agentenv/services/aenv-api-bridge/internal/metadata"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	pb "github.com/agent-substrate/substrate/pkg/proto/ateapipb"
	"google.golang.org/protobuf/proto"
)

// Restore reserves each child as an independent creation job. The child keeps
// the captured template identity; cloning the template would invalidate memory
// compatibility. Its TTL starts only after its own RUNNING acknowledgment.
func (s *Service) Restore(ctx context.Context, t auth.Tenant, child metadata.Sandbox, tag *pb.Tag, template *pb.ActorTemplate, access metadata.Access, profile metadata.Profile, policy *pb.AgentENVNetworkPolicy) (metadata.Sandbox, *pb.ConnectActorResponse, error) {
	if child.Tenant != t.ID || child.ActorAtespace != t.Atespace || child.ActorUID != "" || tag.GetMetadata().GetUid() == "" || tag.GetMetadata().GetAtespace() != t.Atespace || tag.GetStatus().GetSnapshot().GetContentScope() != pb.SnapshotContentScope_SNAPSHOT_CONTENT_SCOPE_FULL || tag.GetStatus().GetSnapshot().GetSnapshotUri() == "" || template.GetMetadata().GetAtespace() != t.Atespace || template.GetMetadata().GetUid() == "" || template.GetMetadata().GetUid() != tag.GetStatus().GetActorTemplateUid() {
		return child, nil, fmt.Errorf("snapshot child identity or template mismatch")
	}
	wire, err := proto.MarshalOptions{Deterministic: true}.Marshal(template)
	if err != nil {
		return child, nil, err
	}
	plan := Plan{SourceTagUID: tag.Metadata.Uid, Template: wire, SharedTemplateUID: template.Metadata.Uid, SourceTag: &pb.ObjectRef{Atespace: t.Atespace, Name: tag.Metadata.Name}, Access: access, Profile: profile, Policy: policy}
	prepared, err := json.Marshal(plan)
	if err != nil {
		return child, nil, err
	}
	payload, _ := json.Marshal(struct {
		Child  metadata.Sandbox
		TagUID string
		Plan   Plan
	}{child, tag.Metadata.Uid, plan})
	digest := sha256.Sum256(payload)
	request := metadata.Request{Tenant: t.ID, ExternalID: child.ExternalID, ID: "create-" + child.ExternalID, Kind: "create", Digest: hex.EncodeToString(digest[:])}
	intent, err := s.Store.ReserveCreation(ctx, child, request, prepared)
	if err != nil {
		return child, nil, err
	}
	if err := s.execute(ctx, t, intent); err != nil {
		return child, nil, err
	}
	child, err = s.Store.Get(ctx, t.ID, child.ExternalID)
	if err != nil {
		return child, nil, err
	}
	connection, err := s.Control.Connect(ctx, t, child)
	return child, connection, err
}

// Cancel freezes the rejected job before deletion. A cleanup timeout retains
// the rejected job and UID for retry; successful siblings remain untouched.
func (s *Service) Cancel(ctx context.Context, t auth.Tenant, b metadata.Sandbox, tagUID string) error {
	if b.Tenant != t.ID || b.ActorAtespace != t.Atespace {
		return metadata.ErrConflict
	}
	store, ok := s.Store.(interface {
		RejectSnapshotCreation(context.Context, metadata.Sandbox, string) (metadata.Sandbox, error)
		ConfirmSnapshotCreationFailed(context.Context, metadata.Sandbox) error
	})
	if !ok {
		return fmt.Errorf("snapshot cleanup metadata required")
	}
	control, ok := s.Control.(interface {
		Delete(context.Context, auth.Tenant, metadata.Sandbox) error
	})
	if !ok {
		return fmt.Errorf("snapshot cleanup control required")
	}
	unlock, err := s.Store.LockOperation(ctx, b.Tenant, b.ExternalID)
	if err != nil {
		return err
	}
	defer unlock()
	b, err = store.RejectSnapshotCreation(ctx, b, tagUID)
	if err != nil {
		return err
	}
	if b.Deleted {
		return nil
	}
	if err := control.Delete(ctx, t, b); err != nil {
		return err
	}
	return store.ConfirmSnapshotCreationFailed(ctx, b)
}

func (s *Service) Confirmed(ctx context.Context, t auth.Tenant, b metadata.Sandbox, tagUID string) (bool, error) {
	if b.Tenant != t.ID || b.ActorAtespace != t.Atespace {
		return false, metadata.ErrConflict
	}
	store, ok := s.Store.(interface {
		SnapshotCreationConfirmed(context.Context, metadata.Sandbox, string) (bool, error)
	})
	if !ok {
		return false, fmt.Errorf("snapshot confirmation metadata required")
	}
	return store.SnapshotCreationConfirmed(ctx, b, tagUID)
}

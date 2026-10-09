package lifecycle

import (
	"agentenv/services/aenv-api-bridge/internal/auth"
	"agentenv/services/aenv-api-bridge/internal/metadata"
	"context"
	"errors"
	pb "github.com/agent-substrate/substrate/pkg/proto/ateapipb"
	"testing"
)

type refreshFixture struct {
	row            metadata.Sandbox
	state          pb.ActorState
	seconds, calls int
	uid            string
}

func (f *refreshFixture) LockOperation(context.Context, string, string) (func(), error) {
	return func() {}, nil
}
func (f *refreshFixture) Get(context.Context, string, string) (metadata.Sandbox, error) {
	return f.row, nil
}
func (f *refreshFixture) RefreshMinimum(_ context.Context, b metadata.Sandbox, seconds int) error {
	f.seconds = seconds
	f.calls++
	return nil
}

type refreshRuntime struct{ f *refreshFixture }

func (r refreshRuntime) Get(context.Context, auth.Tenant, metadata.Sandbox) (*pb.Actor, error) {
	return &pb.Actor{Metadata: &pb.ResourceMetadata{Uid: r.f.uid}, Status: &pb.ActorStatus{State: r.f.state}}, nil
}
func TestRefreshRequiresRunningIdentityAndUsesDefault(t *testing.T) {
	f := &refreshFixture{row: metadata.Sandbox{Tenant: "tenant", ExternalID: "sandbox", ActorAtespace: "space", ActorName: "actor", ActorUID: "uid", Revision: 4}, state: pb.ActorState_ACTOR_STATE_RUNNING, uid: "uid"}
	s := &RefreshService{Store: f, Control: refreshRuntime{f}, DefaultTimeout: 15}
	tnt := auth.Tenant{ID: "tenant", Atespace: "space"}
	if err := s.Refresh(t.Context(), tnt, f.row, nil); err != nil || f.seconds != 15 || f.calls != 1 {
		t.Fatal(err, f)
	}
	f.state = pb.ActorState_ACTOR_STATE_SUSPENDED
	if err := s.Refresh(t.Context(), tnt, f.row, nil); err == nil || f.calls != 1 {
		t.Fatal("paused refresh accepted", err)
	}
	f.state = pb.ActorState_ACTOR_STATE_RUNNING
	f.uid = "other"
	if err := s.Refresh(t.Context(), tnt, f.row, nil); !errors.Is(err, metadata.ErrNotFound) {
		t.Fatal("wrong incarnation accepted", err)
	}
}

package lifecycle

import (
	"agentenv/services/aenv-api-bridge/internal/auth"
	"agentenv/services/aenv-api-bridge/internal/metadata"
	"context"
	pb "github.com/agent-substrate/substrate/pkg/proto/ateapipb"
	"testing"
)

type storeStub struct {
	intent     metadata.SuspendIntent
	confirmed  int
	reserveErr error
}

func (s *storeStub) ReserveSuspend(_ context.Context, b metadata.Sandbox, g uint64) (metadata.SuspendIntent, error) {
	if g != 7 {
		panic("wrong generation")
	}
	return s.intent, s.reserveErr
}
func (s *storeStub) ConfirmSuspended(context.Context, metadata.SuspendIntent) error {
	s.confirmed++
	return nil
}
func (s *storeStub) ClaimSuspends(context.Context, int) ([]metadata.SuspendIntent, error) {
	return []metadata.SuspendIntent{s.intent}, nil
}

type controlStub struct {
	calls      int
	err        error
	generation uint64
}

func (c *controlStub) SuspendGeneration(context.Context, auth.Tenant, metadata.Sandbox) (uint64, error) {
	return 7, nil
}
func (c *controlStub) SuspendPrepared(_ context.Context, _ auth.Tenant, i metadata.SuspendIntent) error {
	c.calls++
	c.generation = i.Generation
	return c.err
}
func TestPauseUnknownResultIsReconciledWithSameGeneration(t *testing.T) {
	tenant := auth.Tenant{ID: "tenant", Atespace: "space"}
	b := metadata.Sandbox{Tenant: "tenant", ActorAtespace: "space", ActorUID: "uid", ExternalID: "sandbox"}
	st := &storeStub{intent: metadata.SuspendIntent{Sandbox: b, Generation: 7, Request: metadata.Request{Tenant: "tenant", ExternalID: "sandbox", Kind: "suspend", State: "pending", ID: "stable"}}}
	rpc := &controlStub{err: context.DeadlineExceeded}
	s := &Service{Store: st, Control: rpc, Tenants: map[string]auth.Tenant{"tenant": tenant}}
	if err := s.Pause(t.Context(), tenant, b); err == nil || st.confirmed != 0 {
		t.Fatal("unknown pause acknowledged")
	}
	rpc.err = nil
	if err := s.Sweep(t.Context()); err != nil || st.confirmed != 1 || rpc.generation != 7 || rpc.calls != 2 {
		t.Fatal("restart did not retain assignment", err)
	}
	st.intent.Request.State = "completed"
	if err := s.Pause(t.Context(), tenant, b); err != nil || rpc.calls != 2 {
		t.Fatal("completed intent executed again", err)
	}
	b.ActorAtespace = "other"
	if err := s.Pause(t.Context(), tenant, b); err == nil || rpc.calls != 2 {
		t.Fatal("cross tenant pause")
	}
}
func TestExpiryConflictNeverReachesRuntime(t *testing.T) {
	st := &storeStub{reserveErr: metadata.ErrConflict}
	rpc := &controlStub{}
	s := &Service{Store: st, Control: rpc}
	if err := s.Pause(t.Context(), auth.Tenant{ID: "tenant", Atespace: "space"}, metadata.Sandbox{Tenant: "tenant", ActorAtespace: "space"}); err == nil || rpc.calls != 0 {
		t.Fatal("committed expiry was bypassed")
	}
}

func (s *storeStub) ReserveResume(context.Context, metadata.Sandbox, string, int, bool) (metadata.ResumeIntent, error) {
	return metadata.ResumeIntent{}, metadata.ErrConflict
}
func (s *storeStub) ConfirmResumed(context.Context, metadata.ResumeIntent) error { return nil }
func (s *storeStub) ClaimResumes(context.Context, int) ([]metadata.ResumeIntent, error) {
	return nil, nil
}
func (c *controlStub) ResumeSource(context.Context, auth.Tenant, metadata.Sandbox) (string, bool, error) {
	return "", false, metadata.ErrConflict
}
func (c *controlStub) ResumePrepared(context.Context, auth.Tenant, metadata.ResumeIntent) error {
	return c.err
}
func (c *controlStub) Connect(context.Context, auth.Tenant, metadata.Sandbox) (*pb.ConnectActorResponse, error) {
	return nil, c.err
}

func (s *storeStub) PrepareExpiredSuspend(context.Context, metadata.Expiry, uint64) (metadata.SuspendIntent, error) {
	return metadata.SuspendIntent{}, metadata.ErrConflict
}

package fork

import (
	"agentenv/services/aenv-api-bridge/internal/auth"
	"agentenv/services/aenv-api-bridge/internal/metadata"
	"context"
	"errors"
	pb "github.com/agent-substrate/substrate/pkg/proto/ateapipb"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"testing"
)

type memoryStore struct {
	jobs         map[string]metadata.Fork
	children     map[string][]metadata.ForkChild
	sandboxes    map[string]metadata.Sandbox
	failComplete bool
}

func (s *memoryStore) LockOperation(context.Context, string, string) (func(), error) {
	return func() {}, nil
}
func (s *memoryStore) ReserveFork(_ context.Context, b metadata.Sandbox, r metadata.Request, p []byte) (metadata.Fork, error) {
	if old, ok := s.jobs[r.ID]; ok {
		if old.Request.Digest != r.Digest {
			return old, metadata.ErrConflict
		}
		return old, nil
	}
	r.State = "pending"
	i := metadata.Fork{Request: r, ActorUID: b.ActorUID, Prepared: append([]byte(nil), p...)}
	s.jobs[r.ID] = i
	return i, nil
}
func (s *memoryStore) Fork(_ context.Context, r metadata.Request) (metadata.Fork, error) {
	i, ok := s.jobs[r.ID]
	if !ok {
		return i, metadata.ErrNotFound
	}
	if i.Request.Digest != r.Digest {
		return i, metadata.ErrConflict
	}
	return i, nil
}
func (s *memoryStore) ConfirmForkCaptured(_ context.Context, i metadata.Fork, p []byte) error {
	i = s.jobs[i.Request.ID]
	i.Captured = true
	i.Snapshot = append([]byte(nil), p...)
	s.jobs[i.Request.ID] = i
	return nil
}
func (s *memoryStore) ClaimForks(context.Context, int) ([]metadata.Fork, error) {
	var out []metadata.Fork
	for _, i := range s.jobs {
		if i.Request.State == "pending" {
			out = append(out, i)
		}
	}
	return out, nil
}
func (s *memoryStore) Complete(_ context.Context, r metadata.Request, _ bool, p []byte) error {
	if s.failComplete {
		s.failComplete = false
		return context.DeadlineExceeded
	}
	i := s.jobs[r.ID]
	i.Request.State = "completed"
	i.Request.Result = append([]byte(nil), p...)
	s.jobs[r.ID] = i
	return nil
}
func (s *memoryStore) Get(_ context.Context, tenant, id string) (metadata.Sandbox, error) {
	b, ok := s.sandboxes[id]
	if !ok || b.Tenant != tenant {
		return b, metadata.ErrNotFound
	}
	return b, nil
}
func (s *memoryStore) Profile(context.Context, metadata.Sandbox) (metadata.Profile, error) {
	return metadata.Profile{TemplateID: "template", EnvdVersion: "envd", CPUCount: 1, MemoryMB: 128, DiskSizeMB: 1024}, nil
}
func (s *memoryStore) Access(context.Context, metadata.Sandbox) (metadata.Access, error) {
	return metadata.Access{Secure: true, EnvdPort: 49983, AutoPause: true}, nil
}
func (s *memoryStore) ForkChildren(_ context.Context, i metadata.Fork) ([]metadata.ForkChild, error) {
	return s.children[i.Request.ID], nil
}
func (s *memoryStore) RecordForkChild(_ context.Context, i metadata.Fork, c metadata.ForkChild) error {
	s.children[i.Request.ID] = append(s.children[i.Request.ID], c)
	return nil
}

type runtime struct {
	actor       *pb.Actor
	failCapture bool
	calls       []uint64
}

func (c *runtime) Get(context.Context, auth.Tenant, metadata.Sandbox) (*pb.Actor, error) {
	return c.actor, nil
}
func (c *runtime) Template(context.Context, auth.Tenant, string) (*pb.ActorTemplate, error) {
	return &pb.ActorTemplate{Metadata: &pb.ResourceMetadata{Atespace: "space", Name: "base", Uid: "template-uid"}}, nil
}
func (c *runtime) Capture(_ context.Context, _ auth.Tenant, b metadata.Sandbox, g uint64, d string) (*pb.Tag, error) {
	c.calls = append(c.calls, g)
	if c.failCapture {
		return nil, context.DeadlineExceeded
	}
	return &pb.Tag{Metadata: &pb.ResourceMetadata{Atespace: b.ActorAtespace, Name: d, Uid: "tag-uid"}, Status: &pb.TagStatus{ActorTemplateUid: "template-uid", Snapshot: &pb.ExternalSnapshot{SnapshotUri: "s3://bucket/snapshot", ContentScope: pb.SnapshotContentScope_SNAPSHOT_CONTENT_SCOPE_FULL}}}, nil
}

type childRuntime struct {
	expireFirst      bool
	expiredConfirmed string
	store            *memoryStore
	calls, cancels   map[string]int
	failure          error
	failedChild      string
}

func (c *childRuntime) Restore(_ context.Context, _ auth.Tenant, b metadata.Sandbox, _ *pb.Tag, _ *pb.ActorTemplate, _ metadata.Access, _ metadata.Profile, _ *pb.AgentENVNetworkPolicy) (metadata.Sandbox, *pb.ConnectActorResponse, error) {
	c.calls[b.ExternalID]++
	if c.failedChild == "" && len(c.calls) == 2 {
		c.failedChild = b.ExternalID
	}
	if b.ExternalID == c.failedChild && c.failure != nil {
		return b, nil, c.failure
	}
	b.ActorUID = "uid-" + b.ExternalID
	c.store.sandboxes[b.ExternalID] = b
	if c.expireFirst && len(c.calls) == 1 {
		c.expiredConfirmed = b.ExternalID
		delete(c.store.sandboxes, b.ExternalID)
		return b, nil, context.DeadlineExceeded
	}
	return b, &pb.ConnectActorResponse{}, nil
}
func (c *childRuntime) Cancel(_ context.Context, _ auth.Tenant, b metadata.Sandbox, _ string) error {
	c.cancels[b.ExternalID]++
	delete(c.store.sandboxes, b.ExternalID)
	return nil
}
func fixture() (*Service, *memoryStore, *runtime, *childRuntime, auth.Tenant, metadata.Sandbox) {
	store := &memoryStore{jobs: map[string]metadata.Fork{}, children: map[string][]metadata.ForkChild{}, sandboxes: map[string]metadata.Sandbox{}}
	b := metadata.Sandbox{Tenant: "tenant", ExternalID: "source", ActorAtespace: "space", ActorName: "parent", ActorUID: "parent-uid", TemplateAlias: "template", TimeoutSeconds: 123}
	control := &runtime{actor: &pb.Actor{Metadata: &pb.ResourceMetadata{Uid: b.ActorUID}, ActorTemplate: &pb.ObjectRef{Atespace: "space", Name: "base"}, Status: &pb.ActorStatus{State: pb.ActorState_ACTOR_STATE_RUNNING, WorkerAssignment: &pb.WorkerAssignment{AssignmentGeneration: 7}}}}
	child := &childRuntime{store: store, calls: map[string]int{}, cancels: map[string]int{}}
	tenant := auth.Tenant{ID: b.Tenant, Atespace: b.ActorAtespace}
	return &Service{Store: store, Control: control, Children: child, Tenants: map[string]auth.Tenant{tenant.ID: tenant}}, store, control, child, tenant, b
}
func TestForkRetainsSiblingsAndTerminalOutcomes(t *testing.T) {
	s, store, control, child, tenant, b := fixture()
	count := uint32(3)
	child.failure = status.Error(codes.ResourceExhausted, "budget")
	store.failComplete = true
	if _, err := s.Fork(t.Context(), tenant, b, Input{Count: &count}, "key"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("lost completion not retained", err)
	}
	out, err := s.Fork(t.Context(), tenant, b, Input{Count: &count}, "key")
	if err != nil || len(out) != 3 {
		t.Fatal(out, err)
	}
	if out[0].Sandbox == nil || out[1].Error == "" || out[2].Sandbox == nil {
		t.Fatal("sibling outcomes lost", out)
	}
	if len(control.calls) != 1 || child.cancels[child.failedChild] != 1 {
		t.Fatal("capture or cleanup repeated")
	}
	for _, calls := range child.calls {
		if calls != 1 {
			t.Fatal("committed child attempted again")
		}
	}
	count = 2
	if _, err := s.Fork(t.Context(), tenant, b, Input{Count: &count}, "key"); !errors.Is(err, metadata.ErrConflict) {
		t.Fatal("key changed count", err)
	}
}
func TestForkCaptureUnknownFreezesGeneration(t *testing.T) {
	s, _, control, child, tenant, b := fixture()
	control.failCapture = true
	if _, err := s.Fork(t.Context(), tenant, b, Input{}, "key"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	if len(child.calls) != 0 {
		t.Fatal("children started before capture publication")
	}
	control.actor.Status.WorkerAssignment.AssignmentGeneration = 99
	control.failCapture = false
	if _, err := s.Fork(t.Context(), tenant, b, Input{}, "key"); err != nil {
		t.Fatal(err)
	}
	if len(control.calls) != 2 || control.calls[0] != 7 || control.calls[1] != 7 {
		t.Fatal("retry captured another allocation", control.calls)
	}
}
func TestForkChildUnknownIsNotCleanedUp(t *testing.T) {
	s, _, control, child, tenant, b := fixture()
	count := uint32(2)
	child.failure = context.DeadlineExceeded
	if _, err := s.Fork(t.Context(), tenant, b, Input{Count: &count}, "key"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	if len(child.cancels) != 0 {
		t.Fatal("unknown child destroyed")
	}
	child.failure = nil
	out, err := s.Fork(t.Context(), tenant, b, Input{Count: &count}, "key")
	if err != nil || len(out) != 2 || out[0].Sandbox == nil || out[1].Sandbox == nil {
		t.Fatal(out, err)
	}
	if len(control.calls) != 1 || child.calls[out[0].Sandbox.ExternalID] != 1 || child.calls[out[1].Sandbox.ExternalID] != 2 {
		t.Fatal("sibling repeated")
	}
}
func TestForkValidatesBeforeMutation(t *testing.T) {
	s, store, _, _, tenant, b := fixture()
	zero := uint32(0)
	if _, err := s.Fork(t.Context(), tenant, b, Input{Count: &zero}, "key"); status.Code(err) != codes.InvalidArgument || len(store.jobs) != 0 {
		t.Fatal("invalid count reserved")
	}
	b.Tenant = "other"
	if _, err := s.Fork(t.Context(), tenant, b, Input{}, "key"); status.Code(err) != codes.InvalidArgument || len(store.jobs) != 0 {
		t.Fatal("cross tenant reserved")
	}
}

func (c *childRuntime) Confirmed(_ context.Context, _ auth.Tenant, b metadata.Sandbox, _ string) (bool, error) {
	_, ok := c.store.sandboxes[b.ExternalID]
	return ok || b.ExternalID == c.expiredConfirmed, nil
}

func TestForkDoesNotRecreateAlreadyExpiredConfirmedChild(t *testing.T) {
	s, _, control, child, tenant, b := fixture()
	child.expireFirst = true
	count := uint32(2)
	zero := uint32(0)
	out, err := s.Fork(t.Context(), tenant, b, Input{Count: &count, Timeout: &zero}, "key")
	if err != nil || len(out) != 2 || out[0].Error == "" || out[1].Sandbox == nil {
		t.Fatal(out, err)
	}
	if len(child.cancels) != 0 || child.calls[child.expiredConfirmed] != 1 || len(control.calls) != 1 {
		t.Fatal("expired confirmed child was recreated or cleaned again")
	}
	if _, err := s.Fork(t.Context(), tenant, b, Input{Count: &count, Timeout: &zero}, "key"); err != nil || child.calls[child.expiredConfirmed] != 1 {
		t.Fatal("completed fork resurrected expired child", err)
	}
}

func TestForkWaitsForPolicyAcknowledgment(t *testing.T) {
	s, store, control, _, tenant, b := fixture()
	control.actor.Status.AgentenvPolicyDelivery = &pb.AgentENVPolicyDelivery{Revision: 2, AppliedRevision: 1}
	if _, err := s.Fork(t.Context(), tenant, b, Input{}, "key"); status.Code(err) != codes.FailedPrecondition || len(store.jobs) != 0 {
		t.Fatal("unconfirmed policy inherited", err)
	}
}

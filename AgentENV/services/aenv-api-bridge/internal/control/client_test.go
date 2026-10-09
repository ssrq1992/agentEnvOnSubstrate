package control

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"agentenv/services/aenv-api-bridge/internal/auth"
	bridge "agentenv/services/aenv-api-bridge/internal/metadata"
	pb "github.com/agent-substrate/substrate/pkg/proto/ateapipb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

type controlStub struct {
	pb.ControlClient
	actor                  *pb.Actor
	createErr, errorDelete error
	authorization          []string
	createName             string
	deleted                *pb.DeleteActorRequest
	calls                  int
	lifecycleUID           string
	suspendGeneration      *uint64
	resumeSource           *string
}

func (s *controlStub) record(ctx context.Context) {
	s.calls++
	md, _ := metadata.FromOutgoingContext(ctx)
	s.authorization = md.Get("authorization")
}
func (s *controlStub) CreateActor(ctx context.Context, r *pb.CreateActorRequest, _ ...grpc.CallOption) (*pb.Actor, error) {
	s.record(ctx)
	s.createName = r.Actor.Metadata.Name
	return s.actor, s.createErr
}
func (s *controlStub) GetActor(ctx context.Context, _ *pb.GetActorRequest, _ ...grpc.CallOption) (*pb.Actor, error) {
	s.record(ctx)
	return s.actor, nil
}
func (s *controlStub) DeleteActor(ctx context.Context, r *pb.DeleteActorRequest, _ ...grpc.CallOption) (*pb.Actor, error) {
	s.record(ctx)
	s.deleted = r
	return s.actor, s.errorDelete
}
func fixture(t *testing.T) (*Client, *controlStub, auth.Tenant, bridge.Sandbox) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(path, []byte("tenant-token"), 0600); err != nil {
		t.Fatal(err)
	}
	tenant := auth.Tenant{ID: "tenant", Atespace: "space", ControlTokenFile: path}
	sandbox := bridge.Sandbox{Tenant: "tenant", ExternalID: "sdk-id", ActorAtespace: "space", ActorName: "stable-name"}
	stub := &controlStub{actor: &pb.Actor{Metadata: &pb.ResourceMetadata{Atespace: "space", Name: "stable-name", Uid: "actor-uid"}, ActorTemplate: &pb.ObjectRef{Atespace: "space", Name: "template"}}}
	return &Client{RPC: stub}, stub, tenant, sandbox
}
func TestCreateReconcilesSameActorAndUsesTenantCredentials(t *testing.T) {
	c, rpc, tenant, sandbox := fixture(t)
	rpc.createErr = status.Error(codes.AlreadyExists, "exists")
	ctx := metadata.NewOutgoingContext(t.Context(), metadata.Pairs("authorization", "Bearer attacker", "x-agentenv-tenant", "other"))
	actor, err := c.Create(ctx, tenant, sandbox, "template")
	if err != nil || actor.Metadata.Uid != "actor-uid" {
		t.Fatalf("create: %v %v", actor, err)
	}
	if rpc.calls != 2 || rpc.createName != sandbox.ActorName {
		t.Fatal("create retry changed target")
	}
	if len(rpc.authorization) != 1 || rpc.authorization[0] != "Bearer tenant-token" {
		t.Fatal("inherited credentials escaped")
	}
	rpc.actor.ActorTemplate.Name = "other"
	if _, err = c.Create(ctx, tenant, sandbox, "template"); err == nil {
		t.Fatal("conflicting template accepted")
	}
}
func TestTenantAndIncarnationIsolation(t *testing.T) {
	c, rpc, tenant, sandbox := fixture(t)
	sandbox.Tenant = "other"
	if _, err := c.Get(t.Context(), tenant, sandbox); err == nil || rpc.calls != 0 {
		t.Fatal("cross-tenant request reached control")
	}
	sandbox.Tenant = tenant.ID
	sandbox.ActorUID = "previous-uid"
	if _, err := c.Get(t.Context(), tenant, sandbox); err == nil {
		t.Fatal("different incarnation accepted")
	}
	if err := c.Delete(t.Context(), tenant, sandbox); err != nil {
		t.Fatal(err)
	}
	if rpc.deleted.Options.Uid != "previous-uid" || !rpc.deleted.AnyState {
		t.Fatal("delete lacks UID precondition")
	}
	rpc.errorDelete = status.Error(codes.Unavailable, "unknown termination")
	if err := c.Delete(t.Context(), tenant, sandbox); err == nil {
		t.Fatal("unknown result treated as completed")
	}
	rpc.errorDelete = status.Error(codes.NotFound, "absent")
	if err := c.Delete(t.Context(), tenant, sandbox); err != nil {
		t.Fatal(err)
	}
}

func (s *controlStub) ConnectActor(ctx context.Context, r *pb.ConnectActorRequest, _ ...grpc.CallOption) (*pb.ConnectActorResponse, error) {
	s.record(ctx)
	if r.Uid != "actor-uid" {
		return nil, status.Error(codes.FailedPrecondition, "incarnation changed")
	}
	return &pb.ConnectActorResponse{Actor: s.actor, EnvdAccessToken: "allocation-secret", EnvdVersion: "0.5.1"}, nil
}
func TestConnectBindsIncarnationAndRejectsNonRunningResult(t *testing.T) {
	c, rpc, tenant, b := fixture(t)
	if _, err := c.Connect(t.Context(), tenant, b); err == nil || rpc.calls != 0 {
		t.Fatal("connection without confirmed UID")
	}
	b.ActorUID = "actor-uid"
	if _, err := c.Connect(t.Context(), tenant, b); err == nil {
		t.Fatal("non-running connection accepted")
	}
	rpc.actor.Status = &pb.ActorStatus{State: pb.ActorState_ACTOR_STATE_RUNNING}
	got, err := c.Connect(t.Context(), tenant, b)
	if err != nil || got.GetEnvdAccessToken() != "allocation-secret" {
		t.Fatal("connection failed", err)
	}
	rpc.actor.Metadata.Uid = "replacement"
	if _, err := c.Connect(t.Context(), tenant, b); err == nil {
		t.Fatal("replacement Actor credential accepted")
	}
}

func (s *controlStub) SuspendActor(ctx context.Context, r *pb.SuspendActorRequest, _ ...grpc.CallOption) (*pb.SuspendActorResponse, error) {
	s.record(ctx)
	s.lifecycleUID = r.Uid
	s.suspendGeneration = r.AssignmentGeneration
	return &pb.SuspendActorResponse{Actor: s.actor}, nil
}
func (s *controlStub) ResumeActor(ctx context.Context, r *pb.ResumeActorRequest, _ ...grpc.CallOption) (*pb.ResumeActorResponse, error) {
	s.record(ctx)
	s.lifecycleUID = r.Uid
	s.resumeSource = r.SourceSnapshotUri
	return &pb.ResumeActorResponse{Actor: s.actor}, nil
}
func TestLifecycleCarriesIncarnationPrecondition(t *testing.T) {
	c, rpc, tenant, b := fixture(t)
	b.ActorUID = "actor-uid"
	if _, err := c.Suspend(t.Context(), tenant, b); err != nil {
		t.Fatal(err)
	}
	if rpc.calls != 1 || rpc.lifecycleUID != b.ActorUID {
		t.Fatal("suspend lacks atomic UID precondition")
	}
	if _, err := c.Resume(t.Context(), tenant, b); err != nil {
		t.Fatal(err)
	}
	if rpc.calls != 2 || rpc.lifecycleUID != b.ActorUID {
		t.Fatal("resume lacks atomic UID precondition")
	}
}

type metricsRPC struct {
	*controlStub
	response *pb.GetActorGuestMetricsResponse
	uid      string
}

func (s *metricsRPC) GetActorGuestMetrics(ctx context.Context, r *pb.GetActorGuestMetricsRequest, _ ...grpc.CallOption) (*pb.GetActorGuestMetricsResponse, error) {
	s.record(ctx)
	s.uid = r.Uid
	return s.response, nil
}
func TestGuestMetricsUsesTenantAndExactActor(t *testing.T) {
	c, base, tenant, b := fixture(t)
	b.ActorUID = "uid"
	rpc := &metricsRPC{controlStub: base, response: &pb.GetActorGuestMetricsResponse{ActorUid: "uid", Assignment: &pb.WorkerAssignment{ExecutorInstanceId: "executor"}, ObservedAtUnixMillis: 1, CpuUsedPercent: 12.5}}
	c.RPC = rpc
	got, err := c.GuestMetrics(t.Context(), tenant, b)
	if err != nil || got.CpuUsedPercent != 12.5 || rpc.uid != "uid" || len(rpc.authorization) != 1 || rpc.authorization[0] != "Bearer tenant-token" {
		t.Fatal("metrics attribution", err)
	}
	rpc.response.ActorUid = "other"
	if _, err = c.GuestMetrics(t.Context(), tenant, b); err == nil {
		t.Fatal("different Actor measurement accepted")
	}
	calls := rpc.calls
	b.Tenant = "other"
	if _, err = c.GuestMetrics(t.Context(), tenant, b); err == nil || rpc.calls != calls {
		t.Fatal("cross-tenant measurement")
	}
}

func TestPreparedSuspendBindsAllocationAndRequiresDurableSnapshot(t *testing.T) {
	c, rpc, tenant, b := fixture(t)
	b.ActorUID = rpc.actor.Metadata.Uid
	rpc.actor.Status = &pb.ActorStatus{State: pb.ActorState_ACTOR_STATE_RUNNING, WorkerAssignment: &pb.WorkerAssignment{AssignmentGeneration: 7}}
	generation, err := c.SuspendGeneration(t.Context(), tenant, b)
	if err != nil || generation != 7 {
		t.Fatal("generation", generation, err)
	}
	intent := bridge.SuspendIntent{Sandbox: b, Generation: generation}
	if err = c.SuspendPrepared(t.Context(), tenant, intent); status.Code(err) != codes.Unavailable {
		t.Fatal("running response acknowledged as pause", err)
	}
	rpc.actor.Status.State = pb.ActorState_ACTOR_STATE_SUSPENDED
	if err = c.SuspendPrepared(t.Context(), tenant, intent); status.Code(err) != codes.Unavailable {
		t.Fatal("pause without persistent snapshot acknowledged", err)
	}
	rpc.actor.Status.ExternalSnapshot = &pb.ExternalSnapshot{SnapshotUri: "s3://bucket/snapshot"}
	if err = c.SuspendPrepared(t.Context(), tenant, intent); err != nil {
		t.Fatal(err)
	}
	if rpc.suspendGeneration == nil || *rpc.suspendGeneration != 7 || rpc.lifecycleUID != b.ActorUID {
		t.Fatal("suspend preconditions were dropped")
	}
}

func TestPreparedResumeNeverFallsBackToColdStart(t *testing.T) {
	c, rpc, tenant, b := fixture(t)
	b.ActorUID = rpc.actor.Metadata.Uid
	rpc.actor.Status = &pb.ActorStatus{State: pb.ActorState_ACTOR_STATE_SUSPENDED}
	if _, _, err := c.ResumeSource(t.Context(), tenant, b); status.Code(err) != codes.FailedPrecondition {
		t.Fatal("missing persistent source accepted", err)
	}
	rpc.actor.Status.ExternalSnapshot = &pb.ExternalSnapshot{SnapshotUri: "s3://bucket/source"}
	source, running, err := c.ResumeSource(t.Context(), tenant, b)
	if err != nil || running || source != "s3://bucket/source" {
		t.Fatal("resume source changed", err)
	}
	intent := bridge.ResumeIntent{Sandbox: b, SourceURI: source}
	if err = c.ResumePrepared(t.Context(), tenant, intent); status.Code(err) != codes.Unavailable {
		t.Fatal("nonrunning resume acknowledged", err)
	}
	rpc.actor.Status.State = pb.ActorState_ACTOR_STATE_RUNNING
	rpc.actor.Status.WorkerAssignment = &pb.WorkerAssignment{AssignmentGeneration: 8}
	if err = c.ResumePrepared(t.Context(), tenant, intent); err != nil {
		t.Fatal(err)
	}
	if rpc.resumeSource == nil || *rpc.resumeSource != source || rpc.lifecycleUID != b.ActorUID {
		t.Fatal("source or UID fence missing")
	}
}

// Copyright 2026 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package controlapi

import (
	"context"
	"errors"
	"github.com/agent-substrate/substrate/cmd/ateapi/internal/store"
	"github.com/agent-substrate/substrate/cmd/ateapi/internal/store/storetest"
	"github.com/agent-substrate/substrate/internal/apierror"
	"github.com/agent-substrate/substrate/internal/installdefaults"
	private "github.com/agent-substrate/substrate/internal/proto/aenvexecutorpb"
	"github.com/agent-substrate/substrate/internal/proto/ateletpb"
	"github.com/agent-substrate/substrate/internal/resources"
	pb "github.com/agent-substrate/substrate/pkg/proto/ateapipb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/proto"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"net"
	"sync/atomic"
	"testing"
	"time"
)

type policyAssignmentStore struct {
	store.Interface
	actorUID string
}

func (s *policyAssignmentStore) GetWorker(context.Context, string) (*pb.Worker, error) {
	return &pb.Worker{SandboxClass: "agentenv", WorkerPodUid: "01900000-0000-7000-8000-000000000009", Epoch: 2, Status: &pb.WorkerStatus{ObservedEpoch: 2, RegisteredEpoch: 2, ExecutorInstanceId: "executor"}}, nil
}
func (s *policyAssignmentStore) GetWorkerAssignment(context.Context, string, string) (*pb.ActorAssignment, error) {
	return &pb.ActorAssignment{ActorUid: s.actorUID, WorkerEpoch: 2, ExecutorInstanceId: "executor", AssignmentGeneration: 7}, nil
}

type policyRuntime struct {
	ateletpb.UnimplementedAteomHerderServer
	fail, wrongAck  atomic.Bool
	seen            chan *private.ApplyNetworkPolicyRequest
	captures        atomic.Int32
	captureRequests chan *ateletpb.CheckpointRequest
}

func (f *policyRuntime) ApplyNetworkPolicy(_ context.Context, r *private.ApplyNetworkPolicyRequest) (*private.ApplyNetworkPolicyResponse, error) {
	f.seen <- proto.CloneOf(r)
	if f.fail.Load() {
		return nil, status.Error(codes.Unavailable, "execution outcome unknown")
	}
	revision := r.Policy.Revision
	if f.wrongAck.Load() {
		revision = 0
	}
	return &private.ApplyNetworkPolicyResponse{AppliedRevision: revision}, nil
}
func policyFixture(t *testing.T, running bool) (*RPCService, *ActorWorkflow, store.Interface, resources.ActorRef, *policyRuntime) {
	t.Helper()
	ctx := t.Context()
	st, cleanup := storetest.SetupTestStore(t)
	t.Cleanup(cleanup)
	w := newTestActorWorkflow(t, st, "ns", "tmpl1")
	template, err := st.GetActorTemplate(ctx, resources.ActorTemplateRef{Atespace: "ns", Name: "tmpl1"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = st.UpdateActorTemplate(ctx, resources.ActorTemplateRef{Atespace: "ns", Name: "tmpl1"}, store.PreconditionFrom(template), func(p *pb.ActorTemplate) error {
		p.SandboxConfig.SandboxClass = pb.SandboxClass_SANDBOX_CLASS_AGENTENV
		p.SandboxConfig.ConfigName = "agentenv"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	ref := resources.ActorRef{Atespace: "team-a", Name: "id1"}
	seedWorkflowActor(t, ctx, st, ref, "ns", "tmpl1", pb.ActorState_ACTOR_STATE_SUSPENDED)
	actor, err := st.GetActor(ctx, ref)
	if err != nil {
		t.Fatal(err)
	}
	w.store = &ServiceImpl{store: st}
	f := &policyRuntime{seen: make(chan *private.ApplyNetworkPolicyRequest, 20)}
	if running {
		actor, err = st.UpdateActor(ctx, ref, store.PreconditionFrom(actor), func(a *pb.Actor) error {
			a.Status.State = pb.ActorState_ACTOR_STATE_RUNNING
			a.Status.AssignedNode = "node-1"
			a.Status.WorkerAssignment = &pb.WorkerAssignment{WorkerNamespace: "ate-system", WorkerPool: "pool", WorkerPod: "worker-pod", WorkerPodIps: []string{"10.0.0.2"}, NodeName: "node-1", Worker: &pb.ObjectRef{Name: "worker"}, WorkerPodUid: "01900000-0000-7000-8000-000000000009", WorkerEpoch: 2, ExecutorInstanceId: "executor", AssignmentGeneration: 7}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		w.store = &ServiceImpl{store: &policyAssignmentStore{Interface: st, actorUID: actor.Metadata.Uid}}
		srv := grpc.NewServer()
		ateletpb.RegisterAteomHerderServer(srv, f)
		lis := bufconn.Listen(1 << 20)
		go srv.Serve(lis)
		conn, e := grpc.NewClient("passthrough:///policy", grpc.WithTransportCredentials(insecure.NewCredentials()), grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return lis.DialContext(ctx) }))
		if e != nil {
			t.Fatal(e)
		}
		t.Cleanup(func() { conn.Close(); srv.Stop() })
		pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: installdefaults.SystemNamespace, Name: "atelet-1", UID: "atelet-uid"}, Spec: corev1.PodSpec{NodeName: "node-1"}, Status: corev1.PodStatus{PodIPs: []corev1.PodIP{{IP: "10.0.0.1"}}}}
		w.dialer = NewAteletDialer(newTestAteletIndexer(t, pod), installdefaults.SystemNamespace, "", "")
		w.dialer.ateletConns.Add("atelet-uid", &ateletConn{ip: "10.0.0.1", conn: conn})
	}
	return &RPCService{impl: &ServiceImpl{store: st}, actorWorkflow: w}, w, st, ref, f
}
func policyInput(ref resources.ActorRef) *pb.EgressPolicy {
	return &pb.EgressPolicy{Metadata: &pb.ResourceMetadata{Atespace: ref.Atespace, Name: "default"}, Agentenv: &pb.AgentENVNetworkPolicy{Base: pb.AgentENVNetworkPolicy_DENY, AllowOut: []string{"*.example.com", "1.1.1.1/32"}, DenyOut: []string{"0.0.0.0/0"}}}
}
func TestAgentENVPolicyDeferredCRUDAndMonotonicRecreation(t *testing.T) {
	s, _, st, ref, _ := policyFixture(t, false)
	input := policyInput(ref)
	input.AgentenvDelivery = &pb.AgentENVPolicyDelivery{Revision: 999, AppliedRevision: 999}
	created, err := s.CreateActorEgressPolicy(t.Context(), &pb.CreateActorEgressPolicyRequest{Actor: ref.ToObjectRef(), EgressPolicy: input})
	if err != nil {
		t.Fatal(err)
	}
	if d := created.AgentenvDelivery; d.Revision != 1 || d.AppliedRevision != 0 {
		t.Fatal("suspended policy claimed runtime acknowledgment", d)
	}
	update := proto.CloneOf(created)
	update.Agentenv.Base = pb.AgentENVNetworkPolicy_ALLOW
	updated, err := s.UpdateActorEgressPolicy(t.Context(), &pb.UpdateActorEgressPolicyRequest{Actor: ref.ToObjectRef(), EgressPolicy: update})
	if err != nil {
		t.Fatal(err)
	}
	if updated.AgentenvDelivery.Revision != 2 {
		t.Fatal("policy revision did not advance")
	}
	if _, err = s.DeleteActorEgressPolicy(t.Context(), &pb.DeleteActorEgressPolicyRequest{Actor: ref.ToObjectRef()}); err != nil {
		t.Fatal(err)
	}
	if _, err = st.GetEgressPolicy(t.Context(), ref); !errors.Is(err, store.ErrNotFound) {
		t.Fatal("policy row remains", err)
	}
	actor, err := st.GetActor(t.Context(), ref)
	if err != nil {
		t.Fatal(err)
	}
	if d := actor.Status.AgentenvPolicyDelivery; d.Revision != 3 || d.Deleting || d.Policy.Base != pb.AgentENVNetworkPolicy_DEFAULT {
		t.Fatal("default restore intent missing", d)
	}
	recreated, err := s.CreateActorEgressPolicy(t.Context(), &pb.CreateActorEgressPolicyRequest{Actor: ref.ToObjectRef(), EgressPolicy: policyInput(ref)})
	if err != nil {
		t.Fatal(err)
	}
	if recreated.Metadata.Version != 1 || recreated.AgentenvDelivery.Revision != 4 {
		t.Fatal("policy recreation reused runtime revision")
	}
}
func TestAgentENVPolicyUnknownResultAndDeleteReconcile(t *testing.T) {
	s, w, st, ref, f := policyFixture(t, true)
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	f.fail.Store(true)
	if _, err := s.CreateActorEgressPolicy(ctx, &pb.CreateActorEgressPolicyRequest{Actor: ref.ToObjectRef(), EgressPolicy: policyInput(ref)}); apierror.Code(err) != codes.Unavailable {
		t.Fatal("unknown update acknowledged", err)
	}
	first := <-f.seen
	pending, err := s.GetActorEgressPolicy(ctx, &pb.GetActorEgressPolicyRequest{Actor: ref.ToObjectRef()})
	if err != nil {
		t.Fatal(err)
	}
	if pending.AgentenvDelivery.Revision != 1 || pending.AgentenvDelivery.AppliedRevision != 0 {
		t.Fatal("pending intent lost")
	}
	f.fail.Store(false)
	f.wrongAck.Store(true)
	if err = w.reconcileAgentENVPolicy(ctx, ref); apierror.Code(err) != codes.Unavailable {
		t.Fatal("wrong ACK accepted", err)
	}
	<-f.seen
	f.wrongAck.Store(false)
	if err = w.reconcileAgentENVPolicy(ctx, ref); err != nil {
		t.Fatal(err)
	}
	retry := <-f.seen
	if !proto.Equal(first, retry) {
		t.Fatal("restart reconciliation changed operation identity")
	}
	actor, err := st.GetActor(ctx, ref)
	if err != nil || !policyAcknowledged(actor) {
		t.Fatal("confirmed revision not committed", err)
	}
	f.fail.Store(true)
	if _, err = s.DeleteActorEgressPolicy(ctx, &pb.DeleteActorEgressPolicyRequest{Actor: ref.ToObjectRef()}); apierror.Code(err) != codes.Unavailable {
		t.Fatal("unknown delete acknowledged", err)
	}
	deletion := <-f.seen
	if deletion.Policy.Base != private.NetworkPolicy_DEFAULT || deletion.Policy.Revision != 2 {
		t.Fatal("delete did not stage Default")
	}
	if _, err = st.GetEgressPolicy(ctx, ref); err != nil {
		t.Fatal("policy removed before runtime confirmation")
	}
	if _, err = s.UpdateActorEgressPolicy(ctx, &pb.UpdateActorEgressPolicyRequest{Actor: ref.ToObjectRef(), EgressPolicy: pending}); apierror.Code(err) != codes.Aborted {
		t.Fatal("pending delete overwritten", err)
	}
	f.fail.Store(false)
	if err = w.reconcileAgentENVPolicy(ctx, ref); err != nil {
		t.Fatal(err)
	}
	if retry := <-f.seen; !proto.Equal(deletion, retry) {
		t.Fatal("deletion retry changed its intent")
	}
	if _, err = st.GetEgressPolicy(ctx, ref); !errors.Is(err, store.ErrNotFound) {
		t.Fatal("confirmed deletion not finalized", err)
	}
}
func TestAgentENVPolicyRepairsRowBeforeIntentCrash(t *testing.T) {
	_, w, st, ref, _ := policyFixture(t, false)
	if _, err := st.CreateEgressPolicy(t.Context(), ref, policyInput(ref)); err != nil {
		t.Fatal(err)
	}
	if err := w.reconcileAgentENVPolicy(t.Context(), ref); err != nil {
		t.Fatal(err)
	}
	actor, err := st.GetActor(t.Context(), ref)
	if err != nil {
		t.Fatal(err)
	}
	if actor.Status.AgentenvPolicyDelivery.GetRevision() != 1 || nativePolicy(actor.Status.AgentenvPolicyDelivery).Base != private.NetworkPolicy_DENY {
		t.Fatal("unprojected policy lost on restart")
	}
}

func TestAgentENVPolicyRejectsWrongBackendBeforePersistence(t *testing.T) {
	s, _, st, ref, _ := policyFixture(t, false)
	gateway := &pb.EgressPolicy{Metadata: &pb.ResourceMetadata{Atespace: ref.Atespace, Name: "default"}}
	if _, err := s.CreateActorEgressPolicy(t.Context(), &pb.CreateActorEgressPolicyRequest{Actor: ref.ToObjectRef(), EgressPolicy: gateway}); apierror.Code(err) != codes.FailedPrecondition {
		t.Fatal("gateway policy silently accepted for AgentENV", err)
	}
	template, err := st.GetActorTemplate(t.Context(), resources.ActorTemplateRef{Atespace: "ns", Name: "tmpl1"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = st.UpdateActorTemplate(t.Context(), resources.ActorTemplateRef{Atespace: "ns", Name: "tmpl1"}, store.PreconditionFrom(template), func(p *pb.ActorTemplate) error {
		p.SandboxConfig.SandboxClass = pb.SandboxClass_SANDBOX_CLASS_GVISOR
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err = s.CreateActorEgressPolicy(t.Context(), &pb.CreateActorEgressPolicyRequest{Actor: ref.ToObjectRef(), EgressPolicy: policyInput(ref)}); apierror.Code(err) != codes.FailedPrecondition {
		t.Fatal("AgentENV policy accepted for gVisor", err)
	}
	if _, err = st.GetEgressPolicy(t.Context(), ref); !errors.Is(err, store.ErrNotFound) {
		t.Fatal("rejected policy was persisted")
	}
}
func TestAgentENVPolicyReconcilesDeleteAfterRowCommit(t *testing.T) {
	_, w, st, ref, _ := policyFixture(t, false)
	policy, err := st.CreateEgressPolicy(t.Context(), ref, policyInput(ref))
	if err != nil {
		t.Fatal(err)
	}
	actor, err := st.GetActor(t.Context(), ref)
	if err != nil {
		t.Fatal(err)
	}
	actor, err = w.stagePolicy(t.Context(), actor, policy, true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = st.DeleteEgressPolicy(t.Context(), ref, store.DeletePreconditions{UID: policy.Metadata.Uid}); err != nil {
		t.Fatal(err)
	}
	if err = w.reconcileAgentENVPolicy(t.Context(), ref); err != nil {
		t.Fatal(err)
	}
	current, err := st.GetActor(t.Context(), ref)
	if err != nil {
		t.Fatal(err)
	}
	if d := current.Status.AgentenvPolicyDelivery; d.Deleting || d.PolicyUid != "" || d.Revision != actor.Status.AgentenvPolicyDelivery.Revision {
		t.Fatal("committed delete was not repaired", d)
	}
}

func TestAgentENVPolicyLivePreconditionsBeforePersistence(t *testing.T) {
	s, _, st, ref, _ := policyFixture(t, false)
	actor, err := st.GetActor(t.Context(), ref)
	if err != nil {
		t.Fatal(err)
	}
	for _, pre := range []*pb.AgentENVPolicyPreconditions{
		{ActorUid: "01900000-0000-7000-8000-000000000099"},
		{ActorUid: actor.Metadata.Uid, RequireRunning: true},
	} {
		_, err = s.CreateActorEgressPolicy(t.Context(), &pb.CreateActorEgressPolicyRequest{Actor: ref.ToObjectRef(), EgressPolicy: policyInput(ref), AgentenvPreconditions: pre})
		if apierror.Code(err) != codes.FailedPrecondition {
			t.Fatalf("precondition ignored: %v", err)
		}
		if _, err = st.GetEgressPolicy(t.Context(), ref); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("failed precondition persisted policy: %v", err)
		}
	}
	created, err := s.CreateActorEgressPolicy(t.Context(), &pb.CreateActorEgressPolicyRequest{Actor: ref.ToObjectRef(), EgressPolicy: policyInput(ref), AgentenvPreconditions: &pb.AgentENVPolicyPreconditions{ActorUid: actor.Metadata.Uid}})
	if err != nil {
		t.Fatal(err)
	}
	repeated, err := s.UpdateActorEgressPolicy(t.Context(), &pb.UpdateActorEgressPolicyRequest{Actor: ref.ToObjectRef(), EgressPolicy: proto.CloneOf(created)})
	if err != nil {
		t.Fatal(err)
	}
	if repeated.Metadata.Version != created.Metadata.Version || repeated.AgentenvDelivery.Revision != created.AgentenvDelivery.Revision {
		t.Fatal("identical retry allocated a new revision")
	}
}

func TestAgentENVPolicyStalePreparedRequestCannotOverwrite(t *testing.T) {
	s, _, _, ref, _ := policyFixture(t, false)
	zero := uint64(0)
	pre := &pb.AgentENVPolicyPreconditions{ExpectedPolicyRevision: &zero}
	old := policyInput(ref)
	first, err := s.CreateActorEgressPolicy(t.Context(), &pb.CreateActorEgressPolicyRequest{Actor: ref.ToObjectRef(), EgressPolicy: proto.CloneOf(old), AgentenvPreconditions: pre})
	if err != nil {
		t.Fatal(err)
	}
	future := uint64(999)
	if _, err = s.UpdateActorEgressPolicy(t.Context(), &pb.UpdateActorEgressPolicyRequest{Actor: ref.ToObjectRef(), EgressPolicy: proto.CloneOf(first), AgentenvPreconditions: &pb.AgentENVPolicyPreconditions{ExpectedPolicyRevision: &future}}); apierror.Code(err) != codes.Aborted {
		t.Fatal("future revision accepted", err)
	}
	// An unknown-result retry of the identical desired policy can confirm it.
	if _, err = s.UpdateActorEgressPolicy(t.Context(), &pb.UpdateActorEgressPolicyRequest{Actor: ref.ToObjectRef(), EgressPolicy: proto.CloneOf(first), AgentenvPreconditions: pre}); err != nil {
		t.Fatal(err)
	}
	newer := proto.CloneOf(first)
	newer.Agentenv.Base = pb.AgentENVNetworkPolicy_ALLOW
	updated, err := s.UpdateActorEgressPolicy(t.Context(), &pb.UpdateActorEgressPolicyRequest{Actor: ref.ToObjectRef(), EgressPolicy: newer})
	if err != nil {
		t.Fatal(err)
	}
	// Even re-reading fresh resource metadata must not revive an older intent.
	stale := proto.CloneOf(updated)
	stale.Agentenv = old.Agentenv
	if _, err = s.UpdateActorEgressPolicy(t.Context(), &pb.UpdateActorEgressPolicyRequest{Actor: ref.ToObjectRef(), EgressPolicy: stale, AgentenvPreconditions: pre}); apierror.Code(err) != codes.Aborted {
		t.Fatalf("historical request overwrote current policy: %v", err)
	}
	got, err := s.GetActorEgressPolicy(t.Context(), &pb.GetActorEgressPolicyRequest{Actor: ref.ToObjectRef()})
	if err != nil {
		t.Fatal(err)
	}
	if !proto.Equal(got.Agentenv, updated.Agentenv) || got.AgentenvDelivery.Revision != updated.AgentenvDelivery.Revision {
		t.Fatal("stale request changed policy")
	}
	if _, err = s.DeleteActorEgressPolicy(t.Context(), &pb.DeleteActorEgressPolicyRequest{Actor: ref.ToObjectRef()}); err != nil {
		t.Fatal(err)
	}
	if _, err = s.CreateActorEgressPolicy(t.Context(), &pb.CreateActorEgressPolicyRequest{Actor: ref.ToObjectRef(), EgressPolicy: proto.CloneOf(old), AgentenvPreconditions: pre}); apierror.Code(err) != codes.Aborted {
		t.Fatalf("delete/recreate revived stale intent: %v", err)
	}
}

func (f *policyRuntime) Capture(_ context.Context, req *ateletpb.CheckpointRequest) (*ateletpb.CheckpointResponse, error) {
	f.captures.Add(1)
	if f.captureRequests != nil {
		f.captureRequests <- proto.CloneOf(req)
	}
	if f.fail.Load() {
		return nil, status.Error(codes.Unavailable, "capture outcome unknown")
	}
	if req.Scope != ateletpb.SnapshotScope_SNAPSHOT_SCOPE_FULL || req.Execution.GetOperationId() == "" {
		return nil, status.Error(codes.InvalidArgument, "unfenced capture")
	}
	return &ateletpb.CheckpointResponse{}, nil
}
func TestAgentENVCaptureRetriesFrozenDestination(t *testing.T) {
	s, _, st, ref, runtime := policyFixture(t, true)
	actor, err := st.GetActor(t.Context(), ref)
	if err != nil {
		t.Fatal(err)
	}
	req := &pb.CaptureActorSnapshotRequest{Actor: ref.ToObjectRef(), Uid: actor.Metadata.Uid, AssignmentGeneration: 7, TagName: "capture"}
	runtime.captureRequests = make(chan *ateletpb.CheckpointRequest, 4)
	runtime.fail.Store(true)
	if _, err := s.CaptureActorSnapshot(t.Context(), req); err == nil {
		t.Fatal("unknown outcome published")
	}
	reserved, err := st.GetTag(t.Context(), resources.TagRef{Atespace: ref.Atespace, Name: "capture"})
	if err != nil {
		t.Fatal(err)
	}
	if reserved.Status.Snapshot != nil {
		t.Fatal("pending capture published")
	}
	first := <-runtime.captureRequests
	if len(reserved.Status.CaptureRequest) == 0 {
		t.Fatal("execution intent not persisted")
	}
	if _, err := s.DeleteTag(t.Context(), &pb.DeleteTagRequest{Tag: &pb.ObjectRef{Atespace: ref.Atespace, Name: "capture"}}); apierror.Code(err) != codes.FailedPrecondition {
		t.Fatalf("unresolved capture deleted: %v", err)
	}
	runtime.fail.Store(false)
	captured, err := s.CaptureActorSnapshot(t.Context(), req)
	if err != nil {
		t.Fatal(err)
	}
	if !proto.Equal(first, <-runtime.captureRequests) {
		t.Fatal("retry changed persisted execution payload")
	}
	if len(captured.Status.CaptureRequest) != 0 {
		t.Fatal("private payload escaped public RPC")
	}
	if captured.Metadata.Uid != reserved.Metadata.Uid || captured.Status.Snapshot.GetContentScope() != pb.SnapshotContentScope_SNAPSHOT_CONTENT_SCOPE_FULL {
		t.Fatal("retry changed destination")
	}
	if _, err := s.CaptureActorSnapshot(t.Context(), req); err != nil {
		t.Fatal(err)
	}
	if runtime.captures.Load() != 2 {
		t.Fatal("completed retry recaptured")
	}
	after, err := st.GetActor(t.Context(), ref)
	if err != nil || !proto.Equal(actor, after) {
		t.Fatal("capture changed source Actor", err)
	}
	different := proto.CloneOf(req)
	different.AssignmentGeneration++
	if _, err := s.CaptureActorSnapshot(t.Context(), different); apierror.Code(err) != codes.AlreadyExists {
		t.Fatalf("different operation reused destination: %v", err)
	}
}

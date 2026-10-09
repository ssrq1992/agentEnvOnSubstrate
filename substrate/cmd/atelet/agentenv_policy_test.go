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

package main

import (
	"context"
	pb "github.com/agent-substrate/substrate/internal/proto/aenvexecutorpb"
	"github.com/agent-substrate/substrate/internal/proto/ateletpb"
	"github.com/agent-substrate/substrate/internal/proto/ateompb"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"path/filepath"
	"strings"
	"testing"
)

type policyAteom struct {
	ateompb.UnimplementedAteomServer
	received chan *pb.ApplyNetworkPolicyRequest
}

func (s *policyAteom) ApplyNetworkPolicy(_ context.Context, r *pb.ApplyNetworkPolicyRequest) (*pb.ApplyNetworkPolicyResponse, error) {
	s.received <- proto.CloneOf(r)
	revision := r.Policy.Revision
	if revision == 99 {
		revision = 0
	}
	return &pb.ApplyNetworkPolicyResponse{AppliedRevision: revision}, nil
}
func TestAgentENVPolicyUpdateChecksAllocationAndAck(t *testing.T) {
	previous := agentENVPreparationsDir
	agentENVPreparationsDir = t.TempDir()
	t.Cleanup(func() { agentENVPreparationsDir = previous })
	uid := "01900000-0000-7000-8000-000000000001"
	op := &pb.LifecycleOperation{OperationId: "activate", Fence: &pb.Fence{ProtocolVersion: "agentenv-executor-v1", ActorUid: uid, WorkerPodUid: "pod", WorkerEpoch: 2, WorkerInstanceId: "executor", AssignmentGeneration: 7}}
	if _, err := persistAgentENVLaunch(filepath.Join(agentENVPreparationsDir, uid), op, &pb.LaunchSpec{Vcpus: 1, MemoryMib: 512}); err != nil {
		t.Fatal(err)
	}
	fake := &policyAteom{received: make(chan *pb.ApplyNetworkPolicyRequest, 2)}
	serveFakeAteom(t, fake)
	herder := &AteomHerder{ateomDialer: newAteomDialer(1)}
	req := &pb.ApplyNetworkPolicyRequest{ActorUid: uid, TargetWorkerPodUid: "pod", Execution: proto.CloneOf(op), Policy: &pb.NetworkPolicy{Revision: 3, Base: pb.NetworkPolicy_DENY, AllowOut: []string{"example.com"}}}
	req.Execution.OperationId = "policy-3"
	if response, err := herder.ApplyNetworkPolicy(t.Context(), req); err != nil || response.AppliedRevision != 3 {
		t.Fatalf("policy apply: %v %v", response, err)
	}
	if got := <-fake.received; !proto.Equal(got, req) {
		t.Fatal("atelet changed durable operation identity or policy")
	}
	stale := proto.CloneOf(req)
	stale.Execution.Fence.AssignmentGeneration++
	if _, err := herder.ApplyNetworkPolicy(t.Context(), stale); status.Code(err) != codes.FailedPrecondition {
		t.Fatal("stale allocation accepted", err)
	}
	if len(fake.received) != 0 {
		t.Fatal("stale allocation reached Worker")
	}
	req.Policy.Revision = 99
	if _, err := herder.ApplyNetworkPolicy(t.Context(), req); status.Code(err) != codes.Unavailable {
		t.Fatal("mismatched ACK accepted", err)
	}
	if err := validateAgentENVExisting(uid, op); err != nil {
		t.Fatal("policy update changed launch retry identity")
	}
}

func TestLaunchPinsExplicitPolicyAndPreservesAbsentRestorePolicy(t *testing.T) {
	previous := agentENVPreparationsDir
	agentENVPreparationsDir = t.TempDir()
	t.Cleanup(func() { agentENVPreparationsDir = previous })
	uid := "01900000-0000-7000-8000-000000000001"
	op := &pb.LifecycleOperation{OperationId: "activate", Fence: &pb.Fence{ProtocolVersion: "agentenv-executor-v1", ActorUid: uid, WorkerPodUid: "pod", WorkerEpoch: 2, WorkerInstanceId: "executor", AssignmentGeneration: 7}}
	spec := &ateletpb.WorkloadSpec{Containers: []*ateletpb.Container{{Image: "image@sha256:" + strings.Repeat("a", 64)}}}
	rec := &sandboxAssetsRecord{SandboxClass: "agentenv"}
	herder := &AteomHerder{}
	policy := &pb.NetworkPolicy{Base: pb.NetworkPolicy_DENY, Revision: 9, AllowOut: []string{"*.example.com"}}
	launch, err := herder.prepareAgentENV(t.Context(), uid, op, spec, rec, nil, 1000, 512*1024*1024, policy)
	if err != nil {
		t.Fatal(err)
	}
	if !proto.Equal(launch.GetNetwork().GetPolicy(), policy) {
		t.Fatal("launch lost persisted policy")
	}
	replay, err := herder.prepareAgentENV(t.Context(), uid, op, spec, rec, nil, 1000, 512*1024*1024, policy)
	if err != nil || !proto.Equal(replay, launch) {
		t.Fatal("same policy changed activation payload", err)
	}
	policy.Revision++
	if _, err = herder.prepareAgentENV(t.Context(), uid, op, spec, rec, nil, 1000, 512*1024*1024, policy); err == nil {
		t.Fatal("activation retry changed policy")
	}
	if _, err = herder.prepareAgentENV(t.Context(), uid, op, spec, rec, nil, 1000, 512*1024*1024, nil); err == nil {
		t.Fatal("activation retry erased explicit policy")
	}
	uid = "01900000-0000-7000-8000-000000000002"
	op.Fence.ActorUid = uid
	restored, err := herder.prepareAgentENV(t.Context(), uid, op, spec, rec, nil, 1000, 512*1024*1024, nil)
	if err != nil {
		t.Fatal(err)
	}
	if restored.GetNetwork().GetPolicy() != nil {
		t.Fatal("missing restore override became explicit Default")
	}
}

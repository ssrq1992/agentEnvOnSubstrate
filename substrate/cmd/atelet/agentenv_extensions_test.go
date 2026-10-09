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
	"github.com/agent-substrate/substrate/internal/proto/ateompb"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"path/filepath"
	"testing"
)

type extensionAteom struct {
	ateompb.UnimplementedAteomServer
	received chan *pb.ApplyExtensionParamsRequest
}

func (s *extensionAteom) ApplyExtensionParams(_ context.Context, r *pb.ApplyExtensionParamsRequest) (*pb.ApplyExtensionParamsResponse, error) {
	s.received <- proto.CloneOf(r)
	if r.Patch.Json == `{"rejected":true}` {
		return &pb.ApplyExtensionParamsResponse{Effect: pb.Effect_NO_EFFECT}, nil
	}
	if r.Patch.Json == `{"missing":true}` {
		return &pb.ApplyExtensionParamsResponse{Effect: pb.Effect_COMPLETED}, nil
	}
	return &pb.ApplyExtensionParamsResponse{Effect: pb.Effect_COMPLETED, Approved: &pb.ExtensionParams{Json: `{"approved":1}`}}, nil
}
func TestAgentENVExtensionUpdateChecksAllocationAndApproval(t *testing.T) {
	previous := agentENVPreparationsDir
	agentENVPreparationsDir = t.TempDir()
	t.Cleanup(func() { agentENVPreparationsDir = previous })
	uid := "01900000-0000-7000-8000-000000000001"
	op := &pb.LifecycleOperation{OperationId: "activate", Fence: &pb.Fence{ProtocolVersion: "agentenv-executor-v1", ActorUid: uid, WorkerPodUid: "pod", WorkerEpoch: 2, WorkerInstanceId: "executor", AssignmentGeneration: 7}}
	if _, err := persistAgentENVLaunch(filepath.Join(agentENVPreparationsDir, uid), op, &pb.LaunchSpec{Vcpus: 1, MemoryMib: 512}); err != nil {
		t.Fatal(err)
	}
	fake := &extensionAteom{received: make(chan *pb.ApplyExtensionParamsRequest, 4)}
	serveFakeAteom(t, fake)
	herder := &AteomHerder{ateomDialer: newAteomDialer(1)}
	req := &pb.ApplyExtensionParamsRequest{ActorUid: uid, TargetWorkerPodUid: "pod", Execution: proto.CloneOf(op), Patch: &pb.ExtensionParams{Json: "{}"}}
	req.Execution.OperationId = "extension-patch"
	response, err := herder.ApplyExtensionParams(t.Context(), req)
	if err != nil || response.GetApproved().GetJson() != `{"approved":1}` {
		t.Fatal("approval lost", err)
	}
	if !proto.Equal(<-fake.received, req) {
		t.Fatal("durable extension request changed")
	}
	stale := proto.CloneOf(req)
	stale.Execution.Fence.AssignmentGeneration++
	if _, err := herder.ApplyExtensionParams(t.Context(), stale); status.Code(err) != codes.FailedPrecondition || len(fake.received) != 0 {
		t.Fatal("stale extension reached Worker", err)
	}
	req.Patch.Json = `{"missing":true}`
	if _, err := herder.ApplyExtensionParams(t.Context(), req); status.Code(err) != codes.Unavailable {
		t.Fatal("missing approval accepted", err)
	}
	<-fake.received
	req.Patch.Json = `{"rejected":true}`
	if response, err := herder.ApplyExtensionParams(t.Context(), req); err != nil || response.Effect != pb.Effect_NO_EFFECT {
		t.Fatal("no-effect acknowledgment lost", err)
	}
	<-fake.received
	if err := validateAgentENVExisting(uid, op); err != nil {
		t.Fatal("extension changed allocation preparation", err)
	}
}

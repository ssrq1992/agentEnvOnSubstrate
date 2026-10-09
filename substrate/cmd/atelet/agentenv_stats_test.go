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

type statsAteom struct {
	ateompb.UnimplementedAteomServer
	received chan *pb.ReadGuestStatsRequest
}

func (s *statsAteom) ReadGuestStats(_ context.Context, r *pb.ReadGuestStatsRequest) (*pb.StatsResponse, error) {
	s.received <- proto.CloneOf(r)
	f := proto.CloneOf(r.Execution.Fence)
	if r.Execution.OperationId == "wrong" {
		f.AssignmentGeneration++
	}
	return &pb.StatsResponse{Fence: f, CpuCount: 2, ObservedAtUnixMillis: 1, MemoryTotalBytes: 100, CpuUsedPercent: 25}, nil
}
func TestGuestMetricsForwardingRequiresPreparedAssignment(t *testing.T) {
	previous := agentENVPreparationsDir
	agentENVPreparationsDir = t.TempDir()
	t.Cleanup(func() { agentENVPreparationsDir = previous })
	uid := "01900000-0000-7000-8000-000000000001"
	op := &pb.LifecycleOperation{OperationId: "metrics", Fence: &pb.Fence{ProtocolVersion: "agentenv-executor-v1", ActorUid: uid, WorkerPodUid: "pod", WorkerEpoch: 2, WorkerInstanceId: "executor", AssignmentGeneration: 7}}
	if _, err := persistAgentENVLaunch(filepath.Join(agentENVPreparationsDir, uid), op, &pb.LaunchSpec{Vcpus: 1, MemoryMib: 512}); err != nil {
		t.Fatal(err)
	}
	fake := &statsAteom{received: make(chan *pb.ReadGuestStatsRequest, 2)}
	serveFakeAteom(t, fake)
	herder := &AteomHerder{ateomDialer: newAteomDialer(1)}
	req := &pb.ReadGuestStatsRequest{ActorUid: uid, TargetWorkerPodUid: "pod", Execution: proto.CloneOf(op)}
	if got, err := herder.ReadGuestStats(t.Context(), req); err != nil || got.CpuUsedPercent != 25 {
		t.Fatal("stats", got, err)
	}
	if got := <-fake.received; !proto.Equal(got, req) {
		t.Fatal("routing changed fence")
	}
	req.Execution.Fence.AssignmentGeneration++
	if _, err := herder.ReadGuestStats(t.Context(), req); status.Code(err) != codes.FailedPrecondition || len(fake.received) != 0 {
		t.Fatal("stale generation forwarded", err)
	}
	req.Execution.Fence.AssignmentGeneration--
	req.Execution.OperationId = "wrong"
	if _, err := herder.ReadGuestStats(t.Context(), req); status.Code(err) != codes.FailedPrecondition {
		t.Fatal("wrong sample attribution accepted", err)
	}
}

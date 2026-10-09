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

package adapter

import (
	"context"
	pb "github.com/agent-substrate/substrate/internal/proto/aenvexecutorpb"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"testing"
)

type statsOperations struct {
	Operations
	calls int
	wrong bool
}

func (s *statsOperations) Stats(_ context.Context, f *pb.Fence) (*pb.StatsResponse, error) {
	s.calls++
	fence := proto.CloneOf(f)
	if s.wrong {
		fence.AssignmentGeneration++
	}
	return &pb.StatsResponse{Fence: fence, CpuCount: 2, ObservedAtUnixMillis: 1, MemoryUsedBytes: 10, MemoryTotalBytes: 20, CpuUsedPercent: 50}, nil
}
func TestGuestMetricsAreFencedAndReadOnly(t *testing.T) {
	s, ops, network, ingress, launch := serviceFixture(t)
	reader := &statsOperations{Operations: ops}
	s.Ops = reader
	req := &pb.ReadGuestStatsRequest{ActorUid: launch.ActorUid, TargetWorkerPodUid: "pod", Execution: proto.CloneOf(launch.Execution)}
	if got, err := s.ReadGuestStats(t.Context(), req); err != nil || got.CpuUsedPercent != 50 {
		t.Fatal("metrics", got, err)
	}
	req.Execution.Fence.WorkerEpoch--
	if _, err := s.ReadGuestStats(t.Context(), req); status.Code(err) != codes.FailedPrecondition || reader.calls != 1 {
		t.Fatal("stale epoch reached executor", err)
	}
	req.Execution.Fence.WorkerEpoch++
	reader.wrong = true
	if _, err := s.ReadGuestStats(t.Context(), req); status.Code(err) != codes.Unavailable {
		t.Fatal("wrong assignment measured", err)
	}
	if len(ops.calls) != 0 || network.attached != 0 || network.detached != 0 || ingress.activated != 0 || ingress.deactivated != 0 {
		t.Fatal("metrics mutated runtime")
	}
}

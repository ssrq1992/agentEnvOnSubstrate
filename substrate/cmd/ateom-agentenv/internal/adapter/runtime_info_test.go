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

type inspectOperations struct {
	Operations
	calls   int
	version string
	state   string
	wrong   bool
}

func (s *inspectOperations) Inspect(_ context.Context, f *pb.Fence, op string) (*pb.InspectResponse, error) {
	if op != "" {
		panic("connection read looked up mutation")
	}
	s.calls++
	f = proto.CloneOf(f)
	if s.wrong {
		f.AssignmentGeneration++
	}
	return &pb.InspectResponse{Fence: f, State: s.state, EnvdVersion: s.version}, nil
}
func TestRuntimeConnectionInfoChecksLiveIdentity(t *testing.T) {
	s, ops, network, ingress, launch := serviceFixture(t)
	reader := &inspectOperations{Operations: ops, version: "0.5.1", state: "RUNNING"}
	s.Ops = reader
	req := &pb.ReadRuntimeInfoRequest{ActorUid: launch.ActorUid, TargetWorkerPodUid: "pod", Execution: proto.CloneOf(launch.Execution)}
	got, err := s.ReadRuntimeInfo(t.Context(), req)
	if err != nil || got.EnvdVersion != "0.5.1" {
		t.Fatal("version unavailable", err)
	}
	reader.wrong = true
	if _, err = s.ReadRuntimeInfo(t.Context(), req); status.Code(err) != codes.FailedPrecondition {
		t.Fatal("different allocation accepted", err)
	}
	reader.wrong = false
	reader.state = "STOPPED"
	if _, err = s.ReadRuntimeInfo(t.Context(), req); status.Code(err) != codes.FailedPrecondition {
		t.Fatal("stopped runtime accepted", err)
	}
	calls := reader.calls
	req.Execution.Fence.WorkerEpoch++
	if _, err = s.ReadRuntimeInfo(t.Context(), req); status.Code(err) != codes.FailedPrecondition || reader.calls != calls {
		t.Fatal("stale request reached executor", err)
	}
	if len(ops.calls) != 0 || network.attached != 0 || network.detached != 0 || ingress.activated != 0 || ingress.deactivated != 0 {
		t.Fatal("runtime lookup mutated Actor")
	}
}

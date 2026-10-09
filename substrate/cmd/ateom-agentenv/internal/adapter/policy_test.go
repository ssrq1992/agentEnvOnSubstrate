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
	"errors"
	"github.com/agent-substrate/substrate/internal/aenvexecutor"
	pb "github.com/agent-substrate/substrate/internal/proto/aenvexecutorpb"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"testing"
)

func TestLivePolicyRequiresExactAcknowledgmentAndPreservesNetwork(t *testing.T) {
	s, ops, network, ingress, launch := serviceFixture(t)
	req := &pb.ApplyNetworkPolicyRequest{ActorUid: launch.ActorUid, TargetWorkerPodUid: "pod", Execution: proto.CloneOf(launch.Execution), Policy: &pb.NetworkPolicy{Revision: 3, Base: pb.NetworkPolicy_DENY, AllowOut: []string{"*.example.com"}}}
	req.Execution.OperationId = "policy-3"
	if _, err := s.ApplyNetworkPolicy(t.Context(), req); status.Code(err) != codes.Unavailable {
		t.Fatalf("missing ACK accepted: %v", err)
	}
	ops.appliedRevision = 3
	if response, err := s.ApplyNetworkPolicy(t.Context(), req); err != nil || response.AppliedRevision != 3 {
		t.Fatalf("application: %v %v", response, err)
	}
	if !proto.Equal(ops.calls[1].GetUpdatePolicy(), req.Policy) {
		t.Fatal("policy payload changed")
	}
	ops.err = &aenvexecutor.EffectUnknown{OperationID: "policy-3", Cause: errors.New("timeout")}
	if _, err := s.ApplyNetworkPolicy(t.Context(), req); status.Code(err) != codes.Unavailable {
		t.Fatal("unknown execution acknowledged")
	}
	calls := len(ops.calls)
	req.Execution.Fence.WorkerEpoch--
	if _, err := s.ApplyNetworkPolicy(t.Context(), req); status.Code(err) != codes.FailedPrecondition || len(ops.calls) != calls {
		t.Fatal("stale epoch reached executor")
	}
	if network.attached != 0 || network.detached != 0 || ingress.activated != 0 || ingress.deactivated != 0 {
		t.Fatal("policy mutation changed network ownership")
	}
}

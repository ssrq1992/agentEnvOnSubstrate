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

package aenvexecutor

import (
	pb "github.com/agent-substrate/substrate/internal/proto/aenvexecutorpb"
	"google.golang.org/protobuf/proto"
	"strings"
	"testing"
)

func TestValidatePolicyUpdate(t *testing.T) {
	uid := "01900000-0000-7000-8000-000000000001"
	valid := &pb.ApplyNetworkPolicyRequest{ActorUid: uid, TargetWorkerPodUid: "pod", Execution: &pb.LifecycleOperation{OperationId: "policy-7", Fence: &pb.Fence{ProtocolVersion: ProtocolVersion, ActorUid: uid, WorkerPodUid: "pod", WorkerEpoch: 2, WorkerInstanceId: "executor", AssignmentGeneration: 7}}, Policy: &pb.NetworkPolicy{Base: pb.NetworkPolicy_DENY, Revision: 7, AllowOut: []string{"*.example.com", "1.1.1.1/32"}, DenyOut: []string{"169.254.0.0/16"}}}
	if err := ValidatePolicyUpdate(valid); err != nil {
		t.Fatal(err)
	}
	if err := ValidatePolicyUpdate(nil); err == nil {
		t.Fatal("nil accepted")
	}
	for name, change := range map[string]func(*pb.ApplyNetworkPolicyRequest){
		"missing policy": func(r *pb.ApplyNetworkPolicyRequest) { r.Policy = nil },
		"zero revision":  func(r *pb.ApplyNetworkPolicyRequest) { r.Policy.Revision = 0 },
		"enum":           func(r *pb.ApplyNetworkPolicyRequest) { r.Policy.Base = 99 },
		"actor":          func(r *pb.ApplyNetworkPolicyRequest) { r.ActorUid = "another" },
		"worker":         func(r *pb.ApplyNetworkPolicyRequest) { r.TargetWorkerPodUid = "other" },
		"rules":          func(r *pb.ApplyNetworkPolicyRequest) { r.Policy.AllowOut = make([]string, 1025) },
		"long":           func(r *pb.ApplyNetworkPolicyRequest) { r.Policy.AllowOut = []string{strings.Repeat("a", 254)} },
		"empty":          func(r *pb.ApplyNetworkPolicyRequest) { r.Policy.AllowOut = []string{""} },
		"control":        func(r *pb.ApplyNetworkPolicyRequest) { r.Policy.DenyOut = []string{"1.1.1.1\n"} },
	} {
		t.Run(name, func(t *testing.T) {
			request := proto.CloneOf(valid)
			change(request)
			if err := ValidatePolicyUpdate(request); err == nil {
				t.Fatal("invalid update accepted")
			}
		})
	}
}

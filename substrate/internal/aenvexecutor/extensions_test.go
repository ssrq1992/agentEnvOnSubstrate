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

func TestValidateExtensionParams(t *testing.T) {
	for _, tc := range []struct {
		json  string
		valid bool
	}{{"{}", true}, {`{"key":{"nested":1}}`, true}, {"null", false}, {"[]", false}, {"1", false}, {"{", false}, {"", false}, {`{"x":"` + strings.Repeat("x", 65536) + `"}`, false}} {
		if (ValidateExtensionParams(&pb.ExtensionParams{Json: tc.json}) == nil) != tc.valid {
			t.Fatalf("validation %q", tc.json)
		}
	}
	if ValidateExtensionParams(nil) == nil {
		t.Fatal("nil accepted")
	}
}
func TestValidateExtensionUpdate(t *testing.T) {
	uid := "01900000-0000-7000-8000-000000000001"
	req := &pb.ApplyExtensionParamsRequest{ActorUid: uid, TargetWorkerPodUid: "pod", Execution: &pb.LifecycleOperation{OperationId: "extensions", Fence: &pb.Fence{ProtocolVersion: ProtocolVersion, ActorUid: uid, WorkerPodUid: "pod", WorkerEpoch: 2, WorkerInstanceId: "executor", AssignmentGeneration: 7}}, Patch: &pb.ExtensionParams{Json: "{}"}}
	if err := ValidateExtensionUpdate(req); err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*pb.ApplyExtensionParamsRequest){func(r *pb.ApplyExtensionParamsRequest) { r.Patch = nil }, func(r *pb.ApplyExtensionParamsRequest) { r.ActorUid = "other" }, func(r *pb.ApplyExtensionParamsRequest) { r.TargetWorkerPodUid = "other" }, func(r *pb.ApplyExtensionParamsRequest) { r.Execution.Fence.AssignmentGeneration = 0 }} {
		copy := proto.CloneOf(req)
		change(copy)
		if ValidateExtensionUpdate(copy) == nil {
			t.Fatal("invalid update accepted")
		}
	}
	if ValidateExtensionUpdate(nil) == nil {
		t.Fatal("nil accepted")
	}
}

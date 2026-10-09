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
	"github.com/agent-substrate/substrate/pkg/proto/ateapipb"
	"google.golang.org/protobuf/proto"
	"testing"
)

func TestExecutionIdentityUsesPersistedIntent(t *testing.T) {
	actor := &ateapipb.Actor{Metadata: &ateapipb.ResourceMetadata{Uid: "actor"}, Status: &ateapipb.ActorStatus{WorkerAssignment: &ateapipb.WorkerAssignment{WorkerPodUid: "pod", WorkerEpoch: 3, AssignmentGeneration: 4, ExecutorInstanceId: "executor"}}}
	first := executionIdentity(actor, "suspend", "snapshot-one")
	if !proto.Equal(first, executionIdentity(actor, "suspend", "snapshot-one")) {
		t.Fatal("retry changed identity")
	}
	if first.OperationId == executionIdentity(actor, "suspend", "snapshot-two").OperationId {
		t.Fatal("different capture intent reused operation")
	}
	actor.Status.WorkerAssignment.AssignmentGeneration++
	if first.OperationId == executionIdentity(actor, "suspend", "snapshot-one").OperationId {
		t.Fatal("new allocation reused operation")
	}
	actor.Status.WorkerAssignment.ExecutorInstanceId = ""
	if executionIdentity(actor, "activate", "") != nil {
		t.Fatal("original backend acquired AgentENV identity")
	}
}

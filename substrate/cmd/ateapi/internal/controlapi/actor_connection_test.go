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
	pb "github.com/agent-substrate/substrate/pkg/proto/ateapipb"
	"google.golang.org/protobuf/proto"
	"testing"
)

func TestConnectionAllocationFence(t *testing.T) {
	actor := &pb.Actor{Metadata: &pb.ResourceMetadata{Uid: "uid"}, Status: &pb.ActorStatus{WorkerAssignment: &pb.WorkerAssignment{WorkerPodUid: "pod", WorkerEpoch: 2, AssignmentGeneration: 7, ExecutorInstanceId: "executor"}}}
	worker := &pb.Worker{SandboxClass: "agentenv", WorkerPodUid: "pod", Epoch: 2, Status: &pb.WorkerStatus{ObservedEpoch: 2, RegisteredEpoch: 2, ExecutorInstanceId: "executor"}}
	claim := &pb.ActorAssignment{ActorUid: "uid", WorkerEpoch: 2, AssignmentGeneration: 7, ExecutorInstanceId: "executor"}
	if err := validateConnectionAssignment(actor, worker, claim); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"pod", "epoch", "observed", "registered", "instance", "claim-instance", "generation", "claim-actor", "class"} {
		t.Run(field, func(t *testing.T) {
			w := proto.CloneOf(worker)
			c := proto.CloneOf(claim)
			switch field {
			case "pod":
				w.WorkerPodUid = "different"
			case "epoch":
				w.Epoch++
			case "observed":
				w.Status.ObservedEpoch++
			case "registered":
				w.Status.RegisteredEpoch++
			case "instance":
				w.Status.ExecutorInstanceId = "different"
			case "claim-instance":
				c.ExecutorInstanceId = "different"
			case "generation":
				c.AssignmentGeneration++
			case "claim-actor":
				c.ActorUid = "other"
			case "class":
				w.SandboxClass = "microvm"
			}
			if err := validateConnectionAssignment(actor, w, c); err == nil {
				t.Fatal("stale connection accepted")
			}
		})
	}
}

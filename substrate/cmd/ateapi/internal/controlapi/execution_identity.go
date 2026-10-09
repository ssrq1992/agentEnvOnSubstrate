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
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strconv"

	"github.com/agent-substrate/substrate/internal/aenvexecutor"
	pb "github.com/agent-substrate/substrate/internal/proto/aenvexecutorpb"
	"github.com/agent-substrate/substrate/pkg/proto/ateapipb"
)

// The operation identity derives exclusively from persisted allocation and
// workflow intent. Retries do not depend on request timestamps or row versions.
func executionIdentity(actor *ateapipb.Actor, phase, snapshot string) *pb.LifecycleOperation {
	assignment := actor.GetStatus().GetWorkerAssignment()
	if assignment.GetExecutorInstanceId() == "" {
		return nil
	}
	fence := &pb.Fence{ProtocolVersion: aenvexecutor.ProtocolVersion, ActorUid: actor.GetMetadata().GetUid(), WorkerPodUid: assignment.GetWorkerPodUid(), WorkerEpoch: uint64(assignment.GetWorkerEpoch()), WorkerInstanceId: assignment.GetExecutorInstanceId(), AssignmentGeneration: assignment.GetAssignmentGeneration()}
	key, _ := json.Marshal([]string{fence.ActorUid, strconv.FormatUint(fence.AssignmentGeneration, 10), phase, snapshot})
	digest := sha256.Sum256(key)
	return &pb.LifecycleOperation{Fence: fence, OperationId: hex.EncodeToString(digest[:])}
}

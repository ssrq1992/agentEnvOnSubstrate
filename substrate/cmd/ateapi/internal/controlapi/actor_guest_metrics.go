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
	"context"
	"errors"
	"github.com/agent-substrate/substrate/cmd/ateapi/internal/apivalidation"
	"github.com/agent-substrate/substrate/cmd/ateapi/internal/store"
	"github.com/agent-substrate/substrate/internal/aenvexecutor"
	"github.com/agent-substrate/substrate/internal/apierror"
	private "github.com/agent-substrate/substrate/internal/proto/aenvexecutorpb"
	"github.com/agent-substrate/substrate/internal/proto/ateletpb"
	"github.com/agent-substrate/substrate/internal/resources"
	"github.com/agent-substrate/substrate/pkg/proto/ateapipb"
	"google.golang.org/protobuf/proto"
)

func (s *RPCService) GetActorGuestMetrics(ctx context.Context, req *ateapipb.GetActorGuestMetricsRequest) (*ateapipb.GetActorGuestMetricsResponse, error) {
	if errs := apivalidation.ValidateGetActorGuestMetricsRequest(ctx, req); len(errs) > 0 {
		return nil, resources.ToAPIError(errs)
	}
	return s.actorWorkflow.getActorGuestMetrics(ctx, resources.ActorRefFromObjectRef(req.Actor), req.Uid)
}
func (w *ActorWorkflow) getActorGuestMetrics(ctx context.Context, ref resources.ActorRef, uid string) (*ateapipb.GetActorGuestMetricsResponse, error) {
	ctx, lease, err := w.acquireActorLease(ctx, ref)
	if err != nil {
		return nil, err
	}
	defer lease.Close()
	actor, err := w.store.GetActor(ctx, ref)
	if errors.Is(err, store.ErrNotFound) {
		return nil, apierror.NotFound("Actor not found")
	}
	if err != nil {
		return nil, err
	}
	if actor.GetMetadata().GetUid() != uid {
		return nil, apierror.FailedPrecondition("Actor incarnation changed")
	}
	if actor.GetStatus().GetState() != ateapipb.ActorState_ACTOR_STATE_RUNNING {
		return nil, apierror.FailedPrecondition("Actor must be running")
	}
	execution := executionIdentity(actor, "guest-metrics", "")
	if execution == nil || actor.GetStatus().GetAssignedNode() == "" {
		return nil, apierror.FailedPrecondition("running AgentENV allocation required")
	}
	assignment := actor.GetStatus().GetWorkerAssignment()
	worker, err := w.store.GetWorker(ctx, assignment.GetWorker().GetName())
	if err != nil {
		return nil, apierror.Unavailable("Worker identity unavailable")
	}
	claim, err := w.store.GetWorkerAssignment(ctx, assignment.GetWorker().GetName(), uid)
	if err != nil {
		return nil, apierror.Unavailable("Worker assignment unavailable")
	}
	if err = validateConnectionAssignment(actor, worker, claim); err != nil {
		return nil, err
	}
	conn, err := w.dialer.DialForAteletOnNode(actor.GetStatus().GetAssignedNode())
	if err != nil {
		return nil, err
	}
	response, err := ateletpb.NewAteomHerderClient(conn).ReadGuestStats(ctx, &private.ReadGuestStatsRequest{ActorUid: uid, TargetWorkerPodUid: assignment.GetWorkerPodUid(), Execution: execution})
	if err != nil {
		return nil, err
	}
	if err := aenvexecutor.ValidateGuestStats(execution.Fence, response); err != nil {
		return nil, apierror.Unavailable("guest measurement allocation changed")
	}
	return &ateapipb.GetActorGuestMetricsResponse{ActorUid: uid, Assignment: proto.CloneOf(assignment), ObservedAtUnixMillis: response.ObservedAtUnixMillis, MemoryUsedBytes: response.MemoryUsedBytes, MemoryTotalBytes: response.MemoryTotalBytes, MemoryCacheBytes: response.MemoryCacheBytes, CpuUsedPercent: response.CpuUsedPercent, CpuCount: response.CpuCount, DiskUsedBytes: response.DiskUsedBytes, DiskTotalBytes: response.DiskTotalBytes}, nil
}

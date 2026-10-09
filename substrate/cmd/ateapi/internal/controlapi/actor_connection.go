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
	"github.com/agent-substrate/substrate/internal/apierror"
	"github.com/agent-substrate/substrate/internal/proto/ateletpb"
	"github.com/agent-substrate/substrate/internal/resources"
	"github.com/agent-substrate/substrate/pkg/proto/ateapipb"
)

func (s *RPCService) ConnectActor(ctx context.Context, req *ateapipb.ConnectActorRequest) (*ateapipb.ConnectActorResponse, error) {
	if errs := apivalidation.ValidateConnectActorRequest(ctx, req); len(errs) > 0 {
		return nil, resources.ToAPIError(errs)
	}
	return s.actorWorkflow.connectActor(ctx, resources.ActorRefFromObjectRef(req.Actor), req.Uid)
}
func (w *ActorWorkflow) connectActor(ctx context.Context, ref resources.ActorRef, uid string) (*ateapipb.ConnectActorResponse, error) {
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
	execution := executionIdentity(actor, "connect", "")
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
	response, err := ateletpb.NewAteomHerderClient(conn).ReadAgentENVConnection(ctx, &ateletpb.ReadAgentENVConnectionRequest{ActorUid: uid, TargetAteomUid: assignment.GetWorkerPodUid(), Execution: execution})
	if err != nil {
		return nil, err
	}
	if response.GetEnvdAccessToken() == "" || response.GetEnvdVersion() == "" {
		return nil, apierror.Unavailable("runtime credential unavailable")
	}
	return &ateapipb.ConnectActorResponse{Actor: actor, EnvdAccessToken: response.EnvdAccessToken, EnvdVersion: response.EnvdVersion, RootfsBytes: response.RootfsBytes}, nil
}
func validateConnectionAssignment(actor *ateapipb.Actor, worker *ateapipb.Worker, claim *ateapipb.ActorAssignment) error {
	a := actor.GetStatus().GetWorkerAssignment()
	if worker.GetSandboxClass() != "agentenv" || worker.GetWorkerPodUid() != a.GetWorkerPodUid() || worker.GetEpoch() != a.GetWorkerEpoch() || worker.GetEpoch() != worker.GetStatus().GetObservedEpoch() || worker.GetEpoch() != worker.GetStatus().GetRegisteredEpoch() || a.GetExecutorInstanceId() == "" || a.GetExecutorInstanceId() != worker.GetStatus().GetExecutorInstanceId() || a.GetExecutorInstanceId() != claim.GetExecutorInstanceId() || a.GetAssignmentGeneration() == 0 || a.GetAssignmentGeneration() != claim.GetAssignmentGeneration() || claim.GetWorkerEpoch() != a.GetWorkerEpoch() || claim.GetActorUid() != actor.GetMetadata().GetUid() {
		return apierror.FailedPrecondition("AgentENV assignment identity changed")
	}
	return nil
}

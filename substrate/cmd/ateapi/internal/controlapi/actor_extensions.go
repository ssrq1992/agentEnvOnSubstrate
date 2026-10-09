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
	"math"

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

func (s *RPCService) GetActorExtensionParams(ctx context.Context, req *ateapipb.GetActorExtensionParamsRequest) (*ateapipb.ActorExtensionParams, error) {
	if errs := apivalidation.ValidateGetActorExtensionParamsRequest(ctx, req); len(errs) > 0 {
		return nil, resources.ToAPIError(errs)
	}
	ctx, lease, err := s.actorWorkflow.acquireActorLease(ctx, resources.ActorRefFromObjectRef(req.Actor))
	if err != nil {
		return nil, err
	}
	defer lease.Close()
	actor, err := s.actorWorkflow.store.GetActor(ctx, resources.ActorRefFromObjectRef(req.Actor))
	if err != nil {
		return nil, err
	}
	if actor.GetMetadata().GetUid() != req.Uid {
		return nil, apierror.FailedPrecondition("Actor incarnation changed")
	}
	actor, err = s.actorWorkflow.ensureExtensionState(ctx, actor)
	if err != nil {
		return nil, err
	}
	if actor.Status.AgentenvExtensionDelivery.Pending != nil {
		return nil, apierror.Unavailable("extension update has not been confirmed")
	}
	return proto.CloneOf(actor.Status.AgentenvExtensionDelivery.Approved), nil
}

func (s *RPCService) UpdateActorExtensionParams(ctx context.Context, req *ateapipb.UpdateActorExtensionParamsRequest) (*ateapipb.ActorExtensionParams, error) {
	if errs := apivalidation.ValidateUpdateActorExtensionParamsRequest(ctx, req); len(errs) > 0 {
		return nil, resources.ToAPIError(errs)
	}
	if aenvexecutor.ValidateExtensionParams(&private.ExtensionParams{Json: req.PatchJson}) != nil {
		return nil, apierror.InvalidArgument("extension patch must be a JSON object within size limit")
	}
	w := s.actorWorkflow
	ctx, lease, err := w.acquireActorLease(ctx, resources.ActorRefFromObjectRef(req.Actor))
	if err != nil {
		return nil, err
	}
	defer lease.Close()
	actor, err := w.store.GetActor(ctx, resources.ActorRefFromObjectRef(req.Actor))
	if err != nil {
		return nil, err
	}
	if actor.GetMetadata().GetUid() != req.Uid {
		return nil, apierror.FailedPrecondition("Actor incarnation changed")
	}
	d := actor.GetStatus().GetAgentenvExtensionDelivery()
	if d.GetLastRequest().GetOperationId() == req.OperationId {
		if !proto.Equal(d.LastRequest, req) {
			return nil, apierror.Aborted("extension operation payload changed")
		}
		if d.LastRejected {
			return nil, apierror.FailedPrecondition("extension update was rejected without effect")
		}
		return proto.CloneOf(d.LastResult), nil
	}
	if d.GetPending() != nil {
		if !proto.Equal(d.Pending, req) {
			return nil, apierror.Aborted("another extension update is pending")
		}
	} else {
		if actor.GetStatus().GetState() != ateapipb.ActorState_ACTOR_STATE_RUNNING || actor.GetStatus().GetWorkerAssignment().GetAssignmentGeneration() != req.AssignmentGeneration {
			return nil, apierror.FailedPrecondition("running Actor allocation required")
		}
		actor, err = w.ensureExtensionState(ctx, actor)
		if err != nil {
			return nil, err
		}
		d = actor.Status.AgentenvExtensionDelivery
		if d.Approved.GetRevision() != req.ExpectedRevision || req.ExpectedRevision == math.MaxUint64 {
			return nil, apierror.Aborted("extension revision changed or exhausted")
		}
		actor, err = w.store.UpdateActor(ctx, resources.ActorRefFromActor(actor), store.PreconditionFrom(actor), func(a *ateapipb.Actor) error {
			a.Status.AgentenvExtensionDelivery.Pending = proto.CloneOf(req)
			a.Status.AgentenvExtensionDelivery.PendingAssignment = proto.CloneOf(actor.Status.WorkerAssignment)
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	actor, err = w.applyExtensionUpdate(ctx, actor)
	if err != nil {
		return nil, err
	}
	return proto.CloneOf(actor.Status.AgentenvExtensionDelivery.Approved), nil
}

// Initial state comes from the live runtime, including inherited snapshot parameters.
// Template launch parameters must never replace an extension's approved full object.
func (w *ActorWorkflow) ensureExtensionState(ctx context.Context, actor *ateapipb.Actor) (*ateapipb.Actor, error) {
	if actor.GetStatus().GetAgentenvExtensionDelivery().GetApproved() != nil {
		return actor, nil
	}
	if err := requireAgentENVPolicyActor(ctx, w, actor); err != nil {
		return nil, err
	}
	if actor.GetStatus().GetState() != ateapipb.ActorState_ACTOR_STATE_RUNNING {
		return nil, apierror.FailedPrecondition("extension state requires first observation of a running Actor")
	}
	execution := executionIdentity(actor, "extension-read", "")
	if execution == nil {
		return nil, apierror.FailedPrecondition("AgentENV allocation required")
	}
	if err := w.validateExtensionAssignment(ctx, actor); err != nil {
		return nil, err
	}
	conn, err := w.dialer.DialForAteletOnNode(actor.Status.AssignedNode)
	if err != nil {
		return nil, err
	}
	info, err := ateletpb.NewAteomHerderClient(conn).ReadRuntimeInfo(ctx, &private.ReadRuntimeInfoRequest{Execution: execution, ActorUid: actor.GetMetadata().GetUid(), TargetWorkerPodUid: actor.Status.WorkerAssignment.WorkerPodUid})
	if err != nil {
		return nil, err
	}
	if aenvexecutor.ValidateRuntimeInfo(execution.Fence, info) != nil || aenvexecutor.ValidateExtensionParams(info.GetCurrentExtensions()) != nil {
		return nil, apierror.Unavailable("runtime extension parameters unavailable")
	}
	return w.store.UpdateActor(ctx, resources.ActorRefFromActor(actor), store.PreconditionFrom(actor), func(a *ateapipb.Actor) error {
		a.Status.AgentenvExtensionDelivery = &ateapipb.AgentENVExtensionDelivery{Approved: &ateapipb.ActorExtensionParams{Json: info.CurrentExtensions.Json}}
		return nil
	})
}

func (w *ActorWorkflow) validateExtensionAssignment(ctx context.Context, actor *ateapipb.Actor) error {
	assignment := actor.GetStatus().GetWorkerAssignment()
	if assignment == nil || actor.GetStatus().GetAssignedNode() == "" {
		return apierror.FailedPrecondition("Actor allocation required")
	}
	worker, err := w.store.GetWorker(ctx, assignment.GetWorker().GetName())
	if err != nil {
		return err
	}
	claim, err := w.store.GetWorkerAssignment(ctx, assignment.GetWorker().GetName(), actor.GetMetadata().GetUid())
	if err != nil {
		return err
	}
	return validateConnectionAssignment(actor, worker, claim)
}

func (w *ActorWorkflow) applyExtensionUpdate(ctx context.Context, actor *ateapipb.Actor) (*ateapipb.Actor, error) {
	d := actor.GetStatus().GetAgentenvExtensionDelivery()
	if d.GetPending() == nil {
		return actor, nil
	}
	if actor.GetStatus().GetState() != ateapipb.ActorState_ACTOR_STATE_RUNNING || !proto.Equal(d.PendingAssignment, actor.GetStatus().GetWorkerAssignment()) {
		return nil, apierror.FailedPrecondition("extension update awaits original allocation")
	}
	if err := w.validateExtensionAssignment(ctx, actor); err != nil {
		return nil, err
	}
	execution := executionIdentity(actor, "extension-update", d.Pending.OperationId)
	if execution == nil {
		return nil, apierror.FailedPrecondition("AgentENV allocation required")
	}
	conn, err := w.dialer.DialForAteletOnNode(actor.Status.AssignedNode)
	if err != nil {
		return nil, err
	}
	result, err := ateletpb.NewAteomHerderClient(conn).ApplyExtensionParams(ctx, &private.ApplyExtensionParamsRequest{Execution: execution, ActorUid: actor.GetMetadata().GetUid(), TargetWorkerPodUid: d.PendingAssignment.WorkerPodUid, Patch: &private.ExtensionParams{Json: d.Pending.PatchJson}})
	if err != nil {
		return nil, apierror.Unavailable("extension execution result is unconfirmed")
	}
	rejected := result.GetEffect() == private.Effect_NO_EFFECT
	if !rejected && (result.GetEffect() != private.Effect_COMPLETED || aenvexecutor.ValidateExtensionParams(result.GetApproved()) != nil) {
		return nil, apierror.Unavailable("extension approval is unconfirmed")
	}
	updated, err := w.store.UpdateActor(ctx, resources.ActorRefFromActor(actor), store.PreconditionFrom(actor), func(a *ateapipb.Actor) error {
		state := a.Status.AgentenvExtensionDelivery
		state.LastRequest = proto.CloneOf(state.Pending)
		state.LastRejected = rejected
		if !rejected {
			state.Approved = &ateapipb.ActorExtensionParams{Revision: state.Pending.ExpectedRevision + 1, Json: result.Approved.Json}
		}
		state.LastResult = proto.CloneOf(state.Approved)
		state.Pending = nil
		state.PendingAssignment = nil
		return nil
	})
	if err != nil {
		return nil, err
	}
	if rejected {
		return nil, apierror.FailedPrecondition("extension update was rejected without effect")
	}
	return updated, nil
}

func requireConfirmedExtensions(actor *ateapipb.Actor) error {
	if actor.GetStatus().GetAgentenvExtensionDelivery().GetPending() != nil {
		return apierror.Unavailable("extension update must be confirmed before snapshot or pause")
	}
	return nil
}

func (w *ActorWorkflow) reconcileAgentENVExtensions(ctx context.Context, ref resources.ActorRef) error {
	ctx, lease, err := w.acquireActorLease(ctx, ref)
	if err != nil {
		return err
	}
	defer lease.Close()
	actor, err := w.store.GetActor(ctx, ref)
	if err != nil {
		return err
	}
	_, err = w.applyExtensionUpdate(ctx, actor)
	return err
}

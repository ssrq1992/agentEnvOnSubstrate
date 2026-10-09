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
	"math"
	"strconv"

	"github.com/agent-substrate/substrate/cmd/ateapi/internal/store"
	"github.com/agent-substrate/substrate/internal/apierror"
	pb "github.com/agent-substrate/substrate/internal/proto/aenvexecutorpb"
	"github.com/agent-substrate/substrate/internal/proto/ateletpb"
	"github.com/agent-substrate/substrate/internal/resources"
	"github.com/agent-substrate/substrate/pkg/proto/ateapipb"
	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/proto"
)

func policyMissing(err error) bool {
	return errors.Is(err, store.ErrNotFound) || apierror.Code(err) == codes.NotFound
}
func policyExists(err error) bool {
	return errors.Is(err, store.ErrAlreadyExists) || apierror.Code(err) == codes.AlreadyExists
}
func nativePolicy(delivery *ateapipb.AgentENVPolicyDelivery) *pb.NetworkPolicy {
	if delivery == nil {
		return nil
	}
	p := delivery.GetPolicy()
	return &pb.NetworkPolicy{Base: pb.NetworkPolicy_Base(p.GetBase()), AllowOut: append([]string(nil), p.GetAllowOut()...), DenyOut: append([]string(nil), p.GetDenyOut()...), Revision: delivery.Revision}
}
func requireAgentENVPolicyActor(ctx context.Context, w *ActorWorkflow, actor *ateapipb.Actor) error {
	template, err := resolveActorTemplate(ctx, w.store, actor)
	if err != nil {
		return err
	}
	if template.GetSandboxConfig().GetSandboxClass() != ateapipb.SandboxClass_SANDBOX_CLASS_AGENTENV {
		return apierror.FailedPrecondition("AgentENV policy requires an AgentENV Actor")
	}
	switch actor.GetStatus().GetState() {
	case ateapipb.ActorState_ACTOR_STATE_RUNNING, ateapipb.ActorState_ACTOR_STATE_SUSPENDED:
		return nil
	default:
		return apierror.FailedPrecondition("policy mutation requires a running or durably suspended Actor")
	}
}

// stagePolicy persists the exact desired revision before any executor call.
// It is a projection of the authoritative EgressPolicy, not a second CRUD store.
func (w *ActorWorkflow) stagePolicy(ctx context.Context, actor *ateapipb.Actor, policy *ateapipb.EgressPolicy, deleting bool) (*ateapipb.Actor, error) {
	old := actor.GetStatus().GetAgentenvPolicyDelivery()
	uid, version := "", int64(0)
	desired := &ateapipb.AgentENVNetworkPolicy{}
	if policy != nil {
		uid = policy.GetMetadata().GetUid()
		version = policy.GetMetadata().GetVersion()
		if !deleting {
			desired = proto.CloneOf(policy.Agentenv)
		}
	}
	if old != nil && old.PolicyUid == uid && old.PolicyVersion == version && old.Deleting == deleting && proto.Equal(old.Policy, desired) {
		return actor, nil
	}
	if old.GetRevision() == math.MaxUint64 {
		return nil, apierror.FailedPrecondition("policy revision exhausted")
	}
	delivery := &ateapipb.AgentENVPolicyDelivery{Revision: old.GetRevision() + 1, Policy: desired, PolicyUid: uid, PolicyVersion: version, Deleting: deleting}
	return w.store.UpdateActor(ctx, resources.ActorRefFromActor(actor), store.PreconditionFrom(actor), func(a *ateapipb.Actor) error { a.Status.AgentenvPolicyDelivery = delivery; return nil })
}
func policyAcknowledged(actor *ateapipb.Actor) bool {
	d := actor.GetStatus().GetAgentenvPolicyDelivery()
	return d != nil && d.Revision != 0 && d.AppliedRevision == d.Revision && proto.Equal(d.AppliedAssignment, actor.GetStatus().GetWorkerAssignment()) && actor.GetStatus().GetWorkerAssignment() != nil
}
func (w *ActorWorkflow) applyPolicy(ctx context.Context, actor *ateapipb.Actor) (*ateapipb.Actor, error) {
	d := actor.GetStatus().GetAgentenvPolicyDelivery()
	if d == nil || actor.GetStatus().GetState() == ateapipb.ActorState_ACTOR_STATE_SUSPENDED || policyAcknowledged(actor) {
		return actor, nil
	}
	if actor.GetStatus().GetState() != ateapipb.ActorState_ACTOR_STATE_RUNNING {
		return nil, apierror.FailedPrecondition("policy delivery waits for a stable Actor lifecycle")
	}
	assignment := actor.GetStatus().GetWorkerAssignment()
	worker, err := w.store.GetWorker(ctx, assignment.GetWorker().GetName())
	if err != nil {
		return nil, err
	}
	claim, err := w.store.GetWorkerAssignment(ctx, assignment.GetWorker().GetName(), actor.GetMetadata().GetUid())
	if err != nil {
		return nil, err
	}
	if err = validateConnectionAssignment(actor, worker, claim); err != nil {
		return nil, err
	}
	execution := executionIdentity(actor, "network-policy", strconv.FormatUint(d.Revision, 10))
	conn, err := w.dialer.DialForAteletOnNode(actor.GetStatus().GetAssignedNode())
	if err != nil {
		return nil, err
	}
	response, err := ateletpb.NewAteomHerderClient(conn).ApplyNetworkPolicy(ctx, &pb.ApplyNetworkPolicyRequest{Execution: execution, ActorUid: actor.GetMetadata().GetUid(), TargetWorkerPodUid: assignment.GetWorkerPodUid(), Policy: nativePolicy(d)})
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, apierror.Unavailable("policy application has not been confirmed")
	}
	if response.GetAppliedRevision() != d.Revision {
		return nil, apierror.Unavailable("policy revision was not acknowledged")
	}
	return w.store.UpdateActor(ctx, resources.ActorRefFromActor(actor), store.PreconditionFrom(actor), func(a *ateapipb.Actor) error {
		a.Status.AgentenvPolicyDelivery.AppliedRevision = d.Revision
		a.Status.AgentenvPolicyDelivery.AppliedAssignment = proto.CloneOf(assignment)
		return nil
	})
}

// syncPolicy is also called under the lifecycle lease before activation. This
// repairs the crash window between a policy row write and its delivery intent.
func (w *ActorWorkflow) syncPolicy(ctx context.Context, actor *ateapipb.Actor) (*ateapipb.Actor, error) {
	if actor.GetStatus().GetAgentenvPolicyDelivery().GetDeleting() {
		return actor, nil
	}
	policy, err := w.store.GetEgressPolicy(ctx, resources.ActorRefFromActor(actor))
	if policyMissing(err) {
		if actor.GetStatus().GetAgentenvPolicyDelivery() == nil {
			return actor, nil
		}
		return w.stagePolicy(ctx, actor, nil, false)
	}
	if err != nil {
		return nil, err
	}
	if policy.GetAgentenv() == nil {
		return actor, nil
	}
	return w.stagePolicy(ctx, actor, policy, false)
}
func (w *ActorWorkflow) mutateAgentENVPolicy(ctx context.Context, ref resources.ActorRef, input *ateapipb.EgressPolicy, create bool, conditions *ateapipb.AgentENVPolicyPreconditions) (*ateapipb.EgressPolicy, error) {
	ctx, lease, err := w.acquireActorLease(ctx, ref)
	if err != nil {
		return nil, err
	}
	defer lease.Close()
	actor, err := w.store.GetActor(ctx, ref)
	if err != nil {
		return nil, err
	}
	if err = checkAgentENVPolicyPreconditions(actor, conditions); err != nil {
		return nil, err
	}
	if err = requireAgentENVPolicyActor(ctx, w, actor); err != nil {
		return nil, err
	}
	if actor.GetStatus().GetAgentenvPolicyDelivery().GetDeleting() {
		return nil, apierror.Aborted("policy deletion is pending")
	}
	if conditions != nil && conditions.ExpectedPolicyRevision != nil {
		// Recover a policy row committed before its delivery intent before
		// comparing revisions, otherwise a crash could reopen an old revision.
		actor, err = w.syncPolicy(ctx, actor)
		if err != nil {
			return nil, err
		}
		currentRevision := actor.GetStatus().GetAgentenvPolicyDelivery().GetRevision()
		if conditions.GetExpectedPolicyRevision() > currentRevision {
			return nil, apierror.Aborted("expected policy revision is ahead of current state")
		}
		if currentRevision != conditions.GetExpectedPolicyRevision() {
			current, e := w.store.GetEgressPolicy(ctx, ref)
			if e != nil && !policyMissing(e) {
				return nil, e
			}
			if current == nil || !proto.Equal(current.GetAgentenv(), input.GetAgentenv()) {
				return nil, apierror.Aborted("policy revision changed")
			}
		}
	}
	var policy *ateapipb.EgressPolicy
	if create {
		policy, err = w.store.CreateEgressPolicy(ctx, ref, input)
		if policyExists(err) {
			policy, err = w.store.GetEgressPolicy(ctx, ref)
			if err == nil && !proto.Equal(policy.GetAgentenv(), input.GetAgentenv()) {
				return nil, apierror.AlreadyExists("different policy already exists")
			}
		}
	} else {
		policy, err = w.store.GetEgressPolicy(ctx, ref)
		if err == nil {
			pre := store.PreconditionFrom(input)
			if e := pre.Validate(); e != nil {
				return nil, apierror.InvalidArgument("policy UID and version required")
			}
			if policy.GetAgentenv() == nil {
				return nil, apierror.FailedPrecondition("policy backend cannot be changed")
			}
			if e := pre.Check(policy.GetMetadata()); e != nil {
				if pre.UID != policy.GetMetadata().GetUid() || pre.Version > policy.GetMetadata().GetVersion() || !proto.Equal(input.Agentenv, policy.Agentenv) {
					return nil, apierror.Aborted("policy precondition changed")
				}
			} else if !proto.Equal(input.Agentenv, policy.Agentenv) {
				policy, err = w.store.UpdateEgressPolicy(ctx, ref, pre, func(p *ateapipb.EgressPolicy) error {
					p.Agentenv = proto.CloneOf(input.Agentenv)
					p.AgentenvDelivery = nil
					return nil
				})
			}
		}
	}
	if err != nil {
		return nil, err
	}
	actor, err = w.stagePolicy(ctx, actor, policy, false)
	if err != nil {
		return nil, err
	}
	actor, err = w.applyPolicy(ctx, actor)
	if err != nil {
		return nil, err
	}
	policy = proto.CloneOf(policy)
	policy.AgentenvDelivery = proto.CloneOf(actor.Status.AgentenvPolicyDelivery)
	return policy, nil
}
func (w *ActorWorkflow) finishPolicyDelete(ctx context.Context, actor *ateapipb.Actor) (*ateapipb.Actor, error) {
	d := actor.GetStatus().GetAgentenvPolicyDelivery()
	if d == nil || !d.Deleting {
		return actor, nil
	}
	_, err := w.store.DeleteEgressPolicy(ctx, resources.ActorRefFromActor(actor), store.DeletePreconditions{UID: d.PolicyUid, Version: d.PolicyVersion})
	if err != nil && !policyMissing(err) {
		return nil, err
	}
	return w.store.UpdateActor(ctx, resources.ActorRefFromActor(actor), store.PreconditionFrom(actor), func(a *ateapipb.Actor) error {
		delivery := a.Status.AgentenvPolicyDelivery
		delivery.Deleting = false
		delivery.PolicyUid = ""
		delivery.PolicyVersion = 0
		return nil
	})
}
func (w *ActorWorkflow) deleteAgentENVPolicy(ctx context.Context, ref resources.ActorRef, pre store.DeletePreconditions, conditions *ateapipb.AgentENVPolicyPreconditions) (*ateapipb.EgressPolicy, error) {
	ctx, lease, err := w.acquireActorLease(ctx, ref)
	if err != nil {
		return nil, err
	}
	defer lease.Close()
	actor, err := w.store.GetActor(ctx, ref)
	if err != nil {
		return nil, err
	}
	if err = checkAgentENVPolicyPreconditions(actor, conditions); err != nil {
		return nil, err
	}
	if err = requireAgentENVPolicyActor(ctx, w, actor); err != nil {
		return nil, err
	}
	policy, err := w.store.GetEgressPolicy(ctx, ref)
	if err != nil {
		return nil, err
	}
	if policy.Agentenv == nil {
		return nil, apierror.FailedPrecondition("policy backend changed")
	}
	if err = pre.Check(policy.GetMetadata()); err != nil {
		return nil, apierror.Aborted("policy precondition changed")
	}
	actor, err = w.stagePolicy(ctx, actor, policy, true)
	if err != nil {
		return nil, err
	}
	actor, err = w.applyPolicy(ctx, actor)
	if err != nil {
		return nil, err
	}
	actor, err = w.finishPolicyDelete(ctx, actor)
	if err != nil {
		return nil, err
	}
	policy = proto.CloneOf(policy)
	policy.AgentenvDelivery = proto.CloneOf(actor.Status.AgentenvPolicyDelivery)
	return policy, nil
}

func checkAgentENVPolicyPreconditions(actor *ateapipb.Actor, pre *ateapipb.AgentENVPolicyPreconditions) error {
	if pre.GetActorUid() != "" && pre.GetActorUid() != actor.GetMetadata().GetUid() {
		return apierror.FailedPrecondition("Actor incarnation changed")
	}
	if pre.GetRequireRunning() && actor.GetStatus().GetState() != ateapipb.ActorState_ACTOR_STATE_RUNNING {
		return apierror.FailedPrecondition("live network update requires a running Actor")
	}
	return nil
}

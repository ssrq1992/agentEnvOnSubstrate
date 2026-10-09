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
	"fmt"

	"github.com/agent-substrate/substrate/cmd/ateapi/internal/apivalidation"
	"github.com/agent-substrate/substrate/cmd/ateapi/internal/defaults"
	"github.com/agent-substrate/substrate/cmd/ateapi/internal/store"
	"github.com/agent-substrate/substrate/internal/apierror"
	"github.com/agent-substrate/substrate/internal/resources"
	"github.com/agent-substrate/substrate/pkg/proto/ateapipb"
	"google.golang.org/protobuf/proto"
	"k8s.io/apimachinery/pkg/util/validation/field"
)

func (s *RPCService) CreateActorEgressPolicy(ctx context.Context, req *ateapipb.CreateActorEgressPolicyRequest) (*ateapipb.EgressPolicy, error) {
	// First scrub any fields that users are not allowed to set, then fill the
	// defaults so validation sees the final resource state.
	policy := req.GetEgressPolicy()
	if policy != nil {
		policy.AgentenvDelivery = nil
		scrubResourceMetadataForCreate(policy.Metadata)
		defaults.Apply(policy)
	}
	if errs := apivalidation.ValidateCreateActorEgressPolicyRequest(ctx, req); len(errs) > 0 {
		return nil, resources.ToAPIError(errs)
	}
	actorRef := resources.ActorRefFromObjectRef(req.GetActor())
	if policy.GetAgentenv() != nil {
		return s.actorWorkflow.mutateAgentENVPolicy(ctx, actorRef, policy, true, req.AgentenvPreconditions)
	}
	if req.AgentenvPreconditions != nil {
		return nil, apierror.InvalidArgument("AgentENV preconditions require a native policy")
	}
	return s.impl.CreateEgressPolicy(ctx, actorRef, policy)
}

func (s *ServiceImpl) CreateEgressPolicy(ctx context.Context, actorRef resources.ActorRef, policy *ateapipb.EgressPolicy) (*ateapipb.EgressPolicy, error) {
	actor, err := s.store.GetActor(ctx, actorRef)
	if errors.Is(err, store.ErrNotFound) {
		return nil, apierror.FailedPrecondition("parent Actor does not exist")
	}
	if err != nil {
		return nil, err
	}
	if actor.GetActorTemplate() != nil {
		template, err := resolveActorTemplate(ctx, s.store, actor)
		if err != nil {
			return nil, err
		}
		native := template.GetSandboxConfig().GetSandboxClass() == ateapipb.SandboxClass_SANDBOX_CLASS_AGENTENV
		if native != (policy.GetAgentenv() != nil) {
			return nil, apierror.FailedPrecondition("egress policy backend does not match Actor")
		}
	}
	created, err := s.store.CreateEgressPolicy(ctx, actorRef, policy)
	return mapEgressPolicyWrite(created, err)
}

func (s *RPCService) GetActorEgressPolicy(ctx context.Context, req *ateapipb.GetActorEgressPolicyRequest) (*ateapipb.EgressPolicy, error) {
	if errs := apivalidation.ValidateGetActorEgressPolicyRequest(ctx, req); len(errs) > 0 {
		return nil, resources.ToAPIError(errs)
	}

	ref := resources.ActorRefFromObjectRef(req.GetActor())
	policy, err := s.impl.GetEgressPolicy(ctx, ref)
	if err != nil || policy.GetAgentenv() == nil {
		return policy, err
	}
	actor, err := s.impl.GetActor(ctx, ref)
	if err != nil {
		return nil, err
	}
	policy = proto.CloneOf(policy)
	delivery := actor.GetStatus().GetAgentenvPolicyDelivery()
	if delivery.GetPolicyUid() == policy.GetMetadata().GetUid() && delivery.GetPolicyVersion() == policy.GetMetadata().GetVersion() {
		policy.AgentenvDelivery = proto.CloneOf(delivery)
	} else {
		policy.AgentenvDelivery = nil
	}
	return policy, nil
}

func (s *ServiceImpl) GetEgressPolicy(ctx context.Context, actorRef resources.ActorRef) (*ateapipb.EgressPolicy, error) {
	policy, err := s.store.GetEgressPolicy(ctx, actorRef)
	if errors.Is(err, store.ErrNotFound) {
		return nil, apierror.NotFound("EgressPolicy for actor %s not found", actorRef)
	}
	if err != nil {
		return nil, fmt.Errorf("while getting Actor egress policy: %w", err)
	}
	return policy, nil
}

func (s *RPCService) UpdateActorEgressPolicy(ctx context.Context, req *ateapipb.UpdateActorEgressPolicyRequest) (*ateapipb.EgressPolicy, error) {
	policy := req.GetEgressPolicy()
	if policy != nil {
		policy.AgentenvDelivery = nil
		scrubResourceMetadataForUpdate(policy.Metadata)
	}
	if errs := apivalidation.ValidateUpdateActorEgressPolicyRequest(ctx, req); len(errs) > 0 {
		return nil, resources.ToAPIError(errs)
	}
	actorRef := resources.ActorRefFromObjectRef(req.GetActor())
	if policy.GetAgentenv() != nil {
		return s.actorWorkflow.mutateAgentENVPolicy(ctx, actorRef, policy, false, req.AgentenvPreconditions)
	}
	if req.AgentenvPreconditions != nil {
		return nil, apierror.InvalidArgument("AgentENV preconditions require a native policy")
	}
	return s.impl.UpdateEgressPolicy(ctx, actorRef, store.PreconditionFrom(policy), func(toUpdate *ateapipb.EgressPolicy) error {
		metadata := toUpdate.GetMetadata()
		proto.Reset(toUpdate)
		proto.Merge(toUpdate, policy)
		toUpdate.Metadata = metadata
		defaults.Apply(toUpdate)
		return nil
	})
}

func (s *ServiceImpl) UpdateEgressPolicy(ctx context.Context, actorRef resources.ActorRef, precondition store.Precondition, mutate func(*ateapipb.EgressPolicy) error) (*ateapipb.EgressPolicy, error) {
	updated, err := s.store.UpdateEgressPolicy(ctx, actorRef, precondition, func(toUpdate *ateapipb.EgressPolicy) error {
		oldVal := proto.Clone(toUpdate).(*ateapipb.EgressPolicy)
		if err := mutate(toUpdate); err != nil {
			return err
		}
		if errs := apivalidation.ValidateEgressPolicyUpdate(ctx, field.NewPath("egress_policy"), toUpdate, oldVal); len(errs) > 0 {
			return resources.ToAPIError(errs)
		}
		// EgressPolicy has no status or other server-derived fields to verify.
		return nil
	})
	return mapEgressPolicyWrite(updated, err)
}

func (s *RPCService) DeleteActorEgressPolicy(ctx context.Context, req *ateapipb.DeleteActorEgressPolicyRequest) (*ateapipb.EgressPolicy, error) {
	if req.GetAgentenvPreconditions() != nil && req.GetAgentenvPreconditions().ExpectedPolicyRevision != nil {
		return nil, apierror.InvalidArgument("expected policy revision applies to create/update only")
	}
	if errs := apivalidation.ValidateDeleteActorEgressPolicyRequest(ctx, req); len(errs) > 0 {
		return nil, resources.ToAPIError(errs)
	}

	ref := resources.ActorRefFromObjectRef(req.GetActor())
	policy, err := s.impl.GetEgressPolicy(ctx, ref)
	if err != nil {
		return nil, err
	}
	if policy.GetAgentenv() != nil {
		return s.actorWorkflow.deleteAgentENVPolicy(ctx, ref, toDeletePreconditions(req.GetOptions()), req.AgentenvPreconditions)
	}
	if req.AgentenvPreconditions != nil {
		return nil, apierror.InvalidArgument("AgentENV preconditions require a native policy")
	}
	return s.impl.DeleteEgressPolicy(ctx, ref, toDeletePreconditions(req.GetOptions()))
}

func (s *ServiceImpl) DeleteEgressPolicy(ctx context.Context, actorRef resources.ActorRef, precondition store.DeletePreconditions) (*ateapipb.EgressPolicy, error) {
	deleted, err := s.store.DeleteEgressPolicy(ctx, actorRef, precondition)
	return mapEgressPolicyWrite(deleted, err)
}

func mapEgressPolicyWrite(policy *ateapipb.EgressPolicy, err error) (*ateapipb.EgressPolicy, error) {
	switch {
	case err == nil:
		return policy, nil
	case errors.Is(err, store.ErrNotFound):
		return nil, apierror.NotFound("EgressPolicy not found")
	case errors.Is(err, store.ErrAlreadyExists):
		return nil, apierror.AlreadyExists("EgressPolicy already exists")
	case errors.Is(err, store.ErrVersionConflict):
		return nil, apierror.Aborted("EgressPolicy version conflict")
	case errors.Is(err, store.ErrUIDConflict):
		return nil, apierror.Aborted("EgressPolicy UID conflict")
	case errors.Is(err, store.ErrPreconditionRequired):
		return nil, apierror.InvalidArgument("EgressPolicy UID and version are required")
	case errors.Is(err, store.ErrFailedPrecondition):
		return nil, apierror.FailedPrecondition("parent Actor does not exist")
	default:
		return nil, fmt.Errorf("while writing EgressPolicy: %w", err)
	}
}

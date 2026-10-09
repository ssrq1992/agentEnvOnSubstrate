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

package main

import (
	"context"
	"github.com/agent-substrate/substrate/internal/aenvexecutor"
	pb "github.com/agent-substrate/substrate/internal/proto/aenvexecutorpb"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func (s *AteomHerder) ApplyExtensionParams(ctx context.Context, req *pb.ApplyExtensionParamsRequest) (*pb.ApplyExtensionParamsResponse, error) {
	if err := aenvexecutor.ValidateExtensionUpdate(req); err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	if !agentENVLocks.Lock(ctx, req.ActorUid) {
		return nil, ctx.Err()
	}
	defer agentENVLocks.Unlock(req.ActorUid)
	if err := validateAgentENVExisting(req.ActorUid, req.Execution); err != nil {
		return nil, status.Error(codes.FailedPrecondition, "extension allocation differs from preparation")
	}
	client, err := s.dialAteom(ctx, req.TargetWorkerPodUid)
	if err != nil {
		return nil, err
	}
	response, err := client.ApplyExtensionParams(ctx, req)
	if err != nil {
		return nil, err
	}
	if response.GetEffect() == pb.Effect_NO_EFFECT {
		return response, nil
	}
	if response.GetEffect() != pb.Effect_COMPLETED || aenvexecutor.ValidateExtensionParams(response.GetApproved()) != nil {
		return nil, status.Error(codes.Unavailable, "extension approval was not confirmed")
	}
	return response, nil
}

func (s *AteomHerder) ReadRuntimeInfo(ctx context.Context, req *pb.ReadRuntimeInfoRequest) (*pb.InspectResponse, error) {
	if err := aenvexecutor.ValidateOperation(req.GetExecution(), req.GetActorUid(), req.GetTargetWorkerPodUid()); err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	if !agentENVLocks.Lock(ctx, req.ActorUid) {
		return nil, ctx.Err()
	}
	defer agentENVLocks.Unlock(req.ActorUid)
	if err := validateAgentENVExisting(req.ActorUid, req.Execution); err != nil {
		return nil, status.Error(codes.FailedPrecondition, "runtime allocation changed")
	}
	client, err := s.dialAteom(ctx, req.TargetWorkerPodUid)
	if err != nil {
		return nil, err
	}
	info, err := client.ReadRuntimeInfo(ctx, req)
	if err != nil {
		return nil, err
	}
	if aenvexecutor.ValidateRuntimeInfo(req.Execution.Fence, info) != nil || aenvexecutor.ValidateExtensionParams(info.GetCurrentExtensions()) != nil {
		return nil, status.Error(codes.Unavailable, "runtime extension parameters unavailable")
	}
	return info, nil
}

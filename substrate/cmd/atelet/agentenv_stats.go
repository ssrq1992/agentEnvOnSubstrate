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

func (s *AteomHerder) ReadGuestStats(ctx context.Context, req *pb.ReadGuestStatsRequest) (*pb.StatsResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "stats request required")
	}
	if err := aenvexecutor.ValidateOperation(req.Execution, req.ActorUid, req.TargetWorkerPodUid); err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	if !agentENVLocks.Lock(ctx, req.ActorUid) {
		return nil, ctx.Err()
	}
	defer agentENVLocks.Unlock(req.ActorUid)
	if err := validateAgentENVExisting(req.ActorUid, req.Execution); err != nil {
		return nil, status.Error(codes.FailedPrecondition, "metrics allocation differs from preparation")
	}
	client, err := s.dialAteom(ctx, req.TargetWorkerPodUid)
	if err != nil {
		return nil, err
	}
	response, err := client.ReadGuestStats(ctx, req)
	if err != nil {
		return nil, err
	}
	if err := aenvexecutor.ValidateGuestStats(req.Execution.Fence, response); err != nil {
		return nil, status.Error(codes.FailedPrecondition, "metrics assignment changed")
	}
	return response, nil
}

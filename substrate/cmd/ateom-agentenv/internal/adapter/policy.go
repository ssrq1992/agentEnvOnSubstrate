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

package adapter

import (
	"context"
	"github.com/agent-substrate/substrate/internal/aenvexecutor"
	pb "github.com/agent-substrate/substrate/internal/proto/aenvexecutorpb"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

func (s *Service) ApplyNetworkPolicy(ctx context.Context, req *pb.ApplyNetworkPolicyRequest) (*pb.ApplyNetworkPolicyResponse, error) {
	if err := aenvexecutor.ValidatePolicyUpdate(req); err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	unlock, err := s.lock(ctx, req.Execution, req.ActorUid)
	if err != nil {
		return nil, err
	}
	defer unlock()
	// Ops persists the digest and uses the executor's revision check. Neither
	// network reattachment nor ingress teardown is part of a policy update.
	response, err := s.Ops.Execute(ctx, req.Execution.Fence, req.Execution.OperationId, &pb.Command{Action: &pb.Command_UpdatePolicy{UpdatePolicy: proto.CloneOf(req.Policy)}})
	if err != nil {
		return nil, executeError(err)
	}
	if response.GetEffect() != pb.Effect_COMPLETED || response.GetAppliedPolicyRevision() != req.Policy.Revision {
		return nil, status.Error(codes.Unavailable, "policy application revision was not confirmed")
	}
	return &pb.ApplyNetworkPolicyResponse{AppliedRevision: response.AppliedPolicyRevision}, nil
}

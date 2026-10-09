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
)

func (s *Service) ReadRuntimeInfo(ctx context.Context, req *pb.ReadRuntimeInfoRequest) (*pb.InspectResponse, error) {
	if req == nil || req.TargetWorkerPodUid != s.PodUID {
		return nil, status.Error(codes.InvalidArgument, "Worker routing identity mismatch")
	}
	unlock, err := s.lock(ctx, req.Execution, req.ActorUid)
	if err != nil {
		return nil, err
	}
	defer unlock()
	reader, ok := s.Ops.(interface {
		Inspect(context.Context, *pb.Fence, string) (*pb.InspectResponse, error)
	})
	if !ok {
		return nil, status.Error(codes.Unavailable, "runtime reader unavailable")
	}
	info, err := reader.Inspect(ctx, req.Execution.Fence, "")
	if err != nil {
		return nil, err
	}
	if err = aenvexecutor.ValidateRuntimeInfo(req.Execution.Fence, info); err != nil {
		return nil, status.Error(codes.FailedPrecondition, "live runtime information unavailable")
	}
	return info, nil
}

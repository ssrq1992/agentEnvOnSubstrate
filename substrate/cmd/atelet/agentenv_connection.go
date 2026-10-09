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
	"encoding/hex"
	"encoding/json"
	"github.com/agent-substrate/substrate/internal/aenvexecutor"
	pb "github.com/agent-substrate/substrate/internal/proto/aenvexecutorpb"
	"github.com/agent-substrate/substrate/internal/proto/ateletpb"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"os"
	"path/filepath"
)

// ReadAgentENVConnection is exposed only on the existing control-plane mTLS
// service. The control plane holds the Actor lease and checks its live claim.
func (s *AteomHerder) ReadAgentENVConnection(ctx context.Context, req *ateletpb.ReadAgentENVConnectionRequest) (*ateletpb.ReadAgentENVConnectionResponse, error) {
	if err := aenvexecutor.ValidateOperation(req.GetExecution(), req.GetActorUid(), req.GetTargetAteomUid()); err != nil {
		return nil, status.Error(codes.InvalidArgument, "invalid execution identity")
	}
	if !agentENVLocks.Lock(ctx, req.ActorUid) {
		return nil, ctx.Err()
	}
	defer agentENVLocks.Unlock(req.ActorUid)
	path := filepath.Join(agentENVPreparationsDir, req.ActorUid, "agentenv-preparation.json")
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || info.Size() > 1<<20 {
		return nil, status.Error(codes.FailedPrecondition, "private runtime preparation unavailable")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, status.Error(codes.Unavailable, "runtime preparation unreadable")
	}
	var prepared agentENVPreparation
	if err = json.Unmarshal(data, &prepared); err != nil {
		return nil, status.Error(codes.FailedPrecondition, "invalid runtime preparation")
	}
	if !proto.Equal(prepared.Execution.GetFence(), req.Execution.GetFence()) {
		return nil, status.Error(codes.FailedPrecondition, "runtime allocation changed")
	}
	token := prepared.Launch.GetEnvdAccessToken()
	decoded, err := hex.DecodeString(token)
	if err != nil || len(decoded) != 32 || hex.EncodeToString(decoded) != token {
		return nil, status.Error(codes.FailedPrecondition, "invalid runtime credential")
	}
	client, err := s.dialAteom(ctx, req.TargetAteomUid)
	if err != nil {
		return nil, err
	}
	runtimeInfo, err := client.ReadRuntimeInfo(ctx, &pb.ReadRuntimeInfoRequest{ActorUid: req.ActorUid, TargetWorkerPodUid: req.TargetAteomUid, Execution: proto.CloneOf(req.Execution)})
	if err != nil {
		return nil, err
	}
	if err = aenvexecutor.ValidateRuntimeInfo(req.Execution.Fence, runtimeInfo); err != nil {
		return nil, status.Error(codes.FailedPrecondition, "runtime connection metadata unavailable")
	}
	return &ateletpb.ReadAgentENVConnectionResponse{EnvdAccessToken: token, EnvdVersion: runtimeInfo.EnvdVersion, RootfsBytes: runtimeInfo.RootfsBytes}, nil
}

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
	"github.com/agent-substrate/substrate/internal/proto/ateompb"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"time"
)

func (s *Service) hosted(uid string) *activeActor {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.active[uid]
}
func (s *Service) pendingStats(a *activeActor) *ateompb.WorkloadStatsSample {
	attr := a.Attribution
	return &ateompb.WorkloadStatsSample{Atespace: attr.Ref.Atespace, ActorName: attr.Ref.Name, ActorUid: attr.UID, ActorTemplateAtespace: attr.TemplateAtespace, ActorTemplateName: attr.TemplateName, SandboxClass: ateompb.SandboxClass_SANDBOX_CLASS_AGENTENV, ObservedAtUnixNano: time.Now().UnixNano(), EpochUnixNano: a.Epoch}
}
func (s *Service) hostStats(a *activeActor) (*ateompb.WorkloadStatsSample, error) {
	sample, err := s.Cgroups.Read(&pb.Fence{ProtocolVersion: aenvexecutor.ProtocolVersion, ActorUid: a.Attribution.UID, WorkerPodUid: s.PodUID, WorkerEpoch: s.Epoch, WorkerInstanceId: s.InstanceID, AssignmentGeneration: a.Generation})
	if err != nil {
		return nil, err
	}
	result := s.pendingStats(a)
	result.Source = ateompb.StatsSource_STATS_SOURCE_CGROUP
	result.MemoryCurrentBytes = sample.MemoryCurrentBytes
	result.MemoryPeakBytes = sample.MemoryPeakBytes
	result.MemoryWorkingSetBytes = sample.MemoryWorkingSetBytes
	result.CpuUsageUsec = sample.CPUUsageUsec
	return result, nil
}
func (s *Service) GetWorkloadStats(ctx context.Context, r *ateompb.GetWorkloadStatsRequest) (*ateompb.GetWorkloadStatsResponse, error) {
	if r.GetActorUid() == "" {
		return nil, status.Error(codes.InvalidArgument, "Actor UID required")
	}
	a := s.hosted(r.ActorUid)
	if a == nil {
		return nil, status.Error(codes.NotFound, "Actor not active on Worker")
	}
	sample, err := s.hostStats(a)
	if err != nil {
		return nil, status.Error(codes.FailedPrecondition, "Actor host resource sample unavailable")
	}
	if s.hosted(r.ActorUid) != a {
		return nil, status.Error(codes.NotFound, "Actor activation changed while reading resources")
	}
	return &ateompb.GetWorkloadStatsResponse{Sample: sample}, nil
}
func (s *Service) GetActiveWorkloadStats(ctx context.Context, r *ateompb.GetActiveWorkloadStatsRequest) (*ateompb.GetActiveWorkloadStatsResponse, error) {
	s.mu.Lock()
	actors := make([]*activeActor, 0, len(s.active))
	for _, a := range s.active {
		actors = append(actors, a)
	}
	s.mu.Unlock()
	result := &ateompb.GetActiveWorkloadStatsResponse{}
	for _, a := range actors {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		sample, err := s.hostStats(a)
		if err != nil {
			sample = s.pendingStats(a)
		}
		current := s.hosted(a.Attribution.UID)
		if current == nil {
			continue
		}
		if current != a {
			sample = s.pendingStats(current)
		}
		result.Samples = append(result.Samples, sample)
	}
	return result, nil
}

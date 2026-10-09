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
	"errors"
	"github.com/agent-substrate/substrate/internal/cgroupstats"
	pb "github.com/agent-substrate/substrate/internal/proto/aenvexecutorpb"
	"github.com/agent-substrate/substrate/internal/proto/ateompb"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"testing"
	"time"
)

type samplingLimits struct {
	*limits
	read func(*pb.Fence) (cgroupstats.Sample, error)
}

func (l *samplingLimits) Read(f *pb.Fence) (cgroupstats.Sample, error) { return l.read(f) }
func TestHostStatsUsesCgroupAndStableActivationEpoch(t *testing.T) {
	s, _, _, _, req := serviceFixture(t)
	req.Atespace = "space"
	req.ActorName = "actor"
	if _, err := s.RunWorkload(t.Context(), req); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	if !s.locks.Lock(ctx, req.ActorUid) {
		t.Fatal("test lifecycle lock")
	}
	defer s.locks.Unlock(req.ActorUid)
	got, err := s.GetWorkloadStats(ctx, &ateompb.GetWorkloadStatsRequest{ActorUid: req.ActorUid})
	if err != nil {
		t.Fatal("host stats blocked on lifecycle", err)
	}
	sample := got.Sample
	if sample.Source != ateompb.StatsSource_STATS_SOURCE_CGROUP || sample.SandboxClass != ateompb.SandboxClass_SANDBOX_CLASS_AGENTENV || sample.ActorUid != req.ActorUid || sample.ActorName != "actor" || sample.MemoryCurrentBytes != 100 || sample.MemoryWorkingSetBytes != 70 || sample.CpuUsageUsec != 123 || sample.EpochUnixNano <= 0 {
		t.Fatal(sample)
	}
	before := s.hosted(req.ActorUid)
	// Re-activation retries keep the original accounting epoch.
	req.AgentenvLaunch.Network = &pb.NetworkAttachment{InteractionIpv4: "172.30.0.2"}
	if err := s.activate(before.Attribution, before.Generation, req.AgentenvLaunch); err != nil {
		t.Fatal(err)
	}
	if s.hosted(req.ActorUid) != before {
		t.Fatal("idempotent activation reset statistics epoch")
	}
	if _, err := s.GetWorkloadStats(ctx, &ateompb.GetWorkloadStatsRequest{ActorUid: "other"}); status.Code(err) != codes.NotFound {
		t.Fatal("other actor attributed", err)
	}
}
func TestHostStatsActivationRaceReturnsPendingOrNotFound(t *testing.T) {
	s, _, _, _, req := serviceFixture(t)
	if _, err := s.RunWorkload(t.Context(), req); err != nil {
		t.Fatal(err)
	}
	l := &samplingLimits{limits: s.Cgroups.(*limits)}
	s.Cgroups = l
	l.read = func(*pb.Fence) (cgroupstats.Sample, error) {
		s.mu.Lock()
		delete(s.active, req.ActorUid)
		s.mu.Unlock()
		return cgroupstats.Sample{MemoryCurrentBytes: 999}, nil
	}
	if _, err := s.GetWorkloadStats(t.Context(), &ateompb.GetWorkloadStatsRequest{ActorUid: req.ActorUid}); status.Code(err) != codes.NotFound {
		t.Fatal("late measurement retained attribution", err)
	}
	if _, err := s.RunWorkload(t.Context(), req); err != nil {
		t.Fatal(err)
	}
	l.read = func(*pb.Fence) (cgroupstats.Sample, error) { return cgroupstats.Sample{}, errors.New("no accounting") }
	got, err := s.GetActiveWorkloadStats(t.Context(), &ateompb.GetActiveWorkloadStatsRequest{})
	if err != nil || len(got.GetSamples()) != 1 || got.Samples[0].Source != ateompb.StatsSource_STATS_SOURCE_UNSPECIFIED || got.Samples[0].MemoryCurrentBytes != 0 {
		t.Fatal("unknown measurement invented", got, err)
	}
}

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

package network

import (
	"context"
	"errors"
	pb "github.com/agent-substrate/substrate/internal/proto/aenvexecutorpb"
	"google.golang.org/protobuf/proto"
	"net/netip"
	"path/filepath"
	"testing"
)

func fixture(t *testing.T, run Runner) (*Manager, *pb.Fence) {
	t.Helper()
	m, err := Open(Config{Root: filepath.Join(t.TempDir(), "network"), MaxActors: 2, LinkPool: netip.MustParsePrefix("172.30.0.0/24"), DNS: netip.MustParseAddr("1.1.1.1")}, run)
	if err != nil {
		t.Fatal(err)
	}
	return m, &pb.Fence{ProtocolVersion: "agentenv-executor-v1", ActorUid: "01900000-0000-7000-8000-000000000001", WorkerPodUid: "pod", WorkerEpoch: 1, WorkerInstanceId: "executor", AssignmentGeneration: 1}
}
func TestIsolatedTopologyAndStopTombstones(t *testing.T) {
	calls := 0
	m, f := fixture(t, func(context.Context, string, ...string) error { calls++; return nil })
	a, err := m.Attach(context.Background(), f, nil)
	if err != nil {
		t.Fatal(err)
	}
	count := calls
	again, err := m.Attach(context.Background(), f, nil)
	if err != nil || !proto.Equal(a, again) || calls != count {
		t.Fatal("retry recreated topology")
	}
	other := proto.CloneOf(f)
	other.ActorUid = "01900000-0000-7000-8000-000000000002"
	other.AssignmentGeneration = 2
	b, err := m.Attach(context.Background(), other, nil)
	if err != nil {
		t.Fatal(err)
	}
	if a.InteractionIpv4 == b.InteractionIpv4 || a.NetnsPath == b.NetnsPath {
		t.Fatal("Actors share network allocation")
	}
	if _, err := Open(m.cfg, m.run); err == nil {
		t.Fatal("unreconciled live topology reopened")
	}
	if err := m.Detach(context.Background(), f); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Attach(context.Background(), f, nil); err == nil {
		t.Fatal("late start resurrected stopped network")
	}
	if err := m.Detach(context.Background(), other); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(m.cfg, m.run)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reopened.Attach(context.Background(), f, nil); err == nil {
		t.Fatal("restart lost stop tombstone")
	}
	f.AssignmentGeneration = 3
	if _, err := reopened.Attach(context.Background(), f, nil); err != nil {
		t.Fatal(err)
	}
}
func TestCreationFailureRetainsReservation(t *testing.T) {
	calls := 0
	m, f := fixture(t, func(context.Context, string, ...string) error { calls++; return errors.New("unknown command result") })
	if _, err := m.Attach(context.Background(), f, nil); err == nil {
		t.Fatal("expected failure")
	}
	if _, err := m.Attach(context.Background(), f, nil); err == nil || calls != 1 {
		t.Fatal("uncertain namespace creation retried")
	}
	if err := m.Detach(context.Background(), f); err == nil {
		t.Fatal("ambiguous ownership cleaned up")
	}
}
func TestCleanupFailureDoesNotReleaseAllocation(t *testing.T) {
	m, f := fixture(t, func(context.Context, string, ...string) error { return nil })
	if _, err := m.Attach(context.Background(), f, nil); err != nil {
		t.Fatal(err)
	}
	m.run = func(context.Context, string, ...string) error { return errors.New("iptables permission denied") }
	if err := m.Detach(context.Background(), f); err == nil {
		t.Fatal("cleanup failure ignored")
	}
	if m.records[f.ActorUid].State == "stopped" {
		t.Fatal("failed cleanup freed allocation")
	}
}

func TestKnownPartialTopologyCanBeReclaimedAfterStop(t *testing.T) {
	calls := 0
	m, f := fixture(t, func(context.Context, string, ...string) error {
		calls++
		if calls == 3 {
			return errors.New("address setup failed")
		}
		return nil
	})
	if _, err := m.Attach(context.Background(), f, nil); err == nil {
		t.Fatal("expected partial creation")
	}
	m.run = func(context.Context, string, ...string) error { return nil }
	if err := m.Detach(context.Background(), f); err != nil {
		t.Fatal(err)
	}
	if m.records[f.ActorUid].State != "stopped" {
		t.Fatal("confirmed stop did not reclaim owned partial topology")
	}
}

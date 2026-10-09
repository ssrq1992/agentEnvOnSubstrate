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
	"net"
	"os"
	"path/filepath"
	"testing"

	"github.com/agent-substrate/substrate/internal/aenvexecutor"
	"github.com/agent-substrate/substrate/internal/cgroupstats"
	pb "github.com/agent-substrate/substrate/internal/proto/aenvexecutorpb"
	"github.com/agent-substrate/substrate/internal/proto/ateompb"
	"github.com/agent-substrate/substrate/internal/resources"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

type operations struct {
	calls              []*pb.Command
	err                error
	snapshotFiles      []string
	appliedRevision    uint64
	approvedExtensions *pb.ExtensionParams
	noEffect           bool
}

func (o *operations) Execute(_ context.Context, _ *pb.Fence, id string, c *pb.Command) (*pb.OperationResponse, error) {
	o.calls = append(o.calls, proto.CloneOf(c))
	if o.noEffect {
		return &pb.OperationResponse{OperationId: id, Effect: pb.Effect_NO_EFFECT}, errors.New("rejected before effects")
	}
	if o.err != nil {
		return nil, o.err
	}
	return &pb.OperationResponse{OperationId: id, Effect: pb.Effect_COMPLETED, SnapshotFiles: o.snapshotFiles, AppliedPolicyRevision: o.appliedRevision, ApprovedExtensions: o.approvedExtensions}, nil
}

type limits struct {
	reserved, released int
	err                error
}

func (l *limits) Reserve(*pb.Fence, uint32, uint64) (string, error) {
	l.reserved++
	return "/sys/fs/cgroup/aenv-actor-7", l.err
}
func (l *limits) Release(*pb.Fence) error { l.released++; return l.err }

func (l *limits) Read(*pb.Fence) (cgroupstats.Sample, error) {
	return cgroupstats.Sample{MemoryCurrentBytes: 100, MemoryPeakBytes: 200, MemoryWorkingSetBytes: 70, CPUUsageUsec: 123}, l.err
}

func (l *limits) Epoch(*pb.Fence) (int64, error) { return 12345, l.err }

type topology struct{ attached, detached int }

func (n *topology) Attach(context.Context, *pb.Fence, *pb.NetworkPolicy) (*pb.NetworkAttachment, error) {
	n.attached++
	return &pb.NetworkAttachment{InteractionIpv4: "172.30.0.2", NetnsPath: "/run/netns/owned"}, nil
}
func (n *topology) Detach(context.Context, *pb.Fence) error { n.detached++; return nil }

type tunnel struct{ activated, deactivated int }

func (i *tunnel) Activate(resources.ActorAttribution, uint64, func(context.Context, string, string) (net.Conn, error)) error {
	i.activated++
	return nil
}
func (i *tunnel) Deactivate(context.Context, resources.ActorAttribution) error {
	i.deactivated++
	return nil
}
func serviceFixture(t *testing.T) (*Service, *operations, *topology, *tunnel, *ateompb.RunWorkloadRequest) {
	t.Helper()
	o := &operations{}
	n := &topology{}
	i := &tunnel{}
	s, err := New("pod", "executor", t.TempDir(), 2, o, n, i, &limits{})
	if err != nil {
		t.Fatal(err)
	}
	uid := "01900000-0000-7000-8000-000000000001"
	req := &ateompb.RunWorkloadRequest{ActorUid: uid, Execution: &pb.LifecycleOperation{OperationId: "start", Fence: &pb.Fence{ProtocolVersion: aenvexecutor.ProtocolVersion, ActorUid: uid, WorkerPodUid: "pod", WorkerEpoch: 2, WorkerInstanceId: "executor", AssignmentGeneration: 7}}, AgentenvLaunch: &pb.LaunchSpec{Vcpus: 1, MemoryMib: 512, EnvdAccessToken: "credential"}}
	return s, o, n, i, req
}
func TestAdapterChecksFenceBeforeAnyEffect(t *testing.T) {
	s, o, n, _, req := serviceFixture(t)
	req.Execution.Fence.WorkerEpoch = 1
	if _, err := s.RunWorkload(context.Background(), req); status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(o.calls) != 0 || n.attached != 0 {
		t.Fatal("stale operation caused effects")
	}
}
func TestAdapterRetainsNetworkWhenExecutionUnknown(t *testing.T) {
	s, o, n, i, req := serviceFixture(t)
	o.err = &aenvexecutor.EffectUnknown{OperationID: "start", Cause: errors.New("lost response")}
	if _, err := s.RunWorkload(context.Background(), req); status.Code(err) != codes.Unavailable {
		t.Fatalf("unexpected error: %v", err)
	}
	if n.attached != 1 || n.detached != 0 || i.activated != 0 {
		t.Fatal("unknown execution released allocation or advertised ingress")
	}
	if o.calls[0].GetStart().GetNetwork().GetNetnsPath() != "/run/netns/owned" {
		t.Fatal("executor did not receive owned network")
	}
	if req.AgentenvLaunch.Network != nil {
		t.Fatal("adapter mutated caller launch")
	}
}
func TestAdapterOnlyCleansAfterConfirmedStop(t *testing.T) {
	s, o, n, i, req := serviceFixture(t)
	if _, err := s.RunWorkload(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	stop := &ateompb.TerminateWorkloadRequest{ActorUid: req.ActorUid, Execution: proto.CloneOf(req.Execution)}
	stop.Execution.OperationId = "stop"
	o.err = &aenvexecutor.EffectUnknown{OperationID: "stop", Cause: errors.New("timeout")}
	if _, err := s.TerminateWorkload(context.Background(), stop); err == nil {
		t.Fatal("unknown stop accepted")
	}
	if n.detached != 0 || i.deactivated != 0 {
		t.Fatal("unknown stop cleaned up network")
	}
	o.err = nil
	if _, err := s.TerminateWorkload(context.Background(), stop); err != nil {
		t.Fatal(err)
	}
	if n.detached != 1 || i.deactivated != 1 {
		t.Fatal("confirmed stop did not clean up")
	}
}
func TestSnapshotTransferIsImmutableAndRejectsSymlinks(t *testing.T) {
	source := t.TempDir()
	dest := filepath.Join(t.TempDir(), "copy")
	if err := os.WriteFile(filepath.Join(source, manifestName), []byte("manifest"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := copyFiles(source, dest, []string{manifestName}); err != nil {
		t.Fatal(err)
	}
	if err := copyFiles(source, dest, []string{manifestName}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, manifestName), []byte("changed"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := copyFiles(source, dest, []string{manifestName}); err == nil {
		t.Fatal("retry overwrote immutable snapshot")
	}
	if err := os.Symlink(filepath.Join(source, manifestName), filepath.Join(source, "linked")); err != nil {
		t.Fatal(err)
	}
	if err := copyFiles(source, filepath.Join(t.TempDir(), "copy"), []string{"linked", manifestName}); err == nil {
		t.Fatal("snapshot symlink accepted")
	}
	if err := copyFiles(source, dest, []string{"../escape"}); err == nil {
		t.Fatal("snapshot traversal accepted")
	}
}

func TestOnlineCaptureKeepsSourceNetworkAndIngress(t *testing.T) {
	s, ops, network, ingress, start := serviceFixture(t)
	if _, err := s.RunWorkload(t.Context(), start); err != nil {
		t.Fatal(err)
	}
	op := proto.CloneOf(start.Execution)
	op.OperationId = "capture"
	output := filepath.Join(s.Root, "export-capture")
	if err := os.MkdirAll(output, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(output, manifestName), []byte("snapshot"), 0600); err != nil {
		t.Fatal(err)
	}
	ops.snapshotFiles = []string{manifestName}
	request := &ateompb.CheckpointWorkloadRequest{ActorUid: start.ActorUid, Execution: op, Scope: ateompb.SnapshotScope_SNAPSHOT_SCOPE_FULL, ActorDirs: &ateompb.ActorDirs{CheckpointDir: filepath.Join(t.TempDir(), "snapshot")}}
	response, err := s.CaptureWorkload(t.Context(), request)
	if err != nil || len(response.GetSnapshotFiles()) != 1 {
		t.Fatal("capture failed", err)
	}
	if !ops.calls[len(ops.calls)-1].GetCapture().GetContinueRunning() || network.detached != 0 || ingress.deactivated != 0 || len(s.active) != 1 {
		t.Fatal("online capture stopped source")
	}
	// A transport-unknown capture may still be running or already committed;
	// neither interpretation authorizes removing the source network.
	ops.err = &aenvexecutor.EffectUnknown{OperationID: "capture", Cause: errors.New("lost response")}
	if _, err = s.CaptureWorkload(t.Context(), request); status.Code(err) != codes.Unavailable {
		t.Fatal("unknown capture hidden", err)
	}
	if network.detached != 0 || ingress.deactivated != 0 {
		t.Fatal("unknown capture removed source")
	}
	ops.err = nil
	request.ActorDirs.CheckpointDir = "relative-invalid"
	if _, err = s.CaptureWorkload(t.Context(), request); err == nil {
		t.Fatal("invalid snapshot destination accepted")
	}
	if network.detached != 0 || ingress.deactivated != 0 {
		t.Fatal("export copy failure stopped source")
	}
	request.ActorDirs.CheckpointDir = filepath.Join(t.TempDir(), "suspended")
	if _, err = s.CheckpointWorkload(t.Context(), request); err != nil {
		t.Fatal(err)
	}
	if ops.calls[len(ops.calls)-1].GetCapture().GetContinueRunning() || network.detached != 1 || ingress.deactivated != 1 {
		t.Fatal("persistent suspend did not stop source")
	}
}

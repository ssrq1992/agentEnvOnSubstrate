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

// Package adapter connects Substrate's lifecycle authority to the embedded executor.
package adapter

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"github.com/agent-substrate/substrate/internal/cgroupstats"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"github.com/agent-substrate/substrate/internal/actorlock"
	"github.com/agent-substrate/substrate/internal/aenvexecutor"
	pb "github.com/agent-substrate/substrate/internal/proto/aenvexecutorpb"
	"github.com/agent-substrate/substrate/internal/proto/ateompb"
	"github.com/agent-substrate/substrate/internal/resources"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

type Operations interface {
	Execute(context.Context, *pb.Fence, string, *pb.Command) (*pb.OperationResponse, error)
}
type Network interface {
	Attach(context.Context, *pb.Fence, *pb.NetworkPolicy) (*pb.NetworkAttachment, error)
	Detach(context.Context, *pb.Fence) error
}
type Ingress interface {
	Activate(resources.ActorAttribution, uint64, func(context.Context, string, string) (net.Conn, error)) error
	Deactivate(context.Context, resources.ActorAttribution) error
}
type Cgroups interface {
	Reserve(*pb.Fence, uint32, uint64) (string, error)
	Release(*pb.Fence) error
	Read(*pb.Fence) (cgroupstats.Sample, error)
	Epoch(*pb.Fence) (int64, error)
}
type activeActor struct {
	Attribution resources.ActorAttribution
	Generation  uint64
	Epoch       int64
}
type Service struct {
	ateompb.UnimplementedAteomServer
	PodUID, InstanceID, Root string
	Epoch                    uint64
	Ops                      Operations
	Network                  Network
	Ingress                  Ingress
	Cgroups                  Cgroups
	locks                    *actorlock.Locks
	mu                       sync.Mutex
	active                   map[string]*activeActor
}

func New(pod, instance, root string, epoch uint64, ops Operations, network Network, ingress Ingress, cgroups Cgroups) (*Service, error) {
	if pod == "" || instance == "" || epoch == 0 || !filepath.IsAbs(root) || ops == nil || network == nil || ingress == nil || cgroups == nil {
		return nil, fmt.Errorf("complete adapter dependencies required")
	}
	if err := os.MkdirAll(root, 0700); err != nil {
		return nil, err
	}
	return &Service{PodUID: pod, InstanceID: instance, Root: root, Epoch: epoch, Ops: ops, Network: network, Ingress: ingress, Cgroups: cgroups, locks: actorlock.New(), active: map[string]*activeActor{}}, nil
}
func (s *Service) lock(ctx context.Context, op *pb.LifecycleOperation, uid string) (func(), error) {
	if err := aenvexecutor.ValidateOperation(op, uid, s.PodUID); err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	if op.Fence.WorkerEpoch != s.Epoch || op.Fence.WorkerInstanceId != s.InstanceID {
		return nil, status.Error(codes.FailedPrecondition, "stale executor identity")
	}
	if !s.locks.Lock(ctx, uid) {
		return nil, ctx.Err()
	}
	return func() { s.locks.Unlock(uid) }, nil
}
func executeError(err error) error {
	if err == nil {
		return nil
	}
	var unknown *aenvexecutor.EffectUnknown
	if errors.As(err, &unknown) {
		return status.Error(codes.Unavailable, err.Error())
	}
	return status.Error(codes.FailedPrecondition, err.Error())
}
func attribution(space, name, uid, templateSpace, templateName string) resources.ActorAttribution {
	return resources.ActorAttribution{Ref: resources.ActorRef{Atespace: space, Name: name}, UID: uid, TemplateAtespace: templateSpace, TemplateName: templateName}
}
func (s *Service) prepare(ctx context.Context, op *pb.LifecycleOperation, launch *pb.LaunchSpec) (*pb.LaunchSpec, error) {
	if launch == nil || launch.Vcpus == 0 || launch.MemoryMib == 0 || launch.EnvdAccessToken == "" {
		return nil, status.Error(codes.InvalidArgument, "typed launch specification required")
	}
	copy := proto.CloneOf(launch)
	path, err := s.Cgroups.Reserve(op.Fence, launch.Vcpus, launch.MemoryMib)
	if err != nil {
		return nil, executeError(err)
	}
	copy.CgroupPath = path
	network, err := s.Network.Attach(ctx, op.Fence, launch.GetNetwork().GetPolicy())
	if err != nil {
		return nil, executeError(err)
	}
	copy.Network = network
	return copy, nil
}
func (s *Service) activate(actor resources.ActorAttribution, generation uint64, launch *pb.LaunchSpec) error {
	ip := net.ParseIP(launch.Network.InteractionIpv4)
	if ip == nil {
		return fmt.Errorf("invalid interaction IP")
	}
	// The tunnel supplies a requested port. It cannot select another Actor or a
	// service in the Worker namespace by supplying a host or alternate network.
	dial := func(ctx context.Context, network, address string) (net.Conn, error) {
		if network != "tcp" && network != "tcp4" {
			return nil, fmt.Errorf("only TCP ingress is supported")
		}
		_, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, err
		}
		n, err := strconv.Atoi(port)
		if err != nil || n < 1 || n > 65535 {
			return nil, fmt.Errorf("invalid ingress port")
		}
		return (&net.Dialer{}).DialContext(ctx, "tcp4", net.JoinHostPort(ip.String(), port))
	}
	epoch, err := s.Cgroups.Epoch(&pb.Fence{ProtocolVersion: aenvexecutor.ProtocolVersion, ActorUid: actor.UID, WorkerPodUid: s.PodUID, WorkerEpoch: s.Epoch, WorkerInstanceId: s.InstanceID, AssignmentGeneration: generation})
	if err != nil {
		return err
	}
	if err := s.Ingress.Activate(actor, generation, dial); err != nil {
		return err
	}
	s.mu.Lock()
	if current := s.active[actor.UID]; current == nil || current.Generation != generation {
		s.active[actor.UID] = &activeActor{Attribution: actor, Generation: generation, Epoch: epoch}
	}
	s.mu.Unlock()
	return nil
}
func (s *Service) RunWorkload(ctx context.Context, req *ateompb.RunWorkloadRequest) (*ateompb.RunWorkloadResponse, error) {
	unlock, err := s.lock(ctx, req.GetExecution(), req.GetActorUid())
	if err != nil {
		return nil, err
	}
	defer unlock()
	launch, err := s.prepare(ctx, req.Execution, req.GetAgentenvLaunch())
	if err != nil {
		return nil, err
	}
	if _, err := s.Ops.Execute(ctx, req.Execution.Fence, req.Execution.OperationId, &pb.Command{Action: &pb.Command_Start{Start: launch}}); err != nil {
		return nil, executeError(err)
	}
	if err := s.activate(attribution(req.Atespace, req.ActorName, req.ActorUid, req.ActorTemplateAtespace, req.ActorTemplateName), req.Execution.Fence.AssignmentGeneration, launch); err != nil {
		return nil, status.Error(codes.Unavailable, err.Error())
	}
	return &ateompb.RunWorkloadResponse{}, nil
}
func (s *Service) stopped(ctx context.Context, op *pb.LifecycleOperation, actor resources.ActorAttribution) error {
	if err := s.Ingress.Deactivate(ctx, actor); err != nil {
		return err
	}
	if err := s.Cgroups.Release(op.Fence); err != nil {
		return err
	}
	if err := s.Network.Detach(ctx, op.Fence); err != nil {
		return err
	}
	s.mu.Lock()
	delete(s.active, actor.UID)
	s.mu.Unlock()
	return nil
}
func (s *Service) TerminateWorkload(ctx context.Context, req *ateompb.TerminateWorkloadRequest) (*ateompb.TerminateWorkloadResponse, error) {
	unlock, err := s.lock(ctx, req.GetExecution(), req.GetActorUid())
	if err != nil {
		return nil, err
	}
	defer unlock()
	if _, err := s.Ops.Execute(ctx, req.Execution.Fence, req.Execution.OperationId, &pb.Command{Action: &pb.Command_Stop{Stop: &pb.StopSpec{}}}); err != nil {
		return nil, executeError(err)
	}
	if err := s.stopped(ctx, req.Execution, attribution(req.Atespace, req.ActorName, req.ActorUid, req.ActorTemplateAtespace, req.ActorTemplateName)); err != nil {
		return nil, status.Error(codes.Unavailable, err.Error())
	}
	return &ateompb.TerminateWorkloadResponse{}, nil
}

const manifestName = "agentenv-manifest.v1.json"

func (s *Service) CheckpointWorkload(ctx context.Context, req *ateompb.CheckpointWorkloadRequest) (*ateompb.CheckpointWorkloadResponse, error) {
	return s.captureWorkload(ctx, req, false)
}
func (s *Service) CaptureWorkload(ctx context.Context, req *ateompb.CheckpointWorkloadRequest) (*ateompb.CheckpointWorkloadResponse, error) {
	return s.captureWorkload(ctx, req, true)
}
func (s *Service) captureWorkload(ctx context.Context, req *ateompb.CheckpointWorkloadRequest, keepRunning bool) (*ateompb.CheckpointWorkloadResponse, error) {
	unlock, err := s.lock(ctx, req.GetExecution(), req.GetActorUid())
	if err != nil {
		return nil, err
	}
	defer unlock()
	if req.Scope != ateompb.SnapshotScope_SNAPSHOT_SCOPE_FULL {
		return nil, status.Error(codes.InvalidArgument, "AgentENV capture requires FULL scope")
	}
	output := filepath.Join(s.Root, "export-"+req.Execution.OperationId)
	response, err := s.Ops.Execute(ctx, req.Execution.Fence, req.Execution.OperationId, &pb.Command{Action: &pb.Command_Capture{Capture: &pb.CaptureSpec{OutputDir: output, ContinueRunning: keepRunning}}})
	if err != nil {
		return nil, executeError(err)
	}
	if !keepRunning {
		if err := s.stopped(ctx, req.Execution, attribution(req.Atespace, req.ActorName, req.ActorUid, req.ActorTemplateAtespace, req.ActorTemplateName)); err != nil {
			return nil, status.Error(codes.Unavailable, err.Error())
		}
	}
	if err := copyFiles(output, req.GetActorDirs().GetCheckpointDir(), response.SnapshotFiles); err != nil {
		return nil, status.Error(codes.Unavailable, err.Error())
	}
	return &ateompb.CheckpointWorkloadResponse{SnapshotFiles: response.SnapshotFiles}, nil
}
func (s *Service) RestoreWorkload(ctx context.Context, req *ateompb.RestoreWorkloadRequest) (*ateompb.RestoreWorkloadResponse, error) {
	unlock, err := s.lock(ctx, req.GetExecution(), req.GetActorUid())
	if err != nil {
		return nil, err
	}
	defer unlock()
	if req.Scope != ateompb.SnapshotScope_SNAPSHOT_SCOPE_FULL {
		return nil, status.Error(codes.InvalidArgument, "AgentENV restore requires FULL scope")
	}
	source := req.GetActorDirs().GetRestoreDir()
	entries, err := os.ReadDir(source)
	if err != nil {
		return nil, err
	}
	files := make([]string, 0, len(entries))
	for _, entry := range entries {
		files = append(files, entry.Name())
	}
	dest := filepath.Join(s.Root, "import-"+req.Execution.OperationId)
	if err := copyFiles(source, dest, files); err != nil {
		return nil, err
	}
	manifest, err := os.ReadFile(filepath.Join(dest, manifestName))
	if err != nil {
		return nil, err
	}
	digest := sha256.Sum256(manifest)
	launch, err := s.prepare(ctx, req.Execution, req.GetAgentenvLaunch())
	if err != nil {
		return nil, err
	}
	// Extension state is part of the captured runtime. Initial template params
	// must not overwrite an approved live patch during suspend/restore or fork.
	launch.Extensions = nil
	command := &pb.Command{Action: &pb.Command_Restore{Restore: &pb.RestoreSpec{Launch: launch, SnapshotDir: dest, ManifestSha256: hex.EncodeToString(digest[:])}}}
	if _, err := s.Ops.Execute(ctx, req.Execution.Fence, req.Execution.OperationId, command); err != nil {
		return nil, executeError(err)
	}
	if err := s.activate(attribution(req.Atespace, req.ActorName, req.ActorUid, req.ActorTemplateAtespace, req.ActorTemplateName), req.Execution.Fence.AssignmentGeneration, launch); err != nil {
		return nil, status.Error(codes.Unavailable, err.Error())
	}
	return &ateompb.RestoreWorkloadResponse{}, nil
}

func copyFiles(source, dest string, names []string) error {
	if !filepath.IsAbs(source) || !filepath.IsAbs(dest) || source == dest || len(names) == 0 {
		return fmt.Errorf("distinct absolute snapshot paths and file list required")
	}
	if err := os.MkdirAll(dest, 0700); err != nil {
		return err
	}
	for _, root := range []string{source, dest} {
		info, err := os.Lstat(root)
		if err != nil {
			return err
		}
		if !info.IsDir() {
			return fmt.Errorf("snapshot directory must not be a symlink")
		}
	}
	foundManifest := false
	for _, name := range names {
		if name == manifestName {
			foundManifest = true
		}
		if name == "" || name == "." || name == ".." || strings.ContainsAny(name, "/\\") {
			return fmt.Errorf("invalid portable snapshot filename")
		}
		src := filepath.Join(source, name)
		info, err := os.Lstat(src)
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("snapshot file must be regular")
		}
		if err := copyFile(src, filepath.Join(dest, name)); err != nil {
			return err
		}
	}
	if !foundManifest {
		return fmt.Errorf("portable manifest missing")
	}
	dir, err := os.Open(dest)
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}
func copyFile(src, dst string) error {
	if info, err := os.Lstat(dst); err == nil {
		if !info.Mode().IsRegular() {
			return fmt.Errorf("unsafe snapshot destination")
		}
		a, err := fileHash(src)
		if err != nil {
			return err
		}
		b, err := fileHash(dst)
		if err != nil {
			return err
		}
		if a != b {
			return fmt.Errorf("snapshot retry changed immutable content")
		}
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}

	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.CreateTemp(filepath.Dir(dst), ".copy-")
	if err != nil {
		return err
	}
	defer os.Remove(out.Name())
	if _, err = io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	if err = out.Sync(); err != nil {
		out.Close()
		return err
	}
	if err = out.Close(); err != nil {
		return err
	}
	return os.Rename(out.Name(), dst)
}

func fileHash(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

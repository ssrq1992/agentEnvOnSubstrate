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
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/agent-substrate/substrate/cmd/atelet/internal/ateletpath"
	"github.com/agent-substrate/substrate/internal/actorlock"
	"github.com/agent-substrate/substrate/internal/aenvexecutor"
	"github.com/agent-substrate/substrate/internal/nodepath"
	pb "github.com/agent-substrate/substrate/internal/proto/aenvexecutorpb"
	"github.com/agent-substrate/substrate/internal/proto/ateletpb"
	"github.com/agent-substrate/substrate/internal/proto/ateompb"
	"google.golang.org/protobuf/proto"
)

var agentENVLocks = actorlock.New()

var agentENVPreparationsDir = filepath.Join(nodepath.BasePath, "agentenv-preparations")

type agentENVPreparation struct {
	Execution *pb.LifecycleOperation
	Digest    string
	Launch    *pb.LaunchSpec
}

func agentENVLaunch(spec *ateletpb.WorkloadSpec, rec *sandboxAssetsRecord, paths map[string]string, cpu, memory int64) (*pb.LaunchSpec, error) {
	const mib = 1024 * 1024
	if cpu <= 0 || cpu%1000 != 0 || cpu/1000 > int64(^uint32(0)) || memory <= 0 || memory%mib != 0 || memory/mib > int64(^uint32(0)) {
		return nil, fmt.Errorf("AgentENV requires integer vCPUs and positive MiB-aligned memory")
	}
	if len(spec.GetContainers()) != 1 {
		return nil, fmt.Errorf("AgentENV launch requires one VM image container")
	}
	if len(spec.GetVolumes()) != 0 || len(spec.Containers[0].GetVolumeMounts()) != 0 {
		return nil, fmt.Errorf("AgentENV volumes require typed drive descriptors")
	}
	c := spec.Containers[0]
	name, digest, ok := strings.Cut(c.GetImage(), "@sha256:")
	if !ok || name == "" || len(digest) != 64 {
		return nil, fmt.Errorf("AgentENV image must be pinned by SHA256 digest")
	}
	decoded, err := hex.DecodeString(digest)
	if err != nil || len(decoded) != 32 || strings.ToLower(digest) != digest {
		return nil, fmt.Errorf("invalid immutable image digest")
	}
	if len(c.GetCommand()) == 0 && len(c.GetArgs()) != 0 {
		return nil, fmt.Errorf("image entrypoint must be resolved before applying args")
	}
	if len(c.GetSecurityContext().GetCapabilities().GetAdd()) > 0 || len(c.GetSecurityContext().GetCapabilities().GetDrop()) > 0 {
		return nil, fmt.Errorf("guest process capability overrides require an image configuration")
	}
	launch := &pb.LaunchSpec{ImageDigest: c.Image, Vcpus: uint32(cpu / 1000), MemoryMib: uint64(memory / mib), CompatibilityDomain: "agentenv-v0.2.3-substrate-v0.4.0-amd64", Argv: append(append([]string(nil), c.Command...), c.Args...)}

	if options := c.GetAgentenvLaunch(); options != nil {
		launch.RootfsBytes = options.RootfsBytes
		launch.ExtraBootArgs = options.ExtraBootArgs
		for _, drive := range options.Drives {
			launch.Drives = append(launch.Drives, &pb.Drive{Id: drive.Id, ImageDigest: drive.Image, MountPath: drive.MountPath, ReadOnly: drive.ReadOnly, VirtualSize: drive.VirtualSize, SubPath: drive.SubPath})
		}
	}
	if c.GetAgentenvExtensionParams() != "" {
		launch.Extensions = &pb.ExtensionParams{Json: c.GetAgentenvExtensionParams()}
	}
	for _, entry := range c.Env {
		launch.Env = append(launch.Env, &pb.EnvVar{Name: entry.Name, Value: entry.Value})
	}
	names := make([]string, 0, len(rec.Assets))
	for name := range rec.Assets {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if paths[name] == "" {
			return nil, fmt.Errorf("missing fetched runtime asset %s", name)
		}
		launch.Assets = append(launch.Assets, &pb.RuntimeAsset{Name: name, Path: paths[name], Sha256: rec.Assets[name].SHA256})
	}
	return launch, nil
}

// persistAgentENVLaunch writes the generated credential once before any VM RPC.
// Replayed requests cannot replace the payload or reuse a previous allocation.
// This file is node-local and is deliberately absent from snapshot manifests.
func persistAgentENVLaunch(root string, execution *pb.LifecycleOperation, launch *pb.LaunchSpec) (*pb.LaunchSpec, error) {
	if err := aenvexecutor.ValidateOperation(execution, execution.GetFence().GetActorUid(), execution.GetFence().GetWorkerPodUid()); err != nil {
		return nil, err
	}
	if launch == nil {
		return nil, fmt.Errorf("launch required")
	}
	if err := os.MkdirAll(root, 0700); err != nil {
		return nil, err
	}
	info, err := os.Lstat(root)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() || info.Mode().Perm()&0077 != 0 {
		return nil, fmt.Errorf("private Actor preparation directory required")
	}

	wire, err := proto.MarshalOptions{Deterministic: true}.Marshal(launch)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(wire)
	digest := hex.EncodeToString(sum[:])
	path := filepath.Join(root, "agentenv-preparation.json")
	if info, err := os.Lstat(path); err == nil {
		if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
			return nil, fmt.Errorf("unsafe Actor preparation")
		}
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if err == nil {
		var previous agentENVPreparation
		if err := json.Unmarshal(data, &previous); err != nil {
			return nil, err
		}
		old := previous.Execution.GetFence()
		current := execution.GetFence()
		if old.GetAssignmentGeneration() > current.GetAssignmentGeneration() {
			return nil, fmt.Errorf("stale AgentENV preparation")
		}
		if old.GetAssignmentGeneration() == current.GetAssignmentGeneration() {
			if !proto.Equal(previous.Execution, execution) || previous.Digest != digest || previous.Launch == nil {
				return nil, fmt.Errorf("AgentENV preparation identity or payload changed")
			}
			return previous.Launch, nil
		}
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	token := make([]byte, 32)
	if _, err := rand.Read(token); err != nil {
		return nil, err
	}
	launch = proto.CloneOf(launch)
	launch.EnvdAccessToken = hex.EncodeToString(token)
	data, err = json.Marshal(agentENVPreparation{Execution: execution, Digest: digest, Launch: launch})
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(root, 0700); err != nil {
		return nil, err
	}
	if err := writeAgentENVPreparation(path, data); err != nil {
		return nil, err
	}
	return launch, nil
}
func (s *AteomHerder) prepareAgentENV(ctx context.Context, uid string, execution *pb.LifecycleOperation, spec *ateletpb.WorkloadSpec, rec *sandboxAssetsRecord, paths map[string]string, cpu, memory int64, policy *pb.NetworkPolicy) (*pb.LaunchSpec, error) {
	launch, err := agentENVLaunch(spec, rec, paths, cpu, memory)
	if err != nil {
		return nil, err
	}
	if policy != nil {
		if err := aenvexecutor.ValidatePolicyUpdate(&pb.ApplyNetworkPolicyRequest{Execution: execution, ActorUid: uid, TargetWorkerPodUid: execution.GetFence().GetWorkerPodUid(), Policy: policy}); err != nil {
			return nil, err
		}
		launch.Network = &pb.NetworkAttachment{Policy: proto.CloneOf(policy)}
	}
	return persistAgentENVLaunch(filepath.Join(agentENVPreparationsDir, uid), execution, launch)
}
func (s *AteomHerder) runAgentENV(ctx context.Context, req *ateletpb.RunRequest) (*ateletpb.RunResponse, error) {
	if !agentENVLocks.Lock(ctx, req.ActorUid) {
		return nil, ctx.Err()
	}
	defer agentENVLocks.Unlock(req.ActorUid)
	rec, err := recordFromRequest(req.SandboxAssets)
	if err != nil {
		return nil, err
	}
	paths, err := s.ensureSandboxAssets(ctx, rec)
	if err != nil {
		return nil, err
	}
	launch, err := s.prepareAgentENV(ctx, req.ActorUid, req.Execution, req.Spec, rec, paths, req.CpuMilli, req.MemoryBytes, req.AgentenvPolicy)
	if err != nil {
		return nil, err
	}
	if err := writeSandboxRecord(req.ActorUid, rec); err != nil {
		return nil, err
	}
	client, err := s.dialAteom(ctx, req.TargetAteomUid)
	if err != nil {
		return nil, err
	}
	_, err = client.RunWorkload(ctx, &ateompb.RunWorkloadRequest{Atespace: req.Atespace, ActorName: req.ActorName, ActorUid: req.ActorUid, ActorTemplateAtespace: req.ActorTemplateAtespace, ActorTemplateName: req.ActorTemplateName, Execution: req.Execution, AgentenvLaunch: launch, RuntimeAssetPaths: paths, CpuMilli: req.CpuMilli, MemoryBytes: req.MemoryBytes, ActorDirs: ateletpath.ActorDirs(req.ActorUid)})
	if err != nil {
		return nil, err
	}
	return &ateletpb.RunResponse{}, nil
}

func agentENVRestoreDirectory(uid string, op *pb.LifecycleOperation) string {
	return filepath.Join(nodepath.ActorsDir, uid, "agentenv-restore-"+strconv.FormatUint(op.GetFence().GetAssignmentGeneration(), 10))
}

func writeAgentENVPreparation(path string, data []byte) error {
	file, err := os.CreateTemp(filepath.Dir(path), ".agentenv-")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	if _, err := file.Write(data); err != nil {
		file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if err := os.Rename(file.Name(), path); err != nil {
		return err
	}
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}
func validateAgentENVExisting(uid string, execution *pb.LifecycleOperation) error {
	if err := aenvexecutor.ValidateOperation(execution, uid, execution.GetFence().GetWorkerPodUid()); err != nil {
		return err
	}
	data, err := os.ReadFile(filepath.Join(agentENVPreparationsDir, uid, "agentenv-preparation.json"))
	if err != nil {
		return err
	}
	var previous agentENVPreparation
	if err := json.Unmarshal(data, &previous); err != nil {
		return err
	}
	if !proto.Equal(previous.Execution.GetFence(), execution.GetFence()) {
		return fmt.Errorf("AgentENV execution differs from node preparation")
	}
	return nil
}

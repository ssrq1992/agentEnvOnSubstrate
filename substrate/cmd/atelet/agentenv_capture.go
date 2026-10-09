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
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"github.com/agent-substrate/substrate/cmd/atelet/internal/ateletpath"
	"github.com/agent-substrate/substrate/internal/aenvexecutor"
	"github.com/agent-substrate/substrate/internal/apierror"
	"github.com/agent-substrate/substrate/internal/proto/ateletpb"
	"github.com/agent-substrate/substrate/internal/proto/ateompb"
	"google.golang.org/protobuf/proto"
	"os"
	"path/filepath"
)

// Capture keeps runtime assets, volumes and ingress registered. Every operation
// has a separate export directory so a later capture cannot overwrite a retry.
// Exported bytes remain until control-plane ownership has been reconciled.
func (s *AteomHerder) Capture(ctx context.Context, req *ateletpb.CheckpointRequest) (*ateletpb.CheckpointResponse, error) {
	if err := validateCheckpointRequest(req); err != nil {
		return nil, apierror.InvalidArgument("%v", err)
	}
	if req.GetScope() != ateletpb.SnapshotScope_SNAPSHOT_SCOPE_FULL || req.GetType() != ateletpb.CheckpointType_CHECKPOINT_TYPE_EXTERNAL {
		return nil, apierror.InvalidArgument("online capture requires external FULL snapshot")
	}
	if err := aenvexecutor.ValidateOperation(req.GetExecution(), req.GetActorUid(), req.GetTargetAteomUid()); err != nil {
		return nil, apierror.InvalidArgument("invalid capture identity")
	}
	if !agentENVLocks.Lock(ctx, req.ActorUid) {
		return nil, ctx.Err()
	}
	defer agentENVLocks.Unlock(req.ActorUid)
	if err := validateAgentENVExisting(req.ActorUid, req.Execution); err != nil {
		return nil, apierror.FailedPrecondition("capture allocation differs from preparation")
	}
	rec, err := readSandboxRecord(req.ActorUid)
	if err != nil {
		return nil, err
	}
	if rec.SandboxClass != "agentenv" {
		return nil, apierror.FailedPrecondition("online capture requires AgentENV")
	}
	paths, err := s.ensureSandboxAssets(ctx, rec)
	if err != nil {
		return nil, err
	}
	client, err := s.dialAteom(ctx, req.TargetAteomUid)
	if err != nil {
		return nil, err
	}
	dirs := ateletpath.ActorDirs(req.ActorUid)
	dirs.CheckpointDir = filepath.Join(filepath.Dir(dirs.CheckpointDir), "online-captures", req.Execution.OperationId)
	if err := reserveAgentENVCapture(dirs.CheckpointDir, req); err != nil {
		return nil, apierror.FailedPrecondition("%v", err)
	}
	response, err := client.CaptureWorkload(ctx, &ateompb.CheckpointWorkloadRequest{
		Execution: req.Execution, Atespace: req.Atespace, ActorName: req.ActorName, ActorUid: req.ActorUid,
		ActorTemplateAtespace: req.ActorTemplateAtespace, ActorTemplateName: req.ActorTemplateName,
		RuntimeAssetPaths: paths, Scope: ateompb.SnapshotScope_SNAPSHOT_SCOPE_FULL, ActorDirs: dirs,
	})
	if err != nil {
		return nil, err
	}
	rec.SnapshotFiles, rec.DataSnapshotFiles, err = checkpointSnapshotFiles(response, true)
	if err != nil {
		return nil, err
	}
	rec.Atespace = req.Atespace
	rec.ActorName = req.ActorName
	rec.ActorUID = req.ActorUid
	rec.ActorTemplateAtespace = req.ActorTemplateAtespace
	rec.ActorTemplateName = req.ActorTemplateName
	rec.Scope = "full"
	if err = s.uploadExternalCheckpoint(ctx, req, dirs.CheckpointDir, rec); err != nil {
		return nil, err
	}
	return &ateletpb.CheckpointResponse{}, nil
}

// Bind the upload destination and complete request to the operation before VM
// capture. A retry may resend the same export, but cannot publish that operation
// to a different destination or substitute a different allocation.
func reserveAgentENVCapture(dir string, req *ateletpb.CheckpointRequest) error {
	encoded, err := proto.MarshalOptions{Deterministic: true}.Marshal(req)
	if err != nil {
		return err
	}
	digest := sha256.Sum256(encoded)
	expected := hex.EncodeToString(digest[:])
	if err = os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	info, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode().Perm()&0077 != 0 {
		return fmt.Errorf("private capture directory required")
	}
	path := filepath.Join(dir, ".capture-intent")
	if info, err = os.Lstat(path); err == nil {
		if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || info.Size() != int64(len(expected)) {
			return fmt.Errorf("invalid capture intent")
		}
		previous, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if string(previous) != expected {
			return fmt.Errorf("capture operation payload changed")
		}
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}
	return writeAgentENVPreparation(path, []byte(expected))
}

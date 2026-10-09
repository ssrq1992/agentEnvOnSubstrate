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

package controlapi

import (
	"context"
	"errors"
	"github.com/agent-substrate/substrate/cmd/ateapi/internal/apivalidation"
	"github.com/agent-substrate/substrate/cmd/ateapi/internal/store"
	"github.com/agent-substrate/substrate/internal/apierror"
	"github.com/agent-substrate/substrate/internal/proto/ateletpb"
	"github.com/agent-substrate/substrate/internal/resources"
	pb "github.com/agent-substrate/substrate/pkg/proto/ateapipb"
	"google.golang.org/protobuf/proto"
)

func (s *RPCService) CaptureActorSnapshot(ctx context.Context, req *pb.CaptureActorSnapshotRequest) (*pb.Tag, error) {
	if errs := apivalidation.ValidateCaptureActorSnapshotRequest(ctx, req); len(errs) > 0 {
		return nil, resources.ToAPIError(errs)
	}
	tag, err := s.actorWorkflow.captureActorSnapshot(ctx, req)
	return publicTag(tag), err
}

func (w *ActorWorkflow) captureActorSnapshot(ctx context.Context, req *pb.CaptureActorSnapshotRequest) (*pb.Tag, error) {
	ref := resources.ActorRefFromObjectRef(req.Actor)
	ctx, lease, err := w.acquireActorLease(ctx, ref)
	if err != nil {
		return nil, err
	}
	defer lease.Close()
	tagRef := resources.TagRef{Atespace: ref.Atespace, Name: req.TagName}
	ctx, tagLease, err := acquireTagLease(ctx, w.store, tagRef)
	if err != nil {
		return nil, err
	}
	defer tagLease.Close()
	actor, err := w.store.GetActor(ctx, ref)
	if err != nil {
		return nil, err
	}
	if err := requireConfirmedExtensions(actor); err != nil {
		return nil, err
	}
	if actor.GetMetadata().GetUid() != req.Uid {
		return nil, apierror.FailedPrecondition("Actor incarnation changed")
	}
	tag, err := w.store.GetTag(ctx, tagRef)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return nil, err
	}
	if err == nil {
		if tag.GetStatus().GetCaptureActorUid() != req.Uid || tag.GetStatus().GetCaptureAssignment().GetAssignmentGeneration() != req.AssignmentGeneration || !proto.Equal(tag.GetSourceActor(), req.Actor) {
			return nil, apierror.AlreadyExists("capture destination belongs to another operation")
		}
		// A published result remains readable after the source allocation moves.
		if tag.GetStatus().GetSnapshot() != nil {
			return tag, nil
		}
	}
	assignment := actor.GetStatus().GetWorkerAssignment()
	if actor.GetStatus().GetState() != pb.ActorState_ACTOR_STATE_RUNNING || assignment.GetAssignmentGeneration() != req.AssignmentGeneration || executionIdentity(actor, "capture", "") == nil {
		return nil, apierror.FailedPrecondition("running AgentENV allocation changed")
	}
	if tag != nil && !proto.Equal(tag.GetStatus().GetCaptureAssignment(), assignment) {
		return nil, apierror.FailedPrecondition("capture allocation changed")
	}
	template, err := resolveActorTemplate(ctx, w.store, actor)
	if err != nil {
		return nil, err
	}
	if template.GetSandboxConfig().GetSandboxClass() != pb.SandboxClass_SANDBOX_CLASS_AGENTENV {
		return nil, apierror.FailedPrecondition("online capture requires AgentENV")
	}
	worker, err := w.store.GetWorker(ctx, assignment.GetWorker().GetName())
	if err != nil {
		return nil, err
	}
	claim, err := w.store.GetWorkerAssignment(ctx, assignment.GetWorker().GetName(), req.Uid)
	if err != nil {
		return nil, err
	}
	if err := validateConnectionAssignment(actor, worker, claim); err != nil {
		return nil, err
	}
	if tag == nil {
		tag, err = w.store.CreateTag(ctx, &pb.Tag{
			Metadata:    &pb.ResourceMetadata{Atespace: ref.Atespace, Name: req.TagName},
			SourceActor: proto.CloneOf(req.Actor), Scope: pb.TagScope_TAG_SCOPE_ATESPACE,
			Status: &pb.TagStatus{ActorTemplateUid: template.GetMetadata().GetUid(), StorageLocation: template.GetSnapshotConfig().GetStorageLocation(), CaptureActorUid: req.Uid, CaptureAssignment: proto.CloneOf(assignment)},
		})
		if err != nil {
			return nil, err
		}
	}
	dst, err := resources.NewTagSnapshotURI(tag.GetStatus().GetStorageLocation(), ref.Atespace, tag.GetMetadata().GetUid())
	if err != nil {
		return nil, err
	}
	spec, err := workloadSpecFromActorTemplate(template, actor, nil)
	if err != nil {
		return nil, err
	}
	conn, err := w.dialer.DialForAteletOnNode(assignment.GetNodeName())
	if err != nil {
		return nil, err
	}
	capture := &ateletpb.CheckpointRequest{
		Execution: executionIdentity(actor, "capture", dst.String()), TargetAteomUid: assignment.GetWorkerPodUid(),
		Atespace: ref.Atespace, ActorName: ref.Name, ActorUid: req.Uid,
		ActorTemplateAtespace: actor.GetActorTemplate().GetAtespace(), ActorTemplateName: actor.GetActorTemplate().GetName(), Spec: spec,
		Type: ateletpb.CheckpointType_CHECKPOINT_TYPE_EXTERNAL, Scope: ateletpb.SnapshotScope_SNAPSHOT_SCOPE_FULL,
		Config: &ateletpb.CheckpointRequest_ExternalConfig{ExternalConfig: &ateletpb.ExternalCheckpointConfiguration{SnapshotUri: dst.String()}},
	}
	if len(tag.GetStatus().GetCaptureRequest()) == 0 {
		payload, err := proto.MarshalOptions{Deterministic: true}.Marshal(capture)
		if err != nil {
			return nil, err
		}
		tag, err = w.store.UpdateTag(ctx, tagRef, store.PreconditionFrom(tag), func(updated *pb.Tag) error { updated.Status.CaptureRequest = payload; return nil })
		if err != nil {
			return nil, err
		}
	} else {
		capture = &ateletpb.CheckpointRequest{}
		if err := proto.Unmarshal(tag.Status.CaptureRequest, capture); err != nil {
			return nil, apierror.FailedPrecondition("invalid persisted capture intent")
		}
	}
	_, err = ateletpb.NewAteomHerderClient(conn).Capture(ctx, capture)
	if err != nil {
		return nil, err
	}
	return w.ensureTagFinalized(ctx, tag, &pb.ExternalSnapshot{ContentScope: pb.SnapshotContentScope_SNAPSHOT_CONTENT_SCOPE_FULL}, dst)
}

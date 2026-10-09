package snapshots

import (
	"agentenv/services/aenv-api-bridge/internal/auth"
	"agentenv/services/aenv-api-bridge/internal/metadata"
	"context"
	"encoding/json"
	"errors"
	pb "github.com/agent-substrate/substrate/pkg/proto/ateapipb"
	"google.golang.org/protobuf/types/known/timestamppb"
	"testing"
	"time"
)

type fixture struct {
	Store
	job                    metadata.Capture
	commitFail             bool
	captures, observations int
	generation             uint64
	state                  pb.ActorState
	tag                    *pb.Tag
}

func (f *fixture) LockOperation(context.Context, string, string) (func(), error) {
	return func() {}, nil
}
func (f *fixture) Profile(context.Context, metadata.Sandbox) (metadata.Profile, error) {
	return metadata.Profile{CPUCount: 2, MemoryMB: 256, DiskSizeMB: 2048}, nil
}
func (f *fixture) Capture(_ context.Context, r metadata.Request) (metadata.Capture, error) {
	if f.job.Request.ID == "" {
		return metadata.Capture{}, metadata.ErrNotFound
	}
	if f.job.Request.ID != r.ID || f.job.Request.Digest != r.Digest {
		return metadata.Capture{}, metadata.ErrConflict
	}
	return f.job, nil
}
func (f *fixture) ReserveCapture(_ context.Context, b metadata.Sandbox, r metadata.Request, wire []byte, alias string) (metadata.Capture, error) {
	r.State = "pending"
	f.job = metadata.Capture{Source: b, Request: r, Prepared: wire}
	return f.job, nil
}
func (f *fixture) ConfirmCapture(_ context.Context, c metadata.Capture, id, uid string, wire json.RawMessage, created time.Time) error {
	if f.commitFail {
		return errors.New("database unavailable")
	}
	f.job.Request.State = "completed"
	f.job.Request.Result = wire
	return nil
}

type runtime struct{ f *fixture }

func (r runtime) Get(context.Context, auth.Tenant, metadata.Sandbox) (*pb.Actor, error) {
	r.f.observations++
	return &pb.Actor{Metadata: &pb.ResourceMetadata{Uid: "uid"}, ActorTemplate: &pb.ObjectRef{Atespace: "space", Name: "native"}, Status: &pb.ActorStatus{State: r.f.state, WorkerAssignment: &pb.WorkerAssignment{AssignmentGeneration: r.f.generation}}}, nil
}
func (r runtime) Template(context.Context, auth.Tenant, string) (*pb.ActorTemplate, error) {
	return &pb.ActorTemplate{Metadata: &pb.ResourceMetadata{Atespace: "space", Name: "native", Uid: "template-uid"}}, nil
}
func (r runtime) Capture(_ context.Context, t auth.Tenant, b metadata.Sandbox, generation uint64, name string) (*pb.Tag, error) {
	r.f.captures++
	if generation != 5 || b.ActorUID != "uid" {
		panic("capture moved to new assignment")
	}
	if r.f.tag == nil {
		now := timestamppb.New(time.Unix(100, 0))
		r.f.tag = &pb.Tag{Metadata: &pb.ResourceMetadata{Atespace: t.Atespace, Name: name, Uid: "tag-uid", CreateTime: now, UpdateTime: now}, Status: &pb.TagStatus{ActorTemplateUid: "template-uid", CaptureActorUid: b.ActorUID, CaptureAssignment: &pb.WorkerAssignment{AssignmentGeneration: generation}, Snapshot: &pb.ExternalSnapshot{ContentScope: pb.SnapshotContentScope_SNAPSHOT_CONTENT_SCOPE_FULL, SnapshotUri: "s3://private/snapshot"}}}
	}
	return r.f.tag, nil
}
func TestCaptureRetryAfterCommitFailureKeepsSourceAndReturnsConfirmedRecord(t *testing.T) {
	f := &fixture{generation: 5, state: pb.ActorState_ACTOR_STATE_RUNNING, commitFail: true}
	s := &Service{Store: f, Control: runtime{f}}
	tnt := auth.Tenant{ID: "tenant", Atespace: "space"}
	b := metadata.Sandbox{Tenant: tnt.ID, ExternalID: "source", ActorAtespace: tnt.Atespace, ActorName: "actor", ActorUID: "uid"}
	if _, err := s.Create(t.Context(), tnt, b, "named", "key"); err == nil || f.job.Request.State != "pending" {
		t.Fatal("unknown publication confirmed", err)
	}
	f.generation = 10
	f.commitFail = false
	info, err := s.Create(t.Context(), tnt, b, "named", "key")
	if err != nil || info.ID == "" || info.CPUCount != 2 || info.CreatedAt != time.Unix(100, 0).UTC() || f.observations != 1 || f.captures != 2 {
		t.Fatal(info, err, f.observations, f.captures)
	}
	again, err := s.Create(t.Context(), tnt, b, "named", "key")
	if err != nil || again.ID != info.ID || f.captures != 2 {
		t.Fatal("confirmed snapshot recaptured", again, err)
	}
	if _, err = s.Create(t.Context(), tnt, b, "different", "key"); !errors.Is(err, metadata.ErrConflict) {
		t.Fatal("alias changed on retry", err)
	}
}
func TestCaptureRejectsPausedSourceAndBadNameBeforeDispatch(t *testing.T) {
	f := &fixture{generation: 5, state: pb.ActorState_ACTOR_STATE_SUSPENDED}
	s := &Service{Store: f, Control: runtime{f}}
	tnt := auth.Tenant{ID: "tenant", Atespace: "space"}
	b := metadata.Sandbox{Tenant: tnt.ID, ExternalID: "source", ActorAtespace: tnt.Atespace, ActorName: "actor", ActorUID: "uid"}
	for _, alias := range []string{"with space", "valid"} {
		if _, err := s.Create(t.Context(), tnt, b, alias, "key"); err == nil {
			t.Fatal("invalid snapshot accepted", alias)
		}
	}
	if f.captures != 0 || f.job.Request.ID != "" {
		t.Fatal("rejected snapshot created intent")
	}
}

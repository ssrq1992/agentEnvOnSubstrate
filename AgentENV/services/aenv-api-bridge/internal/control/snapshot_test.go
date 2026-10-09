package control

import (
	"context"
	pb "github.com/agent-substrate/substrate/pkg/proto/ateapipb"
	"google.golang.org/grpc"
	"testing"
)

type snapshotRPC struct {
	*controlStub
	request  *pb.CaptureActorSnapshotRequest
	response *pb.Tag
}

func (s *snapshotRPC) CaptureActorSnapshot(ctx context.Context, req *pb.CaptureActorSnapshotRequest, _ ...grpc.CallOption) (*pb.Tag, error) {
	s.record(ctx)
	s.request = req
	return s.response, nil
}
func TestCaptureChecksTenantAndAcknowledgment(t *testing.T) {
	c, base, tenant, sandbox := fixture(t)
	sandbox.ActorUID = "actor-uid"
	rpc := &snapshotRPC{controlStub: base, response: &pb.Tag{Metadata: &pb.ResourceMetadata{Atespace: "space", Name: "snapshot"}, Status: &pb.TagStatus{CaptureActorUid: "actor-uid", CaptureAssignment: &pb.WorkerAssignment{AssignmentGeneration: 7}, Snapshot: &pb.ExternalSnapshot{ContentScope: pb.SnapshotContentScope_SNAPSHOT_CONTENT_SCOPE_FULL, SnapshotUri: "s3://bucket/snapshot"}}}}
	c.RPC = rpc
	if _, err := c.Capture(t.Context(), tenant, sandbox, 7, "snapshot"); err != nil {
		t.Fatal(err)
	}
	if rpc.request.GetUid() != sandbox.ActorUID || rpc.request.GetAssignmentGeneration() != 7 || len(rpc.authorization) != 1 || rpc.authorization[0] != "Bearer tenant-token" {
		t.Fatal("capture identity or credentials lost")
	}
	rpc.response.Status.CaptureAssignment.AssignmentGeneration++
	if _, err := c.Capture(t.Context(), tenant, sandbox, 7, "snapshot"); err == nil {
		t.Fatal("wrong allocation acknowledged")
	}
	calls := rpc.calls
	sandbox.Tenant = "other"
	if _, err := c.Capture(t.Context(), tenant, sandbox, 7, "snapshot"); err == nil || rpc.calls != calls {
		t.Fatal("cross tenant capture reached control plane")
	}
}
func TestSnapshotChildRejectsConflictingSource(t *testing.T) {
	c, rpc, tenant, sandbox := fixture(t)
	source := &pb.ObjectRef{Atespace: tenant.Atespace, Name: "snapshot"}
	rpc.actor.SourceTag = source
	rpc.actor.SourceTagUid = "tag-uid"
	if _, err := c.CreateFromSnapshot(t.Context(), tenant, sandbox, "template", source, "tag-uid"); err != nil {
		t.Fatal(err)
	}
	rpc.actor.SourceTag = &pb.ObjectRef{Atespace: tenant.Atespace, Name: "other"}
	if _, err := c.CreateFromSnapshot(t.Context(), tenant, sandbox, "template", source, "tag-uid"); err == nil {
		t.Fatal("conflicting source accepted")
	}
	calls := rpc.calls
	if _, err := c.CreateFromSnapshot(t.Context(), tenant, sandbox, "template", &pb.ObjectRef{Atespace: "other", Name: "snapshot"}, "tag-uid"); err == nil || rpc.calls != calls {
		t.Fatal("cross tenant source reached control plane")
	}
}

package httpapi

import (
	"agentenv/services/aenv-api-bridge/internal/auth"
	"agentenv/services/aenv-api-bridge/internal/guestmetrics"
	"agentenv/services/aenv-api-bridge/internal/metadata"
	"context"
	"encoding/json"
	pb "github.com/agent-substrate/substrate/pkg/proto/ateapipb"
	"net/url"
	"testing"
	"time"
)

type batchFixture struct {
	storeStub
	rows   map[string]metadata.Sandbox
	states map[string]pb.ActorState
	calls  int
}

func (f *batchFixture) Get(_ context.Context, tenant, id string) (metadata.Sandbox, error) {
	b, ok := f.rows[id]
	if !ok || tenant != b.Tenant {
		return metadata.Sandbox{}, metadata.ErrNotFound
	}
	return b, nil
}

type batchRuntime struct{ f *batchFixture }

func (r batchRuntime) Get(_ context.Context, t auth.Tenant, b metadata.Sandbox) (*pb.Actor, error) {
	return &pb.Actor{Metadata: &pb.ResourceMetadata{Uid: b.ActorUID}, Status: &pb.ActorStatus{State: r.f.states[b.ExternalID], WorkerAssignment: &pb.WorkerAssignment{ExecutorInstanceId: "current", AssignmentGeneration: 3}}}, nil
}
func (f *batchFixture) Latest(_ context.Context, b metadata.Sandbox, a *pb.WorkerAssignment) (*guestmetrics.Sample, error) {
	if a.ExecutorInstanceId != "current" || a.AssignmentGeneration != 3 || b.Tenant != "tenant" {
		panic("unfenced metrics")
	}
	f.calls++
	return &guestmetrics.Sample{CPUCount: 2}, nil
}
func TestBatchMetricsCSVIsolationAndRunningFilter(t *testing.T) {
	id := "11111111-1111-4111-8111-111111111111"
	paused := "22222222-2222-4222-8222-222222222222"
	missing := "33333333-3333-4333-8333-333333333333"
	f := &batchFixture{rows: map[string]metadata.Sandbox{}, states: map[string]pb.ActorState{id: pb.ActorState_ACTOR_STATE_RUNNING, paused: pb.ActorState_ACTOR_STATE_SUSPENDED}}
	for _, key := range []string{id, paused} {
		f.rows[key] = metadata.Sandbox{Tenant: "tenant", ExternalID: key, ActorAtespace: "space", ActorUID: "uid-" + key}
	}
	h, err := (&Server{Auth: authStub{}, Store: f, Control: &controlStub{}, InfoControl: batchRuntime{f}, LatestMetrics: f, RequestTimeout: time.Second}).Handler()
	if err != nil {
		t.Fatal(err)
	}
	for _, query := range []string{"", "sandbox_ids=bad", "sandbox_ids=" + id + "," + id, "sandbox_ids=" + id + "&sandbox_ids=" + paused} {
		if w := request(h, "GET", "/sandboxes/metrics?"+query, ""); w.Code != 400 {
			t.Fatal(query, w.Code)
		}
	}
	if f.calls != 0 {
		t.Fatal("invalid CSV reached metrics")
	}
	w := request(h, "GET", "/sandboxes/metrics?sandbox_ids="+id+","+paused+","+missing, "")
	var body struct {
		Sandboxes map[string]guestmetrics.Sample `json:"sandboxes"`
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &body) != nil || len(body.Sandboxes) != 1 || body.Sandboxes[id].CPUCount != 2 || f.calls != 1 {
		t.Fatal(w.Code, w.Body.String())
	}
	b := f.rows[id]
	b.ActorAtespace = "foreign"
	f.rows[id] = b
	w = request(h, "GET", "/sandboxes/metrics?sandbox_ids="+url.QueryEscape(id), "")
	if w.Code != 200 || w.Body.String() != "{\"sandboxes\":{}}\n" || f.calls != 1 {
		t.Fatal("cross-space metrics leaked", w.Code, w.Body.String())
	}
}

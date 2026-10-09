package guestmetrics

import (
	"agentenv/services/aenv-api-bridge/internal/auth"
	"agentenv/services/aenv-api-bridge/internal/metadata"
	"context"
	pb "github.com/agent-substrate/substrate/pkg/proto/ateapipb"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"sync"
	"testing"
)

type repositoryStub struct {
	mu            sync.Mutex
	calls         int
	appended      []string
	failAfterPage bool
}

func (s *repositoryStub) Candidates(_ context.Context, tenant, id string) ([]metadata.Sandbox, error) {
	s.calls++
	if id == "last" {
		if s.failAfterPage {
			return nil, context.DeadlineExceeded
		}
		return nil, nil
	}
	if tenant != "" || id != "" {
		panic("wrong cursor")
	}
	return []metadata.Sandbox{{Tenant: "tenant", ExternalID: "running", ActorAtespace: "space", ActorUID: "uid"}, {Tenant: "tenant", ExternalID: "paused", ActorAtespace: "space", ActorUID: "paused"}, {Tenant: "tenant", ExternalID: "last", ActorAtespace: "wrong", ActorUID: "other"}}, nil
}
func (s *repositoryStub) Append(_ context.Context, b metadata.Sandbox, _ *pb.GetActorGuestMetricsResponse) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.appended = append(s.appended, b.ExternalID)
	return nil
}
func (s *repositoryStub) Prune(context.Context) error { return nil }

type metricsControlStub struct {
	mu    sync.Mutex
	calls []string
}

func (s *metricsControlStub) GuestMetrics(_ context.Context, t auth.Tenant, b metadata.Sandbox) (*pb.GetActorGuestMetricsResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if t.ID != b.Tenant || t.Atespace != b.ActorAtespace {
		panic("tenant mismatch reached control")
	}
	s.calls = append(s.calls, b.ExternalID)
	if b.ExternalID == "paused" {
		return nil, status.Error(codes.FailedPrecondition, "not running")
	}
	return &pb.GetActorGuestMetricsResponse{ActorUid: b.ActorUID}, nil
}
func TestCollectorSkipsUnmeasuredAndMismatchedTenants(t *testing.T) {
	repo := &repositoryStub{}
	rpc := &metricsControlStub{}
	c := &Collector{Store: repo, Control: rpc, Tenants: map[string]auth.Tenant{"tenant": {ID: "tenant", Atespace: "space"}}}
	if err := c.Sweep(t.Context()); err == nil {
		t.Fatal("missing tenant mapping was not reported")
	}
	if len(repo.appended) != 1 || repo.appended[0] != "running" || len(rpc.calls) != 2 || c.cursorID != "" {
		t.Fatal("collector fabricated a paused sample, leaked tenant, or failed cursor wrap")
	}
}
func TestCollectorContinuesCursorAfterPageFailure(t *testing.T) {
	repo := &repositoryStub{failAfterPage: true}
	rpc := &metricsControlStub{}
	c := &Collector{Store: repo, Control: rpc, Tenants: map[string]auth.Tenant{"tenant": {ID: "tenant", Atespace: "space"}}}
	if err := c.Sweep(t.Context()); err == nil || c.cursorID != "last" {
		t.Fatal("failure discarded pagination progress")
	}
	calls := len(rpc.calls)
	repo.failAfterPage = false
	if err := c.Sweep(t.Context()); err != nil || c.cursorID != "" || len(rpc.calls) != calls {
		t.Fatal("retry recollected the first page", err)
	}
}

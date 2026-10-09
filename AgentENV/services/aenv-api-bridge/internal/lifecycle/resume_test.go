package lifecycle

import (
	"agentenv/services/aenv-api-bridge/internal/auth"
	"agentenv/services/aenv-api-bridge/internal/metadata"
	"context"
	pb "github.com/agent-substrate/substrate/pkg/proto/ateapipb"
	"testing"
)

type resumeStoreStub struct {
	storeStub
	resume          metadata.ResumeIntent
	resumeConfirmed int
}

func (s *resumeStoreStub) ReserveResume(_ context.Context, b metadata.Sandbox, source string, seconds int, running bool) (metadata.ResumeIntent, error) {
	if source != s.resume.SourceURI || seconds != s.resume.Timeout || b.ActorUID != s.resume.Sandbox.ActorUID {
		panic("resume preparation changed")
	}
	return s.resume, nil
}
func (s *resumeStoreStub) ConfirmResumed(context.Context, metadata.ResumeIntent) error {
	s.resumeConfirmed++
	return nil
}
func (s *resumeStoreStub) ClaimSuspends(context.Context, int) ([]metadata.SuspendIntent, error) {
	return nil, nil
}
func (s *resumeStoreStub) ClaimResumes(context.Context, int) ([]metadata.ResumeIntent, error) {
	return []metadata.ResumeIntent{s.resume}, nil
}

type resumeControlStub struct {
	controlStub
	source    string
	running   bool
	resumeErr error
	tokens    int
	store     *resumeStoreStub
}

func (c *resumeControlStub) ResumeSource(context.Context, auth.Tenant, metadata.Sandbox) (string, bool, error) {
	return c.source, c.running, nil
}
func (c *resumeControlStub) ResumePrepared(_ context.Context, _ auth.Tenant, i metadata.ResumeIntent) error {
	if i.SourceURI != c.source {
		panic("snapshot source changed")
	}
	c.calls++
	return c.resumeErr
}
func (c *resumeControlStub) Connect(context.Context, auth.Tenant, metadata.Sandbox) (*pb.ConnectActorResponse, error) {
	if c.store.resumeConfirmed == 0 {
		panic("credential returned before timer commit")
	}
	c.tokens++
	return &pb.ConnectActorResponse{EnvdAccessToken: "test-secret", EnvdVersion: "0.5.1"}, nil
}
func TestResumeUnknownOutcomeKeepsSourceAndCommitsBeforeCredentials(t *testing.T) {
	tenant := auth.Tenant{ID: "tenant", Atespace: "space"}
	b := metadata.Sandbox{Tenant: "tenant", ActorAtespace: "space", ActorUID: "uid", ExternalID: "sandbox"}
	st := &resumeStoreStub{resume: metadata.ResumeIntent{Sandbox: b, SourceURI: "s3://bucket/source", Timeout: 300, Request: metadata.Request{Tenant: b.Tenant, ExternalID: b.ExternalID, Kind: "resume", State: "pending", ID: "stable"}}}
	rpc := &resumeControlStub{source: st.resume.SourceURI, resumeErr: context.DeadlineExceeded, store: st}
	s := &Service{Store: st, Control: rpc, Tenants: map[string]auth.Tenant{tenant.ID: tenant}}
	if _, _, err := s.Connect(t.Context(), tenant, b, 300, false); err == nil || st.resumeConfirmed != 0 || rpc.tokens != 0 {
		t.Fatal("unknown resume committed")
	}
	rpc.resumeErr = nil
	rpc.running = true
	if err := s.Sweep(t.Context()); err != nil || st.resumeConfirmed != 1 || rpc.tokens != 0 {
		t.Fatal("background recovery fetched a credential or lost source", err)
	}
	got, resumed, err := s.Connect(t.Context(), tenant, b, 300, false)
	if err != nil || !resumed || got.EnvdAccessToken != "test-secret" || rpc.tokens != 1 {
		t.Fatal("retry lost original resume semantics", err)
	}
	if _, _, err = s.Connect(t.Context(), tenant, b, 300, true); err != metadata.ErrConflict {
		t.Fatal("legacy resume accepted already running Actor", err)
	}
}

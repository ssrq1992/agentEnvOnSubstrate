package expiry

import (
	"agentenv/services/aenv-api-bridge/internal/auth"
	"agentenv/services/aenv-api-bridge/internal/metadata"
	"context"
	"errors"
	"testing"
	"time"
)

type storeStub struct {
	claims    []metadata.Expiry
	confirmed []string
	fail      bool
}

func (s *storeStub) ClaimExpired(context.Context, int) ([]metadata.Expiry, error) {
	return s.claims, nil
}
func (s *storeStub) ConfirmDeleted(_ context.Context, r metadata.Request) error {
	if s.fail {
		return errors.New("commit failed")
	}
	s.confirmed = append(s.confirmed, r.ExternalID)
	return nil
}

type controlStub struct {
	calls []string
	fail  string
}

func (c *controlStub) Delete(ctx context.Context, _ auth.Tenant, b metadata.Sandbox) error {
	if _, ok := ctx.Deadline(); !ok {
		panic("unbounded RPC")
	}
	c.calls = append(c.calls, b.ExternalID)
	if b.ExternalID == c.fail {
		return context.DeadlineExceeded
	}
	return nil
}
func claim(id string) metadata.Expiry {
	return metadata.Expiry{Sandbox: metadata.Sandbox{Tenant: "tenant", ActorAtespace: "space", ActorUID: "uid-" + id, ExternalID: id}, Request: metadata.Request{Tenant: "tenant", ExternalID: id, Kind: "delete"}}
}
func worker(s *storeStub, c *controlStub) *Worker {
	return &Worker{Store: s, Control: c, Tenants: map[string]auth.Tenant{"tenant": {ID: "tenant", Atespace: "space"}}, Batch: 10, RPCTimeout: time.Second}
}
func TestUnknownDeleteDoesNotCommitAndSiblingsContinue(t *testing.T) {
	s := &storeStub{claims: []metadata.Expiry{claim("one"), claim("two")}}
	c := &controlStub{fail: "one"}
	w := worker(s, c)
	if w.Sweep(t.Context()) == nil {
		t.Fatal("unknown outcome hidden")
	}
	if len(s.confirmed) != 1 || s.confirmed[0] != "two" {
		t.Fatalf("incorrect acknowledgements %v", s.confirmed)
	}
	c.fail = ""
	s.claims = s.claims[:1]
	if err := w.Sweep(t.Context()); err != nil {
		t.Fatal(err)
	}
	if len(c.calls) != 3 || c.calls[2] != "one" {
		t.Fatal("pending intent not retried")
	}
}
func TestTenantMismatchNeverCallsControl(t *testing.T) {
	s := &storeStub{claims: []metadata.Expiry{claim("one")}}
	s.claims[0].Sandbox.ActorAtespace = "other"
	c := &controlStub{}
	if worker(s, c).Sweep(t.Context()) == nil || len(c.calls) != 0 || len(s.confirmed) != 0 {
		t.Fatal("cross tenant deletion")
	}
}
func TestMetadataCommitFailureRemainsRetryable(t *testing.T) {
	s := &storeStub{claims: []metadata.Expiry{claim("one")}, fail: true}
	c := &controlStub{}
	w := worker(s, c)
	if w.Sweep(t.Context()) == nil {
		t.Fatal("commit failure hidden")
	}
	s.fail = false
	if err := w.Sweep(t.Context()); err != nil {
		t.Fatal(err)
	}
	if len(c.calls) != 2 || len(s.confirmed) != 1 {
		t.Fatal("deletion not retried after unknown commit")
	}
}

type pauseStub struct {
	calls int
	fail  bool
}

func (p *pauseStub) PauseExpired(context.Context, auth.Tenant, metadata.Expiry) error {
	p.calls++
	if p.fail {
		return context.DeadlineExceeded
	}
	return nil
}
func TestAutoPauseNeverDeletes(t *testing.T) {
	s := &storeStub{claims: []metadata.Expiry{claim("one")}}
	s.claims[0].Request.Kind = "suspend"
	c := &controlStub{}
	p := &pauseStub{fail: true}
	w := worker(s, c)
	w.Pauses = p
	if w.Sweep(t.Context()) == nil {
		t.Fatal("unknown pause outcome hidden")
	}
	p.fail = false
	if err := w.Sweep(t.Context()); err != nil {
		t.Fatal(err)
	}
	if p.calls != 2 || len(c.calls) != 0 || len(s.confirmed) != 0 {
		t.Fatal("pause was deleted or falsely acknowledged")
	}
}

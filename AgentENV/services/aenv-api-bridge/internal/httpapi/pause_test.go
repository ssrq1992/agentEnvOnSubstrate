package httpapi

import (
	"agentenv/services/aenv-api-bridge/internal/auth"
	"agentenv/services/aenv-api-bridge/internal/metadata"
	"context"
	"testing"
	"time"
)

type pauseStub struct {
	calls int
	err   error
}

func (s *pauseStub) Pause(_ context.Context, t auth.Tenant, b metadata.Sandbox) error {
	if t.ID != b.Tenant || b.ActorUID != "uid" {
		panic("unbound pause")
	}
	s.calls++
	return s.err
}
func TestPauseRequiresPersistentConfirmation(t *testing.T) {
	_, st, c := setup(t)
	p := &pauseStub{err: context.DeadlineExceeded}
	h, err := (&Server{Auth: authStub{}, Store: st, Control: c, Pauses: p, RequestTimeout: time.Second}).Handler()
	if err != nil {
		t.Fatal(err)
	}
	if w := request(h, "POST", "/sandboxes/sandbox/pause", ""); w.Code != 504 {
		t.Fatal("unknown pause acknowledged", w.Code)
	}
	p.err = nil
	if w := request(h, "POST", "/sandboxes/sandbox/pause", ""); w.Code != 204 {
		t.Fatal("confirmed pause failed", w.Code)
	}
	st.row.ActorAtespace = "other"
	if w := request(h, "POST", "/sandboxes/sandbox/pause", ""); w.Code != 404 || p.calls != 2 {
		t.Fatal("cross tenant pause reached lifecycle")
	}
}

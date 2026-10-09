package httpapi

import (
	"agentenv/services/aenv-api-bridge/internal/auth"
	"agentenv/services/aenv-api-bridge/internal/metadata"
	"context"
	"testing"
	"time"
)

type refreshStub struct {
	calls    int
	duration *int
}

func (s *refreshStub) Refresh(_ context.Context, t auth.Tenant, b metadata.Sandbox, duration *int) error {
	if t.ID != "tenant" || b.ActorUID != "uid" {
		panic("unbound refresh")
	}
	s.calls++
	s.duration = duration
	return nil
}
func TestRefreshOptionalBodyAndDuration(t *testing.T) {
	_, store, control := setup(t)
	service := &refreshStub{}
	h, err := (&Server{Auth: authStub{}, Store: store, Control: control, Refreshes: service, RequestTimeout: time.Second}).Handler()
	if err != nil {
		t.Fatal(err)
	}
	for _, body := range []string{"", "null", "{}", "{\"duration\":0}", "{\"duration\":4294967295}"} {
		if w := request(h, "POST", "/sandboxes/sandbox/refreshes", body); w.Code != 204 {
			t.Fatal(body, w.Code, w.Body.String())
		}
	}
	calls := service.calls
	for _, body := range []string{`{"duration":-1}`, `{"duration":4294967296}`, `{"duration":1.5}`, `{} {}`, `[`} {
		if w := request(h, "POST", "/sandboxes/sandbox/refreshes", body); w.Code != 400 {
			t.Fatal(body, w.Code)
		}
	}
	if service.calls != calls {
		t.Fatal("invalid refresh reached service")
	}
	store.row.ActorAtespace = "foreign"
	if w := request(h, "POST", "/sandboxes/sandbox/refreshes", "{}"); w.Code != 404 || service.calls != calls {
		t.Fatal("foreign refresh reached service", w.Code)
	}
}

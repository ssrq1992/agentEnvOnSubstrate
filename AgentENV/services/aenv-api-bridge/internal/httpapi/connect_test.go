package httpapi

import (
	"agentenv/services/aenv-api-bridge/internal/auth"
	"agentenv/services/aenv-api-bridge/internal/metadata"
	"context"
	"encoding/json"
	pb "github.com/agent-substrate/substrate/pkg/proto/ateapipb"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

type connectStub struct {
	calls, seconds  int
	legacy, resumed bool
	err             error
}

func (s *connectStub) Connect(_ context.Context, t auth.Tenant, b metadata.Sandbox, seconds int, legacy bool) (*pb.ConnectActorResponse, bool, error) {
	if t.ID != b.Tenant || b.ActorUID != "uid" {
		panic("cross tenant connect")
	}
	s.calls++
	s.seconds = seconds
	s.legacy = legacy
	return &pb.ConnectActorResponse{Actor: &pb.Actor{Metadata: &pb.ResourceMetadata{Uid: b.ActorUID}, ActorTemplate: &pb.ObjectRef{Name: "template"}}, EnvdAccessToken: "test-secret", EnvdVersion: "0.5.1"}, s.resumed, s.err
}
func connectFixture(t *testing.T) (http.Handler, *connectStub, *storeStub) {
	t.Helper()
	_, st, c := setup(t)
	connector := &connectStub{}
	h, err := (&Server{Auth: authStub{}, Store: st, Control: c, Connections: connector, SandboxDomain: "sandbox.example.com", RequestTimeout: time.Second}).Handler()
	if err != nil {
		t.Fatal(err)
	}
	return h, connector, st
}
func TestConnectVersionDefaultsAndResponse(t *testing.T) {
	h, c, _ := connectFixture(t)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("POST", "/v2/sandboxes/sandbox/connect", nil))
	if w.Code != 200 || c.seconds != 300 || c.legacy {
		t.Fatal("v2 default", w.Code, c)
	}
	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	for key, value := range map[string]string{"sandboxID": "sandbox", "templateID": "template", "clientID": "", "envdVersion": "0.5.1", "envdAccessToken": "test-secret", "domain": "sandbox.example.com"} {
		if body[key] != value {
			t.Fatal("wrong SDK field", key)
		}
	}
	if w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("credential response is cacheable")
	}
	c.resumed = true
	if w = request(h, "POST", "/sandboxes/sandbox/connect", `{"timeout":0}`); w.Code != 201 || c.seconds != 0 {
		t.Fatal("legacy connect minimum", w.Code)
	}
	if w = request(h, "POST", "/sandboxes/sandbox/resume", `{}`); w.Code != 201 || c.seconds != 15 || !c.legacy {
		t.Fatal("legacy resume default", w.Code)
	}
}
func TestConnectRejectsMalformedRequestsAndUnconfirmedResume(t *testing.T) {
	h, c, st := connectFixture(t)
	for _, tc := range []struct{ path, body string }{{"/sandboxes/sandbox/connect", `{}`}, {"/v2/sandboxes/sandbox/connect", `{"timeout":0}`}, {"/v2/sandboxes/sandbox/connect", `null`}, {"/v2/sandboxes/sandbox/connect", `{"timeout":4294967296}`}, {"/sandboxes/sandbox/connect", `{"timeout":300} {}`}} {
		if w := request(h, "POST", tc.path, tc.body); w.Code != 400 {
			t.Fatal("invalid request reached lifecycle", tc, w.Code)
		}
	}
	if c.calls != 0 {
		t.Fatal("invalid input mutated Actor")
	}
	c.err = context.DeadlineExceeded
	if w := request(h, "POST", "/v2/sandboxes/sandbox/connect", `{}`); w.Code != 504 {
		t.Fatal("unknown resume acknowledged", w.Code)
	}
	st.row.ActorAtespace = "other"
	if w := request(h, "POST", "/v2/sandboxes/sandbox/connect", `{}`); w.Code != 404 || c.calls != 1 {
		t.Fatal("cross tenant connect")
	}
}

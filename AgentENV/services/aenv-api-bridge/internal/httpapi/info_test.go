package httpapi

import (
	"agentenv/services/aenv-api-bridge/internal/auth"
	"agentenv/services/aenv-api-bridge/internal/metadata"
	"context"
	"encoding/json"
	pb "github.com/agent-substrate/substrate/pkg/proto/ateapipb"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

type infoFixture struct {
	rows   []metadata.Sandbox
	states map[string]pb.ActorState
	calls  int
}

func (f *infoFixture) ListPage(_ context.Context, tenant, after string, limit int) ([]metadata.Sandbox, error) {
	if tenant != "tenant" {
		panic("wrong tenant")
	}
	out := []metadata.Sandbox{}
	for _, b := range f.rows {
		if b.ExternalID > after {
			out = append(out, b)
		}
	}
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}
func (f *infoFixture) Profile(_ context.Context, b metadata.Sandbox) (metadata.Profile, error) {
	return metadata.Profile{TemplateID: "template", Alias: "python", EnvdVersion: "version", CPUCount: 1, MemoryMB: 128, DiskSizeMB: 1024, Metadata: map[string]string{"user": "me"}}, nil
}
func (f *infoFixture) Access(context.Context, metadata.Sandbox) (metadata.Access, error) {
	return metadata.Access{AutoPause: true, AutoResume: true, EnvdPort: 49983}, nil
}
func (f *infoFixture) Get(_ context.Context, t auth.Tenant, b metadata.Sandbox) (*pb.Actor, error) {
	f.calls++
	if b.Tenant != t.ID || b.ActorAtespace != t.Atespace {
		panic("tenant escape")
	}
	return &pb.Actor{Metadata: &pb.ResourceMetadata{Uid: b.ActorUID}, Status: &pb.ActorStatus{State: f.states[b.ExternalID], AgentenvPolicyDelivery: &pb.AgentENVPolicyDelivery{Policy: &pb.AgentENVNetworkPolicy{Base: pb.AgentENVNetworkPolicy_DENY}}}}, nil
}
func infoSetup(t *testing.T) (http.Handler, *infoFixture) {
	t.Helper()
	f := &infoFixture{states: map[string]pb.ActorState{}}
	for i, id := range []string{"a", "b", "c"} {
		f.rows = append(f.rows, metadata.Sandbox{Tenant: "tenant", ExternalID: id, ActorAtespace: "space", ActorUID: "uid-" + id, CreatedAt: time.Unix(int64(i+1), 0), ExpiresAt: time.Unix(300, 0)})
		f.states[id] = pb.ActorState_ACTOR_STATE_RUNNING
	}
	f.states["b"] = pb.ActorState_ACTOR_STATE_SUSPENDED
	store := &storeStub{row: f.rows[0]}
	h, err := (&Server{Auth: authStub{}, Store: store, Control: &controlStub{}, Infos: f, InfoControl: f, Profiles: f, RequestTimeout: time.Second, SandboxDomain: "sandbox.example"}).Handler()
	if err != nil {
		t.Fatal(err)
	}
	return h, f
}
func TestSDKListStateAndCursor(t *testing.T) {
	h, _ := infoSetup(t)
	r := request(h, "GET", "/sandboxes", "")
	var legacy []sandboxInfo
	if err := json.Unmarshal(r.Body.Bytes(), &legacy); err != nil || len(legacy) != 2 {
		t.Fatalf("legacy %s", r.Body.String())
	}
	r = request(h, "GET", "/v2/sandboxes?limit=1", "")
	var page []sandboxInfo
	_ = json.Unmarshal(r.Body.Bytes(), &page)
	if len(page) != 1 || page[0].SandboxID != "c" || r.Header().Get("X-Total-Running") != "2" {
		t.Fatalf("page %s", r.Body.String())
	}
	token := r.Header().Get("X-Next-Token")
	if token == "" {
		t.Fatal("cursor missing")
	}
	r = request(h, "GET", "/v2/sandboxes?limit=1&nextToken="+url.QueryEscape(token), "")
	_ = json.Unmarshal(r.Body.Bytes(), &page)
	if len(page) != 1 || page[0].SandboxID != "b" {
		t.Fatal("cursor ordering failed", r.Body.String())
	}
	r = request(h, "GET", "/v2/sandboxes?limit=1&order=asc&nextToken="+url.QueryEscape(token), "")
	if r.Code != 400 {
		t.Fatal("cursor reused with changed order")
	}
	r = request(h, "GET", "/v2/sandboxes?state=paused", "")
	_ = json.Unmarshal(r.Body.Bytes(), &page)
	if len(page) != 1 || page[0].SandboxID != "b" || r.Header().Get("X-Total-Running") != "" {
		t.Fatal("paused filter failed")
	}
}
func TestSDKListValidationAndMetadata(t *testing.T) {
	h, f := infoSetup(t)
	for _, query := range []string{"limit=0", "limit=101", "order=other", "state=unknown", "nextToken=invalid", "startedAfter=invalid"} {
		before := f.calls
		r := request(h, "GET", "/v2/sandboxes?"+query, "")
		if r.Code != 400 || before != f.calls {
			t.Fatalf("accepted %s", query)
		}
	}
	r := request(h, "GET", "/v2/sandboxes?metadata="+url.QueryEscape("user=other"), "")
	if strings.TrimSpace(r.Body.String()) != "[]" {
		t.Fatal("metadata filter ignored")
	}
}
func TestSDKInfoAuthoritativeStateAndLifecycle(t *testing.T) {
	h, f := infoSetup(t)
	f.states["a"] = pb.ActorState_ACTOR_STATE_SUSPENDED
	r := request(h, "GET", "/sandboxes/a", "")
	if r.Code != 200 {
		t.Fatal(r.Body.String())
	}
	var result sandboxInfo
	if err := json.Unmarshal(r.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.State != "paused" || result.Lifecycle.OnTimeout != "pause" || !result.Lifecycle.AutoResume || result.AllowInternetAccess == nil || *result.AllowInternetAccess {
		t.Fatal("incorrect runtime projection", result)
	}
}
func TestListRejectsTenantMappingBeforeControl(t *testing.T) {
	h, f := infoSetup(t)
	f.rows[0].ActorAtespace = "other"
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/v2/sandboxes", nil))
	if w.Code != 503 || f.calls != 0 {
		t.Fatal("list crossed tenant boundary")
	}
}

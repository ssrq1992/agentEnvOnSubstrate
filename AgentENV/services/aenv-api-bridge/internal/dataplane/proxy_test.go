package dataplane

import (
	"agentenv/services/aenv-api-bridge/internal/auth"
	"agentenv/services/aenv-api-bridge/internal/metadata"
	"bytes"
	"context"
	pb "github.com/agent-substrate/substrate/pkg/proto/ateapipb"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

type fixture struct {
	b                          metadata.Sandbox
	a                          metadata.Access
	state                      pb.ActorState
	trusted                    bool
	lookups, resumes, connects int
}

func (f *fixture) LookupExternal(context.Context, string) (metadata.Sandbox, error) {
	f.lookups++
	return f.b, nil
}
func (f *fixture) Access(context.Context, metadata.Sandbox) (metadata.Access, error) { return f.a, nil }
func (f *fixture) TrustedGateway(*http.Request) bool                                 { return f.trusted }
func (f *fixture) Get(context.Context, auth.Tenant, metadata.Sandbox) (*pb.Actor, error) {
	return f.actor(), nil
}
func (f *fixture) actor() *pb.Actor {
	return &pb.Actor{Metadata: &pb.ResourceMetadata{Uid: f.b.ActorUID}, Status: &pb.ActorStatus{State: f.state, WorkerAssignment: &pb.WorkerAssignment{AssignmentGeneration: 7}}}
}
func (f *fixture) Connect(context.Context, auth.Tenant, metadata.Sandbox) (*pb.ConnectActorResponse, error) {
	f.connects++
	return &pb.ConnectActorResponse{Actor: f.actor(), EnvdAccessToken: "runtime-secret"}, nil
}

type wake struct{ f *fixture }

func (w wake) Connect(ctx context.Context, t auth.Tenant, b metadata.Sandbox, _ int, _ bool) (*pb.ConnectActorResponse, bool, error) {
	w.f.resumes++
	w.f.state = pb.ActorState_ACTOR_STATE_RUNNING
	r, e := w.f.Connect(ctx, t, b)
	return r, true, e
}

type transportFunc func(*http.Request) (*http.Response, error)

func (f transportFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func setup(t *testing.T) (*Handler, *fixture) {
	t.Helper()
	f := &fixture{b: metadata.Sandbox{Tenant: "tenant", ExternalID: "sandbox", ActorAtespace: "space", ActorName: "actor", ActorUID: "uid"}, a: metadata.Access{Secure: true, EnvdPort: 49983}, state: pb.ActorState_ACTOR_STATE_RUNNING, trusted: true}
	tokens, _ := NewTokens(bytes.Repeat([]byte{1}, 32))
	target, _ := url.Parse("https://ingress.internal")
	h := &Handler{Domain: "sandbox.example", Target: target, Store: f, Control: f, Lifecycle: wake{f}, Gateway: f, Tokens: tokens, Tenants: map[string]auth.Tenant{"tenant": {ID: "tenant", Atespace: "space"}}, AutoResumeTimeout: 300}
	h.Transport = transportFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader("response"))}, nil
	})
	if err := h.Validate(); err != nil {
		t.Fatal(err)
	}
	return h, f
}
func TestCredentialScope(t *testing.T) {
	h, f := setup(t)
	token := h.Tokens.Token(f.b, "envd")
	if !h.Tokens.Valid(f.b, "envd", token) || h.Tokens.Valid(f.b, "traffic", token) {
		t.Fatal("purpose separation failed")
	}
	other := f.b
	other.Tenant = "other"
	if h.Tokens.Valid(other, "envd", token) {
		t.Fatal("cross tenant token")
	}
	other = f.b
	other.ActorUID = "new"
	if h.Tokens.Valid(other, "envd", token) {
		t.Fatal("recreated actor token")
	}
}
func TestAuthenticateBeforeWake(t *testing.T) {
	for _, tc := range []struct {
		name                 string
		trusted, token, auto bool
		want                 int
		wake                 int
	}{
		{"untrusted gateway", false, true, true, 401, 0}, {"invalid credential", true, false, true, 401, 0}, {"paused disabled", true, true, false, 409, 0}, {"authorized wake", true, true, true, 200, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, f := setup(t)
			f.trusted = tc.trusted
			f.state = pb.ActorState_ACTOR_STATE_SUSPENDED
			f.a.AutoResume = tc.auto
			r := httptest.NewRequest("GET", "https://49983-sandbox.sandbox.example/files", nil)
			if tc.token {
				r.Header.Set("X-Access-Token", h.Tokens.Token(f.b, "envd"))
			}
			w := httptest.NewRecorder()
			if !h.Handle(w, r) || w.Code != tc.want || f.resumes != tc.wake {
				t.Fatalf("status=%d wake=%d", w.Code, f.resumes)
			}
			if !tc.trusted && f.lookups != 0 {
				t.Fatal("lookup before gateway authentication")
			}
		})
	}
}
func TestProxyCredentialsAndFences(t *testing.T) {
	for _, port := range []string{"49983", "8080"} {
		t.Run(port, func(t *testing.T) {
			h, f := setup(t)
			called := false
			h.Transport = transportFunc(func(r *http.Request) (*http.Response, error) {
				called = true
				if r.URL.Host != "ingress.internal" || r.URL.EscapedPath() != "/files/%2Ftmp%2Fa" || r.URL.RawQuery != "x=a%2Bb" {
					t.Fatalf("incorrect route %s", r.URL)
				}
				for k, want := range map[string]string{"Ate-Target-Actor": "space/actor", "Ate-Target-Actor-Uid": "uid", "Ate-Target-Assignment-Generation": "7", "X-Ate-Target-Port": port} {
					if r.Header.Get(k) != want {
						t.Fatalf("incorrect %s", k)
					}
				}
				for _, k := range []string{"X-API-Key", "E2b-Traffic-Access-Token", "X-Agentenv-Tenant", "E2b-Sandbox-Id"} {
					if r.Header.Get(k) != "" {
						t.Fatalf("leaked %s", k)
					}
				}
				want := ""
				if port == "49983" {
					want = "runtime-secret"
				}
				if r.Header.Get("X-Access-Token") != want {
					t.Fatal("envd token leaked or missing")
				}
				return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader("stream"))}, nil
			})
			r := httptest.NewRequest("POST", "https://"+port+"-sandbox.sandbox.example/files/%2Ftmp%2Fa?x=a%2Bb", strings.NewReader("input"))
			for _, k := range []string{"X-API-Key", "X-Agentenv-Tenant", "E2b-Sandbox-Id", "Ate-Target-Actor", "Ate-Target-Actor-Uid", "Ate-Target-Assignment-Generation", "X-Ate-Target-Port"} {
				r.Header.Set(k, "forged")
			}
			r.Header.Set("X-Access-Token", h.Tokens.Token(f.b, "envd"))
			r.Header.Set("E2b-Traffic-Access-Token", h.Tokens.Token(f.b, "traffic"))
			w := httptest.NewRecorder()
			h.Handle(w, r)
			if !called || w.Code != 200 || w.Body.String() != "stream" {
				t.Fatalf("proxy failed %d", w.Code)
			}
		})
	}
}
func TestDataRouting(t *testing.T) {
	for _, tc := range []struct {
		host, path       string
		matched, invalid bool
	}{
		{"49983-sandbox.sandbox.example", "/files", true, false}, {"0-sandbox.sandbox.example", "/", true, true}, {"65536-sandbox.sandbox.example", "/", true, true}, {"49983-sandbox.sandbox.example.evil", "/", false, false}, {"api.example", "/sandboxes", false, false}, {"bad.sandbox.example", "/", true, true},
	} {
		r := httptest.NewRequest("GET", "https://"+tc.host+tc.path, nil)
		_, _, matched, err := routing(r, "sandbox.example")
		if matched != tc.matched || (err != nil) != tc.invalid {
			t.Fatalf("host %s matched=%v err=%v", tc.host, matched, err)
		}
	}
}
func TestDuplicateTokenRejected(t *testing.T) {
	h, f := setup(t)
	r := httptest.NewRequest("GET", "https://49983-sandbox.sandbox.example/", nil)
	v := h.Tokens.Token(f.b, "envd")
	r.Header.Add("X-Access-Token", v)
	r.Header.Add("X-Access-Token", v)
	w := httptest.NewRecorder()
	h.Handle(w, r)
	if w.Code != 401 || f.connects != 0 {
		t.Fatal("duplicate credential accepted")
	}
}

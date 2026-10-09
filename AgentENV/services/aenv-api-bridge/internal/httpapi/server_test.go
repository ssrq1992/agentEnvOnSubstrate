package httpapi

import (
	"agentenv/services/aenv-api-bridge/internal/auth"
	"agentenv/services/aenv-api-bridge/internal/metadata"
	"context"
	"errors"
	pb "github.com/agent-substrate/substrate/pkg/proto/ateapipb"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type authStub struct{ fail bool }

func (a authStub) Authenticate(*http.Request) (auth.Tenant, error) {
	if a.fail {
		return auth.Tenant{}, errors.New("secret")
	}
	return auth.Tenant{ID: "tenant", Atespace: "space"}, nil
}

type storeStub struct {
	row                           metadata.Sandbox
	reserved, confirmed, extended int
	intent                        metadata.Request
	conflict                      bool
	receiptState                  string
}

func (s *storeStub) Get(context.Context, string, string) (metadata.Sandbox, error) { return s.row, nil }
func (s *storeStub) Reserve(_ context.Context, r metadata.Request) (metadata.Request, error) {
	s.reserved++
	s.intent = r
	r.State = "pending"
	if s.receiptState != "" {
		r.State = s.receiptState
	}
	return r, nil
}
func (s *storeStub) ConfirmDeleted(context.Context, metadata.Request) error {
	s.confirmed++
	return nil
}
func (s *storeStub) Extend(_ context.Context, tenant, id string, revision int64, seconds int) error {
	if tenant != "tenant" || id != "sandbox" || revision != 4 || seconds != 300 {
		panic("wrong extension")
	}
	if s.conflict {
		return metadata.ErrConflict
	}
	s.extended++
	return nil
}

type controlStub struct {
	err    error
	calls  int
	policy *pb.AgentENVNetworkPolicy
}

func (c *controlStub) Delete(context.Context, auth.Tenant, metadata.Sandbox) error {
	c.calls++
	return c.err
}
func setup(t *testing.T) (http.Handler, *storeStub, *controlStub) {
	t.Helper()
	s := &storeStub{row: metadata.Sandbox{Tenant: "tenant", ExternalID: "sandbox", ActorAtespace: "space", ActorName: "actor", ActorUID: "uid", Revision: 4}}
	c := &controlStub{}
	h, err := (&Server{Auth: authStub{}, Store: s, Control: c, RequestTimeout: time.Second}).Handler()
	if err != nil {
		t.Fatal(err)
	}
	return h, s, c
}
func request(h http.Handler, method, path, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Idempotency-Key", "stable")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}
func TestDeleteRequiresConfirmedControlResult(t *testing.T) {
	h, s, c := setup(t)
	c.err = context.DeadlineExceeded
	w := request(h, "DELETE", "/sandboxes/sandbox", "")
	if w.Code != 504 || s.confirmed != 0 || s.reserved != 1 {
		t.Fatalf("unknown deletion acknowledged: %d %+v", w.Code, s)
	}
	first := s.intent
	c.err = nil
	w = request(h, "DELETE", "/sandboxes/sandbox", "")
	if w.Code != 204 || s.confirmed != 1 || s.intent.ID != first.ID || s.intent.Digest != first.Digest {
		t.Fatalf("retry lost intent: %d %+v", w.Code, s)
	}
}
func TestTenantMismatchNeverMutates(t *testing.T) {
	h, s, c := setup(t)
	s.row.ActorAtespace = "other"
	w := request(h, "DELETE", "/sandboxes/sandbox", "")
	if w.Code != 404 || c.calls != 0 || s.reserved != 0 {
		t.Fatal("tenant mismatch reached mutation")
	}
}
func TestTimeoutValidationAndExpiryConflict(t *testing.T) {
	h, s, _ := setup(t)
	for _, body := range []string{`{}`, `{"timeout":-1}`, `{"timeout":4294967296}`, `{"timeout":1.5}`, `{"timeout":300} {}`} {
		w := request(h, "POST", "/sandboxes/sandbox/timeout", body)
		if w.Code != 400 {
			t.Fatalf("accepted %s: %d", body, w.Code)
		}
	}
	if s.extended != 0 {
		t.Fatal("invalid body mutated timeout")
	}
	s.conflict = true
	if w := request(h, "POST", "/sandboxes/sandbox/timeout", `{"timeout":300}`); w.Code != 409 {
		t.Fatal(w.Code)
	}
	s.conflict = false
	if w := request(h, "POST", "/sandboxes/sandbox/timeout", `{"timeout":300}`); w.Code != 204 || s.extended != 1 {
		t.Fatal(w.Code)
	}
}
func TestAuthenticationBeforeRoutes(t *testing.T) {
	_, s, c := setup(t)
	h, err := (&Server{Auth: authStub{fail: true}, Store: s, Control: c, RequestTimeout: time.Second}).Handler()
	if err != nil {
		t.Fatal(err)
	}
	w := request(h, "DELETE", "/sandboxes/sandbox", "")
	if w.Code != 401 || strings.Contains(w.Body.String(), "secret") || s.reserved != 0 {
		t.Fatal("authentication failure leaked or mutated")
	}
	w = request(h, "GET", "/unknown", "")
	if w.Code != 401 {
		t.Fatal("unknown route bypassed authentication")
	}
}

func (s *storeStub) Complete(context.Context, metadata.Request, bool, []byte) error {
	s.confirmed++
	return nil
}
func (c *controlStub) ReplaceNetworkPolicy(_ context.Context, _ auth.Tenant, _ metadata.Sandbox, p *pb.AgentENVNetworkPolicy, _ uint64) error {
	c.policy = p
	c.calls++
	return c.err
}
func TestNetworkUpdateRequiresExecutionACK(t *testing.T) {
	h, s, c := setup(t)
	for _, body := range []string{"null", "[]", "{} {}", `{"allow_internet_access":"false"}`} {
		if w := request(h, "PUT", "/sandboxes/sandbox/network", body); w.Code != 400 {
			t.Fatalf("accepted %s: %d", body, w.Code)
		}
	}
	if c.calls != 0 || s.reserved != 0 {
		t.Fatal("invalid request mutated policy")
	}
	c.err = context.DeadlineExceeded
	if w := request(h, "PUT", "/sandboxes/sandbox/network", `{"allow_internet_access":false}`); w.Code != 504 || s.confirmed != 0 {
		t.Fatal("unknown execution acknowledged")
	}
	first := s.intent
	c.err = nil
	if w := request(h, "PUT", "/sandboxes/sandbox/network", `{"allow_internet_access":false}`); w.Code != 204 || s.confirmed != 1 || s.intent.Digest != first.Digest {
		t.Fatal("retry lost identity")
	}
}

func TestNetworkDefaultAndCompletedReplay(t *testing.T) {
	h, s, c := setup(t)
	cases := []struct {
		body string
		base pb.AgentENVNetworkPolicy_Base
	}{
		{`{}`, pb.AgentENVNetworkPolicy_DEFAULT},
		{`{"allow_internet_access":true}`, pb.AgentENVNetworkPolicy_ALLOW},
		{`{"allow_internet_access":false}`, pb.AgentENVNetworkPolicy_DENY},
	}
	for _, tc := range cases {
		if w := request(h, "PUT", "/sandboxes/sandbox/network", tc.body); w.Code != 204 || c.policy.GetBase() != tc.base {
			t.Fatalf("wrong base: %s %d %v", tc.body, w.Code, c.policy)
		}
	}
	before := c.calls
	s.receiptState = "completed"
	if w := request(h, "PUT", "/sandboxes/sandbox/network", `{}`); w.Code != 204 || c.calls != before {
		t.Fatal("completed request reapplied historical policy")
	}
	s.receiptState = ""
	s.row.ActorAtespace = "other"
	if w := request(h, "PUT", "/sandboxes/sandbox/network", `{}`); w.Code != 404 || c.calls != before {
		t.Fatal("cross tenant policy mutation")
	}
}

func (s *storeStub) Prepare(_ context.Context, r metadata.Request, prepared []byte) (metadata.Request, error) {
	r.State = "pending"
	r.Result = prepared
	return r, nil
}
func (c *controlStub) NetworkPolicyRevision(context.Context, auth.Tenant, metadata.Sandbox) (uint64, error) {
	return 0, nil
}

package httpapi

import (
	"agentenv/services/aenv-api-bridge/internal/auth"
	extensionservice "agentenv/services/aenv-api-bridge/internal/extensions"
	"agentenv/services/aenv-api-bridge/internal/metadata"
	"context"
	"encoding/json"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"net/http"
	"strings"
	"testing"
	"time"
)

type extensionStub struct {
	patches int
	patch   json.RawMessage
	key     string
	err     error
}

func (s *extensionStub) Get(_ context.Context, t auth.Tenant, b metadata.Sandbox) (json.RawMessage, error) {
	if t.ID != b.Tenant {
		panic("tenant escape")
	}
	return json.RawMessage(`{"full":true}`), s.err
}
func (s *extensionStub) Patch(_ context.Context, t auth.Tenant, b metadata.Sandbox, p json.RawMessage, key string) (json.RawMessage, error) {
	if t.ID != b.Tenant {
		panic("tenant escape")
	}
	s.patches++
	s.patch = p
	s.key = key
	return json.RawMessage(`{"approved":true}`), s.err
}
func TestExtensionRoutesReturnFullApprovedObjects(t *testing.T) {
	ext := &extensionStub{}
	h, err := (&Server{Extensions: ext, Auth: authStub{}, Store: &storeStub{row: metadata.Sandbox{Tenant: "tenant", ExternalID: "sandbox", ActorAtespace: "space", ActorName: "actor", ActorUID: "uid"}}, Control: &controlStub{}, RequestTimeout: time.Second}).Handler()
	if err != nil {
		t.Fatal(err)
	}
	got := request(h, http.MethodGet, "/sandboxes/sandbox/custom-extension-params", "")
	if got.Code != 200 || got.Body.String() != `{"full":true}` {
		t.Fatal(got.Code, got.Body.String())
	}
	got = request(h, http.MethodPatch, "/sandboxes/sandbox/custom-extension-params", `{"desired":9007199254740993}`)
	if got.Code != 200 || got.Body.String() != `{"approved":true}` || string(ext.patch) != `{"desired":9007199254740993}` {
		t.Fatal(got.Code, got.Body.String(), string(ext.patch))
	}
	for _, body := range []string{`null`, `[]`, `true`, `{} {}`, strings.Repeat("x", 65537)} {
		calls := ext.patches
		got = request(h, http.MethodPatch, "/sandboxes/sandbox/custom-extension-params", body)
		if got.Code != 400 || ext.patches != calls {
			t.Fatal("bad request reached execution", got.Code)
		}
	}
	ext.err = extensionservice.ErrRejected
	if got = request(h, http.MethodPatch, "/sandboxes/sandbox/custom-extension-params", `{}`); got.Code != 400 {
		t.Fatal("known rejection code", got.Code)
	}
	ext.err = status.Error(codes.Unavailable, "unknown hook outcome")
	if got = request(h, http.MethodPatch, "/sandboxes/sandbox/custom-extension-params", `{}`); got.Code != 503 {
		t.Fatal("unknown result reported terminal", got.Code)
	}
}

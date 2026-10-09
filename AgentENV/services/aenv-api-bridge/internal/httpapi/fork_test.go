package httpapi

import (
	"agentenv/services/aenv-api-bridge/internal/auth"
	forkservice "agentenv/services/aenv-api-bridge/internal/fork"
	"agentenv/services/aenv-api-bridge/internal/metadata"
	"context"
	"encoding/json"
	pb "github.com/agent-substrate/substrate/pkg/proto/ateapipb"
	"net/http"
	"strings"
	"testing"
	"time"
)

type forkStub struct {
	outcomes []forkservice.Outcome
	calls    int
	input    forkservice.Input
	key      string
}

func (f *forkStub) Fork(_ context.Context, t auth.Tenant, b metadata.Sandbox, input forkservice.Input, key string) ([]forkservice.Outcome, error) {
	if t.ID != b.Tenant {
		panic("tenant escape")
	}
	f.calls++
	f.input = input
	f.key = key
	return f.outcomes, nil
}
func TestForkRouteReturnsIndependentSDKResults(t *testing.T) {
	f := &infoFixture{states: map[string]pb.ActorState{"parent": pb.ActorState_ACTOR_STATE_RUNNING, "child": pb.ActorState_ACTOR_STATE_RUNNING}}
	parent := metadata.Sandbox{Tenant: "tenant", ExternalID: "parent", ActorAtespace: "space", ActorUID: "parent-uid"}
	child := metadata.Sandbox{Tenant: "tenant", ExternalID: "child", ActorAtespace: "space", ActorUID: "child-uid", CreatedAt: time.Unix(1, 0), ExpiresAt: time.Unix(301, 0)}
	forks := &forkStub{outcomes: []forkservice.Outcome{{Sandbox: &child}, {Error: "child failed"}}}
	h, err := (&Server{Auth: authStub{}, Store: &storeStub{row: parent}, Control: &controlStub{}, Forks: forks, Infos: f, InfoControl: f, Profiles: f, RequestTimeout: time.Second, SandboxDomain: "sandbox.example"}).Handler()
	if err != nil {
		t.Fatal(err)
	}
	response := request(h, http.MethodPost, "/sandboxes/parent/fork", `{"count":2,"timeout":300}`)
	if response.Code != 201 {
		t.Fatal(response.Code, response.Body.String())
	}
	var outcomes []struct {
		Sandbox *sandboxInfo `json:"sandbox"`
		Error   *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &outcomes); err != nil {
		t.Fatal(err)
	}
	if len(outcomes) != 2 || outcomes[0].Sandbox == nil || outcomes[0].Sandbox.SandboxID != "child" || outcomes[0].Sandbox.TemplateID != "template" || outcomes[1].Error == nil || outcomes[1].Error.Message != "child failed" {
		t.Fatal(response.Body.String())
	}
	for _, body := range []string{`{"unsupported":true}`, `{"count":-1}`, `{} {}`, strings.Repeat("x", 5000)} {
		before := forks.calls
		response = request(h, http.MethodPost, "/sandboxes/parent/fork", body)
		if response.Code != 400 || forks.calls != before {
			t.Fatalf("invalid request reached fork: %s %d", body, response.Code)
		}
	}
}

package extensions

import (
	"agentenv/services/aenv-api-bridge/internal/auth"
	"agentenv/services/aenv-api-bridge/internal/metadata"
	"context"
	"encoding/json"
	"errors"
	pb "github.com/agent-substrate/substrate/pkg/proto/ateapipb"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"testing"
)

type testStore struct {
	row         metadata.Sandbox
	intent      *metadata.Extension
	completeErr error
	completions int
}

func (s *testStore) LockOperation(context.Context, string, string) (func(), error) {
	return func() {}, nil
}
func (s *testStore) Get(context.Context, string, string) (metadata.Sandbox, error) { return s.row, nil }
func (s *testStore) Extension(_ context.Context, r metadata.Request) (metadata.Extension, error) {
	if s.intent == nil {
		return metadata.Extension{}, metadata.ErrNotFound
	}
	if s.intent.Request.Digest != r.Digest || s.intent.Request.ID != r.ID {
		return metadata.Extension{}, metadata.ErrConflict
	}
	return *s.intent, nil
}
func (s *testStore) ReserveExtension(_ context.Context, b metadata.Sandbox, r metadata.Request, prepared []byte) (metadata.Extension, error) {
	r.State = "pending"
	s.intent = &metadata.Extension{Request: r, Sandbox: b, Prepared: prepared}
	return *s.intent, nil
}
func (s *testStore) ClaimExtensions(context.Context, int) ([]metadata.Extension, error) {
	if s.intent == nil || s.intent.Request.State != "pending" {
		return nil, nil
	}
	return []metadata.Extension{*s.intent}, nil
}
func (s *testStore) Complete(_ context.Context, r metadata.Request, rejected bool, result []byte) error {
	if s.completeErr != nil {
		return s.completeErr
	}
	s.completions++
	s.intent.Request.State = "completed"
	if rejected {
		s.intent.Request.State = "rejected"
	}
	s.intent.Request.Result = result
	return nil
}

type testControl struct {
	generation, revision uint64
	err                  error
	calls                int
	requests             []*pb.UpdateActorExtensionParamsRequest
}

func (c *testControl) Get(context.Context, auth.Tenant, metadata.Sandbox) (*pb.Actor, error) {
	return &pb.Actor{Status: &pb.ActorStatus{State: pb.ActorState_ACTOR_STATE_RUNNING, WorkerAssignment: &pb.WorkerAssignment{AssignmentGeneration: c.generation}}}, nil
}
func (c *testControl) ExtensionParams(context.Context, auth.Tenant, metadata.Sandbox) (*pb.ActorExtensionParams, error) {
	return &pb.ActorExtensionParams{Revision: c.revision, Json: `{"initial":true}`}, nil
}
func (c *testControl) ApplyExtensionParams(_ context.Context, _ auth.Tenant, _ metadata.Sandbox, r *pb.UpdateActorExtensionParamsRequest) (*pb.ActorExtensionParams, error) {
	c.calls++
	c.requests = append(c.requests, proto.CloneOf(r))
	if c.err != nil {
		return nil, c.err
	}
	return &pb.ActorExtensionParams{Revision: r.ExpectedRevision + 1, Json: `{"approved":true}`}, nil
}
func fixture() (*Service, *testStore, *testControl, auth.Tenant, metadata.Sandbox) {
	t := auth.Tenant{ID: "tenant", Atespace: "space"}
	b := metadata.Sandbox{Tenant: t.ID, ExternalID: "id", ActorAtespace: t.Atespace, ActorName: "actor", ActorUID: "actor-uid"}
	st := &testStore{row: b}
	c := &testControl{generation: 7, revision: 2}
	s := &Service{Store: st, Control: c, Tenants: map[string]auth.Tenant{t.ID: t}}
	return s, st, c, t, b
}
func TestRestartReplaysFrozenExtensionAndOnlyApprovedParams(t *testing.T) {
	s, st, c, tnt, b := fixture()
	c.err = status.Error(codes.Unavailable, "unknown")
	if _, err := s.Patch(t.Context(), tnt, b, json.RawMessage(`{"requested":9007199254740993}`), "key"); status.Code(err) != codes.Unavailable {
		t.Fatal(err)
	}
	if st.completions != 0 || st.intent.Request.State != "pending" {
		t.Fatal("unknown completed")
	}
	c.generation = 8
	c.revision = 99
	c.err = nil
	restarted := &Service{Store: st, Control: c, Tenants: s.Tenants}
	if err := restarted.Sweep(t.Context()); err != nil {
		t.Fatal(err)
	}
	if len(c.requests) != 2 || !proto.Equal(c.requests[0], c.requests[1]) || c.requests[1].AssignmentGeneration != 7 || c.requests[1].ExpectedRevision != 2 || c.requests[1].PatchJson != `{"requested":9007199254740993}` {
		t.Fatal("retry changed frozen request", c.requests)
	}
	result, err := restarted.Patch(t.Context(), tnt, b, json.RawMessage(`{"requested":9007199254740993}`), "key")
	if err != nil || string(result) != `{"approved":true}` || c.calls != 2 {
		t.Fatal("lost immutable receipt", string(result), err, c.calls)
	}
	if _, err = restarted.Patch(t.Context(), tnt, b, json.RawMessage(`{"changed":true}`), "key"); !errors.Is(err, metadata.ErrConflict) {
		t.Fatal("idempotency payload changed", err)
	}
}
func TestExtensionCompletionFailureRetainsIntent(t *testing.T) {
	s, st, c, tnt, b := fixture()
	st.completeErr = errors.New("db unavailable")
	if _, err := s.Patch(t.Context(), tnt, b, json.RawMessage(`{}`), "key"); err == nil {
		t.Fatal("lost commit ignored")
	}
	if st.intent.Request.State != "pending" {
		t.Fatal("premature completion")
	}
	st.completeErr = nil
	c.generation = 123
	if err := s.Sweep(t.Context()); err != nil {
		t.Fatal(err)
	}
	if !proto.Equal(c.requests[0], c.requests[1]) {
		t.Fatal("commit retry changed operation")
	}
}
func TestExtensionConfirmedNoEffectAndTenantIsolation(t *testing.T) {
	s, st, c, tnt, b := fixture()
	c.err = ErrRejected
	for i := 0; i < 2; i++ {
		if _, err := s.Patch(t.Context(), tnt, b, json.RawMessage(`{}`), "key"); !errors.Is(err, ErrRejected) {
			t.Fatal(err)
		}
	}
	if c.calls != 1 || st.intent.Request.State != "rejected" {
		t.Fatal("known no effect replayed")
	}
	b.Tenant = "other"
	if _, err := s.Get(t.Context(), tnt, b); status.Code(err) != codes.InvalidArgument {
		t.Fatal("cross tenant read", err)
	}
	for _, patch := range []string{`null`, `[]`, `true`, `{} {}`} {
		if _, err := s.Patch(t.Context(), tnt, st.row, json.RawMessage(patch), "bad"); status.Code(err) != codes.InvalidArgument {
			t.Fatal(patch, err)
		}
	}
}

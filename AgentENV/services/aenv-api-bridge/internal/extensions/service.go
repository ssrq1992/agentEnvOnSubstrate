// Package extensions durably forwards SDK patches to Substrate's runtime owner.
package extensions

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"agentenv/services/aenv-api-bridge/internal/auth"
	"agentenv/services/aenv-api-bridge/internal/metadata"
	pb "github.com/agent-substrate/substrate/pkg/proto/ateapipb"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

var ErrRejected = errors.New("extension patch rejected without effect")

type Store interface {
	LockOperation(context.Context, string, string) (func(), error)
	Get(context.Context, string, string) (metadata.Sandbox, error)
	Extension(context.Context, metadata.Request) (metadata.Extension, error)
	ReserveExtension(context.Context, metadata.Sandbox, metadata.Request, []byte) (metadata.Extension, error)
	ClaimExtensions(context.Context, int) ([]metadata.Extension, error)
	Complete(context.Context, metadata.Request, bool, []byte) error
}
type Control interface {
	Get(context.Context, auth.Tenant, metadata.Sandbox) (*pb.Actor, error)
	ExtensionParams(context.Context, auth.Tenant, metadata.Sandbox) (*pb.ActorExtensionParams, error)
	ApplyExtensionParams(context.Context, auth.Tenant, metadata.Sandbox, *pb.UpdateActorExtensionParamsRequest) (*pb.ActorExtensionParams, error)
}
type Service struct {
	Store   Store
	Control Control
	Tenants map[string]auth.Tenant
}

func validObject(value string) bool {
	if len(value) == 0 || len(value) > 65536 {
		return false
	}
	var obj map[string]json.RawMessage
	return json.Unmarshal([]byte(value), &obj) == nil && obj != nil
}
func validateTenant(t auth.Tenant, b metadata.Sandbox) error {
	if t.ID == "" || t.Atespace == "" || b.Tenant != t.ID || b.ActorAtespace != t.Atespace || b.ActorUID == "" || b.ActorName == "" {
		return status.Error(codes.InvalidArgument, "confirmed tenant Actor identity required")
	}
	return nil
}
func (s *Service) Get(ctx context.Context, t auth.Tenant, b metadata.Sandbox) (json.RawMessage, error) {
	if err := validateTenant(t, b); err != nil {
		return nil, err
	}
	got, err := s.Control.ExtensionParams(ctx, t, b)
	if err != nil {
		return nil, err
	}
	if !validObject(got.GetJson()) {
		return nil, status.Error(codes.Unavailable, "approved extension object unavailable")
	}
	return json.RawMessage(got.Json), nil
}
func (s *Service) Patch(ctx context.Context, t auth.Tenant, b metadata.Sandbox, patch json.RawMessage, key string) (json.RawMessage, error) {
	if err := validateTenant(t, b); err != nil {
		return nil, err
	}
	if !validObject(string(patch)) || len(key) > 200 {
		return nil, status.Error(codes.InvalidArgument, "JSON extension object and valid idempotency key required")
	}
	if key == "" {
		var token [16]byte
		if _, err := rand.Read(token[:]); err != nil {
			return nil, err
		}
		key = hex.EncodeToString(token[:])
	}
	// Canonicalize object member order for equivalent SDK retry payloads without
	// converting JSON numbers to floating point or merging the requested patch.
	var obj map[string]json.RawMessage
	_ = json.Unmarshal(patch, &obj)
	canonical, err := json.Marshal(obj)
	if err != nil {
		return nil, err
	}
	payload, _ := json.Marshal(struct {
		Tenant, ID, UID string
		Patch           json.RawMessage
	}{t.ID, b.ExternalID, b.ActorUID, canonical})
	digest := sha256.Sum256(payload)
	operation := sha256.Sum256([]byte(t.ID + "\x00" + b.ExternalID + "\x00" + key))
	r := metadata.Request{Tenant: t.ID, ExternalID: b.ExternalID, ID: "extension-" + hex.EncodeToString(operation[:]), Kind: "extension", Digest: hex.EncodeToString(digest[:])}
	unlock, err := s.Store.LockOperation(ctx, t.ID, b.ExternalID)
	if err != nil {
		return nil, err
	}
	defer unlock()
	i, err := s.Store.Extension(ctx, r)
	if errors.Is(err, metadata.ErrNotFound) {
		current, e := s.Store.Get(ctx, t.ID, b.ExternalID)
		if e != nil {
			return nil, e
		}
		if current.ActorUID != b.ActorUID || current.ActorAtespace != b.ActorAtespace || current.ActorName != b.ActorName {
			return nil, metadata.ErrConflict
		}
		params, e := s.Control.ExtensionParams(ctx, t, b)
		if e != nil {
			return nil, e
		}
		actor, e := s.Control.Get(ctx, t, b)
		if e != nil {
			return nil, e
		}
		if actor.GetStatus().GetState() != pb.ActorState_ACTOR_STATE_RUNNING || actor.GetStatus().GetWorkerAssignment().GetAssignmentGeneration() == 0 {
			return nil, status.Error(codes.FailedPrecondition, "extension patch requires a running sandbox")
		}
		req := &pb.UpdateActorExtensionParamsRequest{Actor: &pb.ObjectRef{Atespace: b.ActorAtespace, Name: b.ActorName}, Uid: b.ActorUID, AssignmentGeneration: actor.Status.WorkerAssignment.AssignmentGeneration, OperationId: r.ID, ExpectedRevision: params.GetRevision(), PatchJson: string(canonical)}
		prepared, e := proto.MarshalOptions{Deterministic: true}.Marshal(req)
		if e != nil {
			return nil, e
		}
		i, err = s.Store.ReserveExtension(ctx, b, r, prepared)
	}
	if err != nil {
		return nil, err
	}
	return s.executeLocked(ctx, t, i)
}
func (s *Service) executeLocked(ctx context.Context, t auth.Tenant, i metadata.Extension) (json.RawMessage, error) {
	if err := validateTenant(t, i.Sandbox); err != nil {
		return nil, err
	}
	// Reread the committed receipt after acquiring the sandbox operation lock.
	fresh, err := s.Store.Extension(ctx, i.Request)
	if err != nil {
		return nil, err
	}
	i = fresh
	if i.Request.State == "completed" {
		if !validObject(string(i.Request.Result)) {
			return nil, status.Error(codes.Unavailable, "extension receipt unavailable")
		}
		return json.RawMessage(i.Request.Result), nil
	}
	if i.Request.State == "rejected" {
		return nil, ErrRejected
	}
	if i.Request.State != "pending" {
		return nil, metadata.ErrConflict
	}
	current, err := s.Store.Get(ctx, t.ID, i.Sandbox.ExternalID)
	if err != nil {
		return nil, err
	}
	if current.ActorUID != i.Sandbox.ActorUID || current.ActorName != i.Sandbox.ActorName || current.ActorAtespace != i.Sandbox.ActorAtespace {
		return nil, metadata.ErrConflict
	}
	var req pb.UpdateActorExtensionParamsRequest
	if err = proto.Unmarshal(i.Prepared, &req); err != nil || req.Uid != i.Sandbox.ActorUID || req.GetActor().GetAtespace() != t.Atespace || req.GetActor().GetName() != i.Sandbox.ActorName || req.OperationId != i.Request.ID || req.AssignmentGeneration == 0 || !validObject(req.PatchJson) {
		return nil, status.Error(codes.Unavailable, "frozen extension request unavailable")
	}
	got, err := s.Control.ApplyExtensionParams(ctx, t, i.Sandbox, &req)
	if errors.Is(err, ErrRejected) {
		if e := s.Store.Complete(ctx, i.Request, true, []byte(`{}`)); e != nil {
			return nil, e
		}
		return nil, ErrRejected
	}
	if err != nil {
		return nil, err
	} // Unknown RPC results never become rejection receipts.
	if !validObject(got.GetJson()) || got.GetRevision() != req.ExpectedRevision+1 {
		return nil, status.Error(codes.Unavailable, "extension approval not confirmed")
	}
	if err = s.Store.Complete(ctx, i.Request, false, []byte(got.Json)); err != nil {
		return nil, err
	}
	return json.RawMessage(got.Json), nil
}
func (s *Service) Sweep(ctx context.Context) error {
	claims, err := s.Store.ClaimExtensions(ctx, 10)
	if err != nil {
		return err
	}
	var failures []error
	for _, i := range claims {
		t, ok := s.Tenants[i.Request.Tenant]
		if !ok {
			failures = append(failures, fmt.Errorf("extension tenant configuration unavailable"))
			continue
		}
		call, cancel := context.WithTimeout(ctx, 2*time.Minute)
		unlock, e := s.Store.LockOperation(call, i.Request.Tenant, i.Request.ExternalID)
		if e == nil {
			_, e = s.executeLocked(call, t, i)
			unlock()
		}
		cancel()
		if e != nil && !errors.Is(e, ErrRejected) {
			failures = append(failures, e)
		}
	}
	return errors.Join(failures...)
}
func (s *Service) Run(ctx context.Context, onError func(error)) error {
	timer := time.NewTicker(5 * time.Second)
	defer timer.Stop()
	for {
		if err := s.Sweep(ctx); err != nil && ctx.Err() == nil && onError != nil {
			onError(err)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
		}
	}
}

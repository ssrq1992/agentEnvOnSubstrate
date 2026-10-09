// Package fork coordinates one durable capture and independently owned children.
package fork

import (
	"agentenv/services/aenv-api-bridge/internal/auth"
	"agentenv/services/aenv-api-bridge/internal/metadata"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	pb "github.com/agent-substrate/substrate/pkg/proto/ateapipb"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"time"
)

type Input struct {
	Count   *uint32 `json:"count,omitempty"`
	Timeout *uint32 `json:"timeout,omitempty"`
}
type Outcome struct {
	Sandbox *metadata.Sandbox
	Error   string
}
type Store interface {
	LockOperation(context.Context, string, string) (func(), error)
	ReserveFork(context.Context, metadata.Sandbox, metadata.Request, []byte) (metadata.Fork, error)
	Fork(context.Context, metadata.Request) (metadata.Fork, error)
	ConfirmForkCaptured(context.Context, metadata.Fork, []byte) error
	ForkChildren(context.Context, metadata.Fork) ([]metadata.ForkChild, error)
	RecordForkChild(context.Context, metadata.Fork, metadata.ForkChild) error
	ClaimForks(context.Context, int) ([]metadata.Fork, error)
	Complete(context.Context, metadata.Request, bool, []byte) error
	Get(context.Context, string, string) (metadata.Sandbox, error)
	Profile(context.Context, metadata.Sandbox) (metadata.Profile, error)
	Access(context.Context, metadata.Sandbox) (metadata.Access, error)
}
type Control interface {
	Get(context.Context, auth.Tenant, metadata.Sandbox) (*pb.Actor, error)
	Template(context.Context, auth.Tenant, string) (*pb.ActorTemplate, error)
	Capture(context.Context, auth.Tenant, metadata.Sandbox, uint64, string) (*pb.Tag, error)
}
type Children interface {
	Restore(context.Context, auth.Tenant, metadata.Sandbox, *pb.Tag, *pb.ActorTemplate, metadata.Access, metadata.Profile, *pb.AgentENVNetworkPolicy) (metadata.Sandbox, *pb.ConnectActorResponse, error)
	Cancel(context.Context, auth.Tenant, metadata.Sandbox, string) error
	Confirmed(context.Context, auth.Tenant, metadata.Sandbox, string) (bool, error)
}
type Service struct {
	Store    Store
	Control  Control
	Children Children
	Tenants  map[string]auth.Tenant
}
type plan struct {
	Source      metadata.Sandbox
	Generation  uint64
	Destination string
	Template    []byte
	Access      metadata.Access
	Profile     metadata.Profile
	Policy      *pb.AgentENVNetworkPolicy
	Children    []metadata.Sandbox
}
type receipt struct {
	ChildIDs []string
	Errors   []string
}

func (s *Service) Fork(ctx context.Context, t auth.Tenant, b metadata.Sandbox, in Input, key string) ([]Outcome, error) {
	count := uint32(1)
	if in.Count != nil {
		count = *in.Count
	}
	if count < 1 || count > 100 || len(key) > 200 || b.Tenant != t.ID || b.ActorAtespace != t.Atespace || b.ActorUID == "" {
		return nil, status.Error(codes.InvalidArgument, "invalid fork request")
	}
	timeout := b.TimeoutSeconds
	if in.Timeout != nil {
		timeout = int(*in.Timeout)
	}
	payload, _ := json.Marshal(struct {
		Tenant, Parent, UID string
		Input               Input
	}{t.ID, b.ExternalID, b.ActorUID, in})
	digest := sha256.Sum256(payload)
	if key == "" {
		var random [16]byte
		if _, err := rand.Read(random[:]); err != nil {
			return nil, err
		}
		key = hex.EncodeToString(random[:])
	}
	operation := sha256.Sum256([]byte(t.ID + "\x00" + b.ExternalID + "\x00" + key))
	request := metadata.Request{Tenant: t.ID, ExternalID: b.ExternalID, ID: "fork-" + hex.EncodeToString(operation[:]), Kind: "fork", Digest: hex.EncodeToString(digest[:])}
	existing, err := s.Store.Fork(ctx, request)
	if err == nil {
		return s.execute(ctx, t, existing)
	}
	if !errors.Is(err, metadata.ErrNotFound) {
		return nil, err
	}
	actor, err := s.Control.Get(ctx, t, b)
	if err != nil {
		return nil, err
	}
	if actor.GetActorTemplate().GetAtespace() != t.Atespace || actor.GetMetadata().GetUid() != b.ActorUID || actor.GetStatus().GetState() != pb.ActorState_ACTOR_STATE_RUNNING || actor.GetStatus().GetWorkerAssignment().GetAssignmentGeneration() == 0 {
		return nil, status.Error(codes.FailedPrecondition, "fork source must be running")
	}
	delivery := actor.GetStatus().GetAgentenvPolicyDelivery()
	if delivery != nil && (delivery.GetRevision() == 0 || delivery.GetAppliedRevision() != delivery.GetRevision() || !proto.Equal(delivery.GetAppliedAssignment(), actor.GetStatus().GetWorkerAssignment())) {
		return nil, status.Error(codes.FailedPrecondition, "fork waits for network policy acknowledgment")
	}
	template, err := s.Control.Template(ctx, t, actor.GetActorTemplate().GetName())
	if err != nil {
		return nil, err
	}
	profile, err := s.Store.Profile(ctx, b)
	if err != nil {
		return nil, err
	}
	access, err := s.Store.Access(ctx, b)
	if err != nil {
		return nil, err
	}
	wire, err := proto.MarshalOptions{Deterministic: true}.Marshal(template)
	if err != nil {
		return nil, err
	}
	p := plan{Source: b, Generation: actor.Status.WorkerAssignment.AssignmentGeneration, Destination: "fork-" + hex.EncodeToString(operation[:20]), Template: wire, Profile: profile, Access: access, Policy: proto.CloneOf(actor.GetStatus().GetAgentenvPolicyDelivery().GetPolicy())}
	if p.Policy == nil {
		p.Policy = &pb.AgentENVNetworkPolicy{}
	}
	for n := uint32(0); n < count; n++ {
		h := sha256.Sum256([]byte(fmt.Sprintf("%x:%d", operation, n)))
		h[6] = (h[6] & 15) | 64
		h[8] = (h[8] & 63) | 128
		id := fmt.Sprintf("%x-%x-%x-%x-%x", h[:4], h[4:6], h[6:8], h[8:10], h[10:16])
		p.Children = append(p.Children, metadata.Sandbox{Tenant: t.ID, ExternalID: id, ActorAtespace: t.Atespace, ActorName: "aenv-" + hex.EncodeToString(h[:16]), TemplateAlias: b.TemplateAlias, TimeoutSeconds: timeout})
	}
	prepared, err := json.Marshal(p)
	if err != nil {
		return nil, err
	}
	job, err := s.Store.ReserveFork(ctx, b, request, prepared)
	if err != nil {
		return nil, err
	}
	return s.execute(ctx, t, job)
}
func (s *Service) execute(ctx context.Context, t auth.Tenant, job metadata.Fork) ([]Outcome, error) {
	unlock, err := s.Store.LockOperation(ctx, t.ID, job.Request.ExternalID)
	if err != nil {
		return nil, err
	}
	defer unlock()
	job, err = s.Store.Fork(ctx, job.Request)
	if err != nil {
		return nil, err
	}
	if job.Request.State == "completed" {
		var r receipt
		if err := json.Unmarshal(job.Request.Result, &r); err != nil {
			return nil, err
		}
		return s.present(ctx, t, r)
	}
	if job.Request.State != "pending" {
		return nil, metadata.ErrConflict
	}
	var p plan
	if err := json.Unmarshal(job.Prepared, &p); err != nil {
		return nil, err
	}
	if p.Source.Tenant != t.ID || p.Source.ActorAtespace != t.Atespace || p.Source.ActorUID != job.ActorUID || len(p.Children) < 1 || len(p.Children) > 100 {
		return nil, metadata.ErrConflict
	}
	var template pb.ActorTemplate
	if err := proto.Unmarshal(p.Template, &template); err != nil {
		return nil, err
	}
	var tag *pb.Tag
	if job.Captured {
		tag = &pb.Tag{}
		if err := proto.Unmarshal(job.Snapshot, tag); err != nil {
			return nil, err
		}
	} else {
		tag, err = s.Control.Capture(ctx, t, p.Source, p.Generation, p.Destination)
		if err != nil {
			return nil, err
		}
		snapshot, err := proto.MarshalOptions{Deterministic: true}.Marshal(tag)
		if err != nil {
			return nil, err
		}
		if err := s.Store.ConfirmForkCaptured(ctx, job, snapshot); err != nil {
			return nil, err
		}
	}
	r := receipt{ChildIDs: make([]string, len(p.Children)), Errors: make([]string, len(p.Children))}
	recorded, err := s.Store.ForkChildren(ctx, job)
	if err != nil {
		return nil, err
	}
	done := map[int]metadata.ForkChild{}
	for _, item := range recorded {
		if item.Index < 0 || item.Index >= len(p.Children) || item.ID != p.Children[item.Index].ExternalID {
			return nil, metadata.ErrConflict
		}
		done[item.Index] = item
	}
	var pending []error
	for n, child := range p.Children {
		if item, ok := done[n]; ok {
			if item.Failed {
				r.Errors[n] = "fork child could not be started"
			} else {
				r.ChildIDs[n] = child.ExternalID
			}
			continue
		}
		_, _, err := s.Children.Restore(ctx, t, child, tag, &template, p.Access, p.Profile, p.Policy)
		if err != nil {
			confirmed, confirmationErr := s.Children.Confirmed(ctx, t, child, tag.GetMetadata().GetUid())
			if confirmationErr != nil {
				pending = append(pending, confirmationErr)
				continue
			}
			if confirmed {
				err = nil
			}
		}
		if err == nil {
			if err := s.Store.RecordForkChild(ctx, job, metadata.ForkChild{Index: n, ID: child.ExternalID}); err != nil {
				pending = append(pending, err)
				continue
			}
			r.ChildIDs[n] = child.ExternalID
			continue
		}
		// Transport failures remain unknown and must not become terminal failures.
		// A permanent failure is reported only after independent child cleanup.
		code := status.Code(err)
		if code != codes.InvalidArgument && code != codes.FailedPrecondition && code != codes.ResourceExhausted && !errors.Is(err, metadata.ErrConflict) {
			pending = append(pending, err)
			continue
		}
		if cleanup := s.Children.Cancel(ctx, t, child, tag.GetMetadata().GetUid()); cleanup != nil {
			pending = append(pending, cleanup)
			continue
		}
		if err := s.Store.RecordForkChild(ctx, job, metadata.ForkChild{Index: n, ID: child.ExternalID, Failed: true}); err != nil {
			pending = append(pending, err)
			continue
		}
		r.Errors[n] = "fork child could not be started"
	}
	if len(pending) > 0 {
		return nil, errors.Join(pending...)
	}
	result, err := json.Marshal(r)
	if err != nil {
		return nil, err
	}
	if err := s.Store.Complete(ctx, job.Request, false, result); err != nil {
		return nil, err
	}
	return s.present(ctx, t, r)
}
func (s *Service) present(ctx context.Context, t auth.Tenant, r receipt) ([]Outcome, error) {
	if len(r.ChildIDs) != len(r.Errors) {
		return nil, metadata.ErrConflict
	}
	out := make([]Outcome, len(r.ChildIDs))
	for n, id := range r.ChildIDs {
		if id == "" {
			out[n].Error = r.Errors[n]
			continue
		}
		b, err := s.Store.Get(ctx, t.ID, id)
		if errors.Is(err, metadata.ErrNotFound) {
			out[n].Error = "fork child no longer exists"
			continue
		}
		if err != nil {
			return nil, err
		}
		out[n].Sandbox = &b
	}
	return out, nil
}
func (s *Service) Run(ctx context.Context, report func(error)) error {
	for {
		jobs, err := s.Store.ClaimForks(ctx, 10)
		if err != nil {
			report(err)
		} else {
			for _, job := range jobs {
				t, ok := s.Tenants[job.Request.Tenant]
				if !ok {
					report(metadata.ErrConflict)
					continue
				}
				call, cancel := context.WithTimeout(ctx, 2*time.Minute)
				_, err := s.execute(call, t, job)
				cancel()
				if err != nil {
					report(err)
				}
			}
		}
		timer := time.NewTimer(5 * time.Second)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

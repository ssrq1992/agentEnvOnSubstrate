// Package lifecycle reconciles SDK lifecycle intent through Substrate.
package lifecycle

import (
	"agentenv/services/aenv-api-bridge/internal/auth"
	"agentenv/services/aenv-api-bridge/internal/metadata"
	"context"
	"errors"
	"fmt"
	pb "github.com/agent-substrate/substrate/pkg/proto/ateapipb"
	"time"
)

type Store interface {
	PrepareExpiredSuspend(context.Context, metadata.Expiry, uint64) (metadata.SuspendIntent, error)
	ReserveResume(context.Context, metadata.Sandbox, string, int, bool) (metadata.ResumeIntent, error)
	ConfirmResumed(context.Context, metadata.ResumeIntent) error
	ClaimResumes(context.Context, int) ([]metadata.ResumeIntent, error)
	ReserveSuspend(context.Context, metadata.Sandbox, uint64) (metadata.SuspendIntent, error)
	ConfirmSuspended(context.Context, metadata.SuspendIntent) error
	ClaimSuspends(context.Context, int) ([]metadata.SuspendIntent, error)
}
type Control interface {
	ResumeSource(context.Context, auth.Tenant, metadata.Sandbox) (string, bool, error)
	ResumePrepared(context.Context, auth.Tenant, metadata.ResumeIntent) error
	Connect(context.Context, auth.Tenant, metadata.Sandbox) (*pb.ConnectActorResponse, error)
	SuspendGeneration(context.Context, auth.Tenant, metadata.Sandbox) (uint64, error)
	SuspendPrepared(context.Context, auth.Tenant, metadata.SuspendIntent) error
}
type Service struct {
	Store   Store
	Control Control
	Tenants map[string]auth.Tenant
}

func (s *Service) Pause(ctx context.Context, t auth.Tenant, b metadata.Sandbox) error {
	if s.Store == nil || s.Control == nil {
		return fmt.Errorf("lifecycle dependencies required")
	}
	if t.ID == "" || t.ID != b.Tenant || t.Atespace != b.ActorAtespace {
		return metadata.ErrNotFound
	}
	generation, err := s.Control.SuspendGeneration(ctx, t, b)
	if err != nil {
		return err
	}
	intent, err := s.Store.ReserveSuspend(ctx, b, generation)
	if err != nil {
		return err
	}
	if intent.Request.State == "completed" {
		return nil
	}
	if intent.Request.State != "pending" {
		return metadata.ErrConflict
	}
	return s.execute(ctx, t, intent)
}
func (s *Service) execute(ctx context.Context, t auth.Tenant, i metadata.SuspendIntent) error {
	if i.Sandbox.Tenant != t.ID || i.Sandbox.ActorAtespace != t.Atespace || i.Sandbox.ActorUID == "" || i.Request.Tenant != t.ID || i.Request.ExternalID != i.Sandbox.ExternalID || i.Request.Kind != "suspend" {
		return metadata.ErrConflict
	}
	if err := s.Control.SuspendPrepared(ctx, t, i); err != nil {
		return err
	}
	return s.Store.ConfirmSuspended(ctx, i)
}
func (s *Service) Sweep(ctx context.Context) error {
	if s.Store == nil || s.Control == nil || len(s.Tenants) == 0 {
		return fmt.Errorf("lifecycle dependencies required")
	}
	intents, err := s.Store.ClaimSuspends(ctx, 10)
	if err != nil {
		return err
	}
	var failures []error
	for _, i := range intents {
		if ctx.Err() != nil {
			return errors.Join(append(failures, ctx.Err())...)
		}
		tenant, ok := s.Tenants[i.Sandbox.Tenant]
		if !ok {
			failures = append(failures, metadata.ErrConflict)
			continue
		}
		call, cancel := context.WithTimeout(ctx, 2*time.Minute)
		err = s.execute(call, tenant, i)
		cancel()
		if err != nil {
			failures = append(failures, err)
		}
	}
	resumes, e := s.Store.ClaimResumes(ctx, 10)
	if e != nil {
		return errors.Join(append(failures, e)...)
	}
	for _, i := range resumes {
		if ctx.Err() != nil {
			return errors.Join(append(failures, ctx.Err())...)
		}
		tenant, ok := s.Tenants[i.Sandbox.Tenant]
		if !ok {
			failures = append(failures, metadata.ErrConflict)
			continue
		}
		call, cancel := context.WithTimeout(ctx, 2*time.Minute)
		e = s.executeResume(call, tenant, i)
		cancel()
		if e != nil {
			failures = append(failures, e)
		}
	}
	return errors.Join(failures...)
}
func (s *Service) Run(ctx context.Context, report func(error)) error {
	if report == nil {
		return fmt.Errorf("lifecycle error reporter required")
	}
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err := s.Sweep(ctx); err != nil && ctx.Err() == nil {
			report(err)
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

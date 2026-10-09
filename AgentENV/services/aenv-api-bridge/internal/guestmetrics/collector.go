package guestmetrics

import (
	"agentenv/services/aenv-api-bridge/internal/auth"
	"agentenv/services/aenv-api-bridge/internal/metadata"
	"context"
	"fmt"
	pb "github.com/agent-substrate/substrate/pkg/proto/ateapipb"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"sync"
	"time"
)

type Repository interface {
	Candidates(context.Context, string, string) ([]metadata.Sandbox, error)
	Append(context.Context, metadata.Sandbox, *pb.GetActorGuestMetricsResponse) error
	Prune(context.Context) error
}
type Control interface {
	GuestMetrics(context.Context, auth.Tenant, metadata.Sandbox) (*pb.GetActorGuestMetricsResponse, error)
}
type Collector struct {
	Store                  Repository
	Control                Control
	Tenants                map[string]auth.Tenant
	cursorTenant, cursorID string
}

// Sweep continues from its keyset cursor. It is called serially by Run; a
// timed-out sweep does not reset progress to the first page.
func (c *Collector) Sweep(ctx context.Context) error {
	if c.Store == nil || c.Control == nil || len(c.Tenants) == 0 {
		return fmt.Errorf("metrics collector dependencies required")
	}
	if err := c.Store.Prune(ctx); err != nil {
		return err
	}
	failures := 0
	for ctx.Err() == nil {
		rows, err := c.Store.Candidates(ctx, c.cursorTenant, c.cursorID)
		if err != nil {
			return err
		}
		if len(rows) == 0 {
			c.cursorTenant = ""
			c.cursorID = ""
			break
		}
		jobs := make(chan metadata.Sandbox)
		var wg sync.WaitGroup
		var mu sync.Mutex
		for i := 0; i < 8; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for b := range jobs {
					tenant, ok := c.Tenants[b.Tenant]
					if !ok || tenant.ID != b.Tenant || tenant.Atespace != b.ActorAtespace {
						mu.Lock()
						failures++
						mu.Unlock()
						continue
					}
					call, cancel := context.WithTimeout(ctx, 2*time.Second)
					sample, e := c.Control.GuestMetrics(call, tenant, b)
					if e == nil {
						e = c.Store.Append(call, b, sample)
					}
					cancel()
					if e != nil && status.Code(e) != codes.FailedPrecondition && status.Code(e) != codes.NotFound {
						mu.Lock()
						failures++
						mu.Unlock()
					}
				}
			}()
		}
		for _, b := range rows {
			jobs <- b
		}
		close(jobs)
		wg.Wait()
		last := rows[len(rows)-1]
		c.cursorTenant = last.Tenant
		c.cursorID = last.ExternalID
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if failures > 0 {
		return fmt.Errorf("%d guest metric samples could not be collected", failures)
	}
	return nil
}
func (c *Collector) Run(ctx context.Context, report func(error)) error {
	if report == nil {
		return fmt.Errorf("metrics error reporter required")
	}
	timer := time.NewTicker(15 * time.Second)
	defer timer.Stop()
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		sweep, cancel := context.WithTimeout(ctx, 12*time.Second)
		err := c.Sweep(sweep)
		cancel()
		if err != nil && ctx.Err() == nil {
			report(err)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
		}
	}
}

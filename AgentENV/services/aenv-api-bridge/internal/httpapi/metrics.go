package httpapi

import (
	"agentenv/services/aenv-api-bridge/internal/auth"
	"agentenv/services/aenv-api-bridge/internal/guestmetrics"
	"agentenv/services/aenv-api-bridge/internal/metadata"
	"context"
	"encoding/json"
	"errors"
	pb "github.com/agent-substrate/substrate/pkg/proto/ateapipb"
	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

type MetricsReader interface {
	History(context.Context, metadata.Sandbox, *int64, *int64) ([]guestmetrics.Sample, error)
}

func (s *Server) metrics(w http.ResponseWriter, r *http.Request) {
	_, b, err := s.sandbox(r)
	if err != nil {
		report(w, err)
		return
	}
	query, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		failure(w, 400, "invalid query encoding")
		return
	}
	var start, end *int64
	for _, bound := range []struct {
		name   string
		target **int64
	}{{"start", &start}, {"end", &end}} {
		if values, ok := query[bound.name]; ok {
			if len(values) != 1 {
				failure(w, 400, "one value per metric interval bound required")
				return
			}
			var value int64
			value, err = strconv.ParseInt(values[0], 10, 64)
			if err != nil || value < 0 {
				failure(w, 400, "metric interval bounds must be non-negative integers")
				return
			}
			*bound.target = &value
		}
	}
	if start != nil && end != nil && *start > *end {
		failure(w, 400, "start must not exceed end")
		return
	}
	samples, err := s.Metrics.History(r.Context(), b, start, end)
	if err != nil {
		report(w, err)
		return
	}
	if samples == nil {
		samples = []guestmetrics.Sample{}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(samples)
}

type LatestMetricsReader interface {
	Latest(context.Context, metadata.Sandbox, *pb.WorkerAssignment) (*guestmetrics.Sample, error)
}

func (s *Server) batchMetrics(w http.ResponseWriter, r *http.Request) {
	query, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		failure(w, 400, "invalid query encoding")
		return
	}
	values := query["sandbox_ids"]
	if len(values) != 1 {
		failure(w, 400, "sandbox_ids must be a single comma-separated list")
		return
	}
	parts := strings.Split(values[0], ",")
	if len(parts) > 100 {
		failure(w, 400, "sandbox_ids must contain at most 100 IDs")
		return
	}
	ids := make([]string, 0, len(parts))
	seen := map[string]bool{}
	for _, part := range parts {
		id, e := uuid.Parse(part)
		if e != nil || seen[id.String()] {
			failure(w, 400, "sandbox_ids must contain distinct valid sandbox IDs")
			return
		}
		seen[id.String()] = true
		ids = append(ids, id.String())
	}
	tenant := r.Context().Value(tenantKey{}).(auth.Tenant)
	result := map[string]guestmetrics.Sample{}
	for _, id := range ids {
		b, e := s.Store.Get(r.Context(), tenant.ID, id)
		if errors.Is(e, metadata.ErrNotFound) {
			continue
		}
		if e != nil {
			report(w, e)
			return
		}
		if b.Tenant != tenant.ID || b.ExternalID != id || b.ActorAtespace != tenant.Atespace || b.ActorUID == "" {
			continue
		}
		actor, e := s.InfoControl.Get(r.Context(), tenant, b)
		if errors.Is(e, metadata.ErrNotFound) || status.Code(e) == codes.NotFound {
			continue
		}
		if e != nil {
			report(w, e)
			return
		}
		if actor.GetMetadata().GetUid() != b.ActorUID || actor.GetStatus().GetState() != pb.ActorState_ACTOR_STATE_RUNNING {
			continue
		}
		a := actor.GetStatus().GetWorkerAssignment()
		sample, e := s.LatestMetrics.Latest(r.Context(), b, a)
		if e != nil {
			report(w, e)
			return
		}
		if sample != nil {
			result[id] = *sample
		}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(struct {
		Sandboxes map[string]guestmetrics.Sample `json:"sandboxes"`
	}{result})
}

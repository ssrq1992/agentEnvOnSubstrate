package httpapi

import (
	"agentenv/services/aenv-api-bridge/internal/auth"
	"agentenv/services/aenv-api-bridge/internal/metadata"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	pb "github.com/agent-substrate/substrate/pkg/proto/ateapipb"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

type InfoStore interface {
	ListPage(context.Context, string, string, int) ([]metadata.Sandbox, error)
	Profile(context.Context, metadata.Sandbox) (metadata.Profile, error)
	Access(context.Context, metadata.Sandbox) (metadata.Access, error)
}
type InfoControl interface {
	Get(context.Context, auth.Tenant, metadata.Sandbox) (*pb.Actor, error)
}
type networkInfo struct {
	AllowPublicTraffic bool     `json:"allowPublicTraffic"`
	AllowOut           []string `json:"allowOut,omitempty"`
	DenyOut            []string `json:"denyOut,omitempty"`
}

type sandboxInfo struct {
	EnvdAccessToken     string            `json:"envdAccessToken,omitempty"`
	AllowInternetAccess *bool             `json:"allowInternetAccess"`
	TemplateID          string            `json:"templateID"`
	SandboxID           string            `json:"sandboxID"`
	Alias               string            `json:"alias,omitempty"`
	ClientID            string            `json:"clientID"`
	EnvdVersion         string            `json:"envdVersion"`
	StartedAt           time.Time         `json:"startedAt"`
	EndAt               time.Time         `json:"endAt"`
	CPUCount            int               `json:"cpuCount"`
	MemoryMB            int               `json:"memoryMB"`
	DiskSizeMB          int               `json:"diskSizeMB"`
	Metadata            map[string]string `json:"metadata,omitempty"`
	State               string            `json:"state"`
	Domain              string            `json:"domain,omitempty"`
	Lifecycle           *struct {
		AutoResume bool   `json:"autoResume"`
		OnTimeout  string `json:"onTimeout"`
	} `json:"lifecycle,omitempty"`
	Network      *networkInfo `json:"network,omitempty"`
	VolumeMounts []any        `json:"volumeMounts"`
}

func (s *Server) readInfo(ctx context.Context, t auth.Tenant, b metadata.Sandbox, detail bool) (sandboxInfo, error) {
	var out sandboxInfo
	p, err := s.Infos.Profile(ctx, b)
	if err != nil {
		return out, err
	}
	actor, err := s.InfoControl.Get(ctx, t, b)
	if err != nil {
		return out, err
	}
	if actor.GetMetadata().GetUid() != b.ActorUID {
		return out, metadata.ErrNotFound
	}
	state := "running"
	switch actor.GetStatus().GetState() {
	case pb.ActorState_ACTOR_STATE_RUNNING, pb.ActorState_ACTOR_STATE_RESUMING:
	case pb.ActorState_ACTOR_STATE_SUSPENDED, pb.ActorState_ACTOR_STATE_SUSPENDING, pb.ActorState_ACTOR_STATE_PAUSED, pb.ActorState_ACTOR_STATE_PAUSING:
		state = "paused"
	default:
		return out, status.Error(codes.FailedPrecondition, "sandbox runtime is unavailable")
	}
	out = sandboxInfo{TemplateID: p.TemplateID, SandboxID: b.ExternalID, Alias: p.Alias, EnvdVersion: p.EnvdVersion, StartedAt: b.CreatedAt, EndAt: b.ExpiresAt, CPUCount: p.CPUCount, MemoryMB: p.MemoryMB, DiskSizeMB: p.DiskSizeMB, Metadata: p.Metadata, State: state, VolumeMounts: []any{}}
	if detail {
		a, e := s.Infos.Access(ctx, b)
		if e != nil {
			return out, e
		}
		out.Domain = s.SandboxDomain
		action := "kill"
		if a.AutoPause {
			action = "pause"
		}
		out.Lifecycle = &struct {
			AutoResume bool   `json:"autoResume"`
			OnTimeout  string `json:"onTimeout"`
		}{a.AutoResume, action}
		policy := actor.GetStatus().GetAgentenvPolicyDelivery().GetPolicy()
		if policy.GetBase() != pb.AgentENVNetworkPolicy_DEFAULT {
			v := policy.GetBase() == pb.AgentENVNetworkPolicy_ALLOW
			out.AllowInternetAccess = &v
		}
		if !a.AllowPublicTraffic || len(policy.GetAllowOut()) > 0 || len(policy.GetDenyOut()) > 0 {
			out.Network = &networkInfo{AllowPublicTraffic: a.AllowPublicTraffic, AllowOut: policy.GetAllowOut(), DenyOut: policy.GetDenyOut()}
		}
		if s.Data != nil {
			token, _, e := s.Data.Credentials(ctx, b)
			if e != nil {
				return out, e
			}
			out.EnvdAccessToken = token
		}
	}
	return out, nil
}
func (s *Server) info(w http.ResponseWriter, r *http.Request) {
	t, b, err := s.sandbox(r)
	if err != nil {
		report(w, err)
		return
	}
	out, err := s.readInfo(r.Context(), t, b, true)
	if err != nil {
		report(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(out)
}

type listCursor struct {
	Tenant, Query, ID string
	Time              time.Time
}

func (s *Server) list(w http.ResponseWriter, r *http.Request) {
	t := r.Context().Value(tenantKey{}).(auth.Tenant)
	q := r.URL.Query()
	v2 := r.URL.Path == "/v2/sandboxes"
	desc := q.Get("order") != "asc"
	if q.Get("order") != "" && q.Get("order") != "asc" && q.Get("order") != "desc" {
		failure(w, 400, "invalid sort order")
		return
	}
	limit := 100
	if q.Get("limit") != "" {
		n, e := strconv.Atoi(q.Get("limit"))
		if e != nil || n < 1 || n > 100 {
			failure(w, 400, "invalid page size")
			return
		}
		limit = n
	}
	filters, e := url.ParseQuery(q.Get("metadata"))
	if e != nil {
		failure(w, 400, "invalid metadata filter")
		return
	}
	states := strings.Split(q.Get("state"), ",")
	for _, state := range states {
		if state != "" && state != "running" && state != "paused" {
			failure(w, 400, "invalid state filter")
			return
		}
	}
	var after time.Time
	if q.Get("startedAfter") != "" {
		var e error
		after, e = time.Parse(time.RFC3339Nano, q.Get("startedAfter"))
		if e != nil {
			failure(w, 400, "invalid start time")
			return
		}
	}
	frozen := url.Values{}
	for _, key := range []string{"metadata", "state", "order", "startedAfter", "template"} {
		frozen.Set(key, q.Get(key))
	}
	sum := sha256.Sum256([]byte(frozen.Encode()))
	queryID := hex.EncodeToString(sum[:])
	var cursor *listCursor
	if token := q.Get("nextToken"); token != "" {
		raw, e := base64.RawURLEncoding.DecodeString(token)
		var c listCursor
		if e != nil || len(raw) > 4096 || json.Unmarshal(raw, &c) != nil || c.Tenant != t.ID || c.Query != queryID || c.ID == "" {
			failure(w, 400, "invalid page token")
			return
		}
		cursor = &c
	}
	out := []sandboxInfo{}
	scanAfter := ""
	totalRunning := 0
	for {
		page, err := s.Infos.ListPage(r.Context(), t.ID, scanAfter, 1000)
		if err != nil {
			report(w, err)
			return
		}
		if len(page) == 0 {
			break
		}
		for _, b := range page {
			if b.ActorAtespace != t.Atespace {
				failure(w, 503, "sandbox tenant mapping invalid")
				return
			}
			item, err := s.readInfo(r.Context(), t, b, false)
			if status.Code(err) == codes.NotFound {
				continue
			}
			if err != nil {
				report(w, err)
				return
			}
			if !v2 && item.State != "running" {
				continue
			}
			if v2 && len(states) == 1 && states[0] != "" && item.State != states[0] {
				continue
			}
			if !after.IsZero() && item.StartedAt.Before(after) {
				continue
			}
			if template := q.Get("template"); template != "" && item.TemplateID != template && item.Alias != template {
				continue
			}
			matches := true
			for key, values := range filters {
				if key != "" && (len(values) == 0 || item.Metadata[key] != values[len(values)-1]) {
					matches = false
					break
				}
			}
			if !matches {
				continue
			}
			if item.State == "running" {
				totalRunning++
			}
			if cursor != nil {
				cmp := item.StartedAt.Compare(cursor.Time)
				if cmp == 0 {
					cmp = strings.Compare(item.SandboxID, cursor.ID)
				}
				if desc && cmp >= 0 || !desc && cmp <= 0 {
					continue
				}
			}
			out = append(out, item)
		}
		scanAfter = page[len(page)-1].ExternalID
		if len(page) < 1000 {
			break
		}
	}
	sort.Slice(out, func(i, j int) bool {
		cmp := out[i].StartedAt.Compare(out[j].StartedAt)
		if cmp == 0 {
			cmp = strings.Compare(out[i].SandboxID, out[j].SandboxID)
		}
		if desc {
			return cmp > 0
		}
		return cmp < 0
	})
	if v2 {
		if len(states) != 1 || states[0] != "paused" {
			w.Header().Set("X-Total-Running", strconv.Itoa(totalRunning))
		}
		if len(out) > limit {
			out = out[:limit]
			last := out[len(out)-1]
			raw, _ := json.Marshal(listCursor{Tenant: t.ID, Query: queryID, ID: last.SandboxID, Time: last.StartedAt})
			w.Header().Set("X-Next-Token", base64.RawURLEncoding.EncodeToString(raw))
		}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(out)
}

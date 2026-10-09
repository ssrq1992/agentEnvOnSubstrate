package httpapi

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"agentenv/services/aenv-api-bridge/internal/auth"
	"agentenv/services/aenv-api-bridge/internal/metadata"
	snapshotservice "agentenv/services/aenv-api-bridge/internal/snapshots"
)

type Snapshots interface {
	Create(context.Context, auth.Tenant, metadata.Sandbox, string, string) (snapshotservice.Info, error)
	Get(context.Context, string, string) (snapshotservice.Info, error)
	Page(context.Context, string, *time.Time, string, int, string, string) ([]snapshotservice.Info, error)
}

func (s *Server) capture(w http.ResponseWriter, r *http.Request) {
	t, b, err := s.sandbox(r)
	if err != nil {
		report(w, err)
		return
	}
	media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || media != "application/json" {
		failure(w, 400, "JSON snapshot request required")
		return
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
	decoder.DisallowUnknownFields()
	var input *struct {
		Name *string `json:"name,omitempty"`
	}
	if decoder.Decode(&input) != nil || input == nil {
		failure(w, 400, "invalid snapshot request")
		return
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		failure(w, 400, "exactly one JSON request required")
		return
	}
	name := ""
	if input.Name != nil {
		name = *input.Name
		if name == "" {
			failure(w, 400, "snapshot name must not be empty")
			return
		}
	}
	key := r.Header.Get("Idempotency-Key")
	if len(r.Header.Values("Idempotency-Key")) > 1 || len(key) > 200 {
		failure(w, 400, "invalid idempotency key")
		return
	}
	info, err := s.Snapshots.Create(r.Context(), t, b, name, key)
	if err != nil {
		report(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(info)
}
func (s *Server) snapshot(w http.ResponseWriter, r *http.Request) {
	tenant := r.Context().Value(tenantKey{}).(auth.Tenant)
	info, err := s.Snapshots.Get(r.Context(), tenant.ID, r.PathValue("snapshotID"))
	if err != nil {
		templateError(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(info)
}

type snapshotCursor struct {
	Tenant, ID, Source, Name string
	Time                     time.Time
}

func (s *Server) snapshots(w http.ResponseWriter, r *http.Request) {
	tenant := r.Context().Value(tenantKey{}).(auth.Tenant)
	q, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		failure(w, 400, "invalid query encoding")
		return
	}
	for _, key := range []string{"limit", "nextToken", "sandboxID", "name"} {
		if len(q[key]) > 1 {
			failure(w, 400, "one value per snapshot query required")
			return
		}
	}
	source, name := q.Get("sandboxID"), q.Get("name")
	var limit int
	if raw, ok := q["limit"]; ok {
		limit, err = strconv.Atoi(raw[0])
		if err != nil || limit < 1 || limit > 100 {
			failure(w, 400, "snapshot limit must be between 1 and 100")
			return
		}
	}
	var cursor snapshotCursor
	if raw, ok := q["nextToken"]; ok {
		wire, e := base64.RawURLEncoding.DecodeString(raw[0])
		if len(raw[0]) > 8192 || e != nil || json.Unmarshal(wire, &cursor) != nil || cursor.Tenant != tenant.ID || cursor.Source != source || cursor.Name != name || cursor.ID == "" || strings.ContainsAny(cursor.ID, "\x00\r\n") || len(cursor.ID) > 256 || cursor.Time.Year() < 1 || cursor.Time.Year() > 9999 || cursor.Time.IsZero() {
			failure(w, 400, "invalid snapshot page token")
			return
		}
	}
	out := []snapshotservice.Info{}
	for {
		var before *time.Time
		if !cursor.Time.IsZero() {
			before = &cursor.Time
		}
		batch := 1000
		if limit > 0 && limit+1-len(out) < batch {
			batch = limit + 1 - len(out)
		}
		records, e := s.Snapshots.Page(r.Context(), tenant.ID, before, cursor.ID, batch, source, name)
		if e != nil {
			templateError(w, e)
			return
		}
		out = append(out, records...)
		if len(records) < batch || limit > 0 && len(out) > limit {
			break
		}
		last := records[len(records)-1]
		cursor = snapshotCursor{Tenant: tenant.ID, ID: last.ID, Time: last.CreatedAt, Source: source, Name: name}
	}
	if limit > 0 && len(out) > limit {
		last := out[limit-1]
		wire, _ := json.Marshal(snapshotCursor{Tenant: tenant.ID, ID: last.ID, Time: last.CreatedAt, Source: source, Name: name})
		w.Header().Set("X-Next-Token", base64.RawURLEncoding.EncodeToString(wire))
		out = out[:limit]
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(out)
}

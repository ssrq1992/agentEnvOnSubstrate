package httpapi

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"agentenv/services/aenv-api-bridge/internal/auth"
	"agentenv/services/aenv-api-bridge/internal/metadata"
	"agentenv/services/aenv-api-bridge/internal/templateregistry"
)

type Templates interface {
	Get(context.Context, string, string) (templateregistry.Record, error)
	Page(context.Context, string, *templateregistry.Cursor, int) ([]templateregistry.Record, error)
}
type templateView struct {
	TemplateID    string     `json:"templateID"`
	BuildID       string     `json:"buildID"`
	CPUCount      int        `json:"cpuCount"`
	MemoryMB      int        `json:"memoryMB"`
	DiskSizeMB    int        `json:"diskSizeMB"`
	Public        bool       `json:"public"`
	Names         []string   `json:"names"`
	CreatedAt     time.Time  `json:"createdAt"`
	UpdatedAt     time.Time  `json:"updatedAt"`
	LastSpawnedAt *time.Time `json:"lastSpawnedAt"`
	SpawnCount    int64      `json:"spawnCount"`
	BuildCount    int        `json:"buildCount"`
	EnvdVersion   string     `json:"envdVersion"`
	BuildStatus   string     `json:"buildStatus"`
}

func presentTemplate(r templateregistry.Record) templateView {
	p := r.Profile
	names := []string{}
	if p.Alias != "" {
		names = append(names, p.Alias)
	}
	// A pre-provisioned native artifact is one imported build. Native UID is its
	// immutable build identity; pending jobs are not published in this directory.
	return templateView{TemplateID: p.ID, BuildID: p.NativeUID, CPUCount: p.CPUCount, MemoryMB: p.MemoryMB, DiskSizeMB: p.DiskSizeMB, Names: names, CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt, BuildCount: 1, EnvdVersion: p.EnvdVersion, BuildStatus: "ready"}
}
func templateError(w http.ResponseWriter, err error) {
	if errors.Is(err, metadata.ErrNotFound) {
		failure(w, 404, "template not found")
	} else {
		failure(w, 503, "template catalog unavailable")
	}
}
func (s *Server) templateAlias(w http.ResponseWriter, r *http.Request) {
	t := r.Context().Value(tenantKey{}).(auth.Tenant)
	alias := r.PathValue("alias")
	valid := alias != ""
	for _, c := range alias {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
			valid = false
		}
	}
	if !valid {
		failure(w, 400, "invalid template alias")
		return
	}
	record, err := s.Templates.Get(r.Context(), t.ID, alias)
	if err != nil {
		templateError(w, err)
		return
	}
	if record.Profile.Tenant != t.ID || record.Profile.Alias != alias {
		templateError(w, metadata.ErrNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(struct {
		TemplateID string `json:"templateID"`
		Public     bool   `json:"public"`
	}{TemplateID: record.Profile.ID})
}
func (s *Server) templates(w http.ResponseWriter, r *http.Request) {
	t := r.Context().Value(tenantKey{}).(auth.Tenant)
	q := r.URL.Query()
	v2 := r.URL.Path == "/v2/templates"
	var cursor *templateregistry.Cursor
	limit := uint64(0)
	bounded := false
	if v2 {
		if len(q["limit"]) > 1 || len(q["nextToken"]) > 1 {
			failure(w, 400, "ambiguous template page")
			return
		}
		if raw, ok := q["limit"]; ok {
			n, err := strconv.ParseUint(raw[0], 10, 32)
			if err != nil {
				failure(w, 400, "invalid template page limit")
				return
			}
			limit = n
			bounded = true
		}
		if raw, ok := q["nextToken"]; ok {
			token := raw[0]
			var c templateregistry.Cursor
			wire, err := base64.RawURLEncoding.DecodeString(token)
			if len(token) > 8192 || err != nil || json.Unmarshal(wire, &c) != nil || c.Tenant != t.ID || c.Time.IsZero() || c.ID == "" || len(c.ID) > 256 || strings.ContainsAny(c.ID, "\x00\r\n") || c.Time.Year() < 1 || c.Time.Year() > 9999 {
				failure(w, 400, "invalid template page token")
				return
			}
			cursor = &c
		}
	}
	out := []templateView{}
	if !bounded || limit > 0 {
		for {
			batch := 1000
			if bounded && limit+1-uint64(len(out)) < uint64(batch) {
				batch = int(limit + 1 - uint64(len(out)))
			}
			records, err := s.Templates.Page(r.Context(), t.ID, cursor, batch)
			if err != nil {
				templateError(w, err)
				return
			}
			for _, record := range records {
				if record.Profile.Tenant != t.ID {
					templateError(w, metadata.ErrNotFound)
					return
				}
				out = append(out, presentTemplate(record))
			}
			if len(records) < batch || bounded && uint64(len(out)) > limit {
				break
			}
			last := records[len(records)-1]
			cursor = &templateregistry.Cursor{Tenant: t.ID, ID: last.Profile.ID, Time: last.CreatedAt}
		}
	}
	if bounded && uint64(len(out)) > limit {
		last := out[limit-1]
		wire, _ := json.Marshal(templateregistry.Cursor{Tenant: t.ID, ID: last.TemplateID, Time: last.CreatedAt})
		w.Header().Set("X-Next-Token", base64.RawURLEncoding.EncodeToString(wire))
		out = out[:limit]
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(out)
}

func (s *Server) templateInfo(w http.ResponseWriter, r *http.Request) {
	t := r.Context().Value(tenantKey{}).(auth.Tenant)
	q := r.URL.Query()
	if len(q["limit"]) > 1 || len(q["nextToken"]) > 1 {
		failure(w, 400, "ambiguous template build page")
		return
	}
	include := true
	if raw, ok := q["limit"]; ok {
		limit, err := strconv.ParseUint(raw[0], 10, 32)
		if err != nil {
			failure(w, 400, "invalid template build limit")
			return
		}
		include = limit > 0
	}
	if _, ok := q["nextToken"]; ok {
		include = false
	}
	record, err := s.Templates.Get(r.Context(), t.ID, r.PathValue("templateID"))
	if err != nil {
		templateError(w, err)
		return
	}
	if record.Profile.Tenant != t.ID {
		templateError(w, metadata.ErrNotFound)
		return
	}
	// Explicit field names follow the existing SDK build model.
	type buildView struct {
		ID          string    `json:"buildID"`
		Status      string    `json:"status"`
		CreatedAt   time.Time `json:"createdAt"`
		UpdatedAt   time.Time `json:"updatedAt"`
		FinishedAt  time.Time `json:"finishedAt"`
		CPUCount    int       `json:"cpuCount"`
		MemoryMB    int       `json:"memoryMB"`
		DiskSizeMB  int       `json:"diskSizeMB"`
		EnvdVersion string    `json:"envdVersion"`
	}
	view := presentTemplate(record)
	builds := []buildView{}
	if include {
		builds = append(builds, buildView{ID: view.BuildID, Status: view.BuildStatus, CreatedAt: view.CreatedAt, UpdatedAt: view.UpdatedAt, FinishedAt: view.UpdatedAt, CPUCount: view.CPUCount, MemoryMB: view.MemoryMB, DiskSizeMB: view.DiskSizeMB, EnvdVersion: view.EnvdVersion})
	}
	out := struct {
		TemplateID    string      `json:"templateID"`
		Public        bool        `json:"public"`
		Names         []string    `json:"names"`
		CreatedAt     time.Time   `json:"createdAt"`
		UpdatedAt     time.Time   `json:"updatedAt"`
		LastSpawnedAt *time.Time  `json:"lastSpawnedAt"`
		SpawnCount    int64       `json:"spawnCount"`
		Builds        []buildView `json:"builds"`
	}{TemplateID: view.TemplateID, Names: view.Names, CreatedAt: view.CreatedAt, UpdatedAt: view.UpdatedAt, Builds: builds}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(out)
}

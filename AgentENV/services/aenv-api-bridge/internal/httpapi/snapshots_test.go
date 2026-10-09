package httpapi

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"agentenv/services/aenv-api-bridge/internal/auth"
	"agentenv/services/aenv-api-bridge/internal/metadata"
	snapshotservice "agentenv/services/aenv-api-bridge/internal/snapshots"
)

type snapshotStub struct {
	calls     int
	pageCalls int
	rows      []snapshotservice.Info
}

func (s *snapshotStub) Create(_ context.Context, tenant auth.Tenant, b metadata.Sandbox, name, key string) (snapshotservice.Info, error) {
	if tenant.ID != "tenant" || b.ActorUID != "uid" || key != "stable" {
		panic("unbound capture")
	}
	s.calls++
	return snapshotservice.Info{ID: "snapshot", Names: []string{name}}, nil
}
func (s *snapshotStub) Get(_ context.Context, tenant, id string) (snapshotservice.Info, error) {
	if tenant != "tenant" {
		panic("foreign directory")
	}
	return snapshotservice.Info{ID: id}, nil
}
func (s *snapshotStub) Page(_ context.Context, tenant string, before *time.Time, id string, limit int, source, name string) ([]snapshotservice.Info, error) {
	if tenant != "tenant" {
		panic("foreign directory")
	}
	s.pageCalls++
	return s.rows, nil
}
func TestSnapshotCaptureAndTenantBoundary(t *testing.T) {
	_, store, control := setup(t)
	service := &snapshotStub{}
	h, err := (&Server{Auth: authStub{}, Store: store, Control: control, Snapshots: service, RequestTimeout: time.Second}).Handler()
	if err != nil {
		t.Fatal(err)
	}
	for _, body := range []string{`{}`, `{"name":"saved"}`} {
		w := request(h, http.MethodPost, "/sandboxes/sandbox/snapshots", body)
		if w.Code != 201 {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	for _, body := range []string{`null`, `{"name":""}`, `{} {}`, `{"other":true}`} {
		w := request(h, http.MethodPost, "/sandboxes/sandbox/snapshots", body)
		if w.Code != 400 {
			t.Fatal(body, w.Code)
		}
	}
	if service.calls != 2 {
		t.Fatal("invalid capture reached backend")
	}
	store.row.ActorAtespace = "foreign"
	w := request(h, http.MethodPost, "/sandboxes/sandbox/snapshots", `{}`)
	if w.Code != 404 || service.calls != 2 {
		t.Fatal("foreign capture reached backend")
	}
}
func TestSnapshotPageTokenBindsTenantAndFilters(t *testing.T) {
	_, store, control := setup(t)
	now := time.Now().UTC()
	service := &snapshotStub{rows: []snapshotservice.Info{{ID: "first", CreatedAt: now}, {ID: "second", CreatedAt: now.Add(-time.Second)}}}
	h, err := (&Server{Auth: authStub{}, Store: store, Control: control, Snapshots: service, RequestTimeout: time.Second}).Handler()
	if err != nil {
		t.Fatal(err)
	}
	w := request(h, "GET", "/snapshots?limit=1&name=saved", "")
	if w.Code != 200 || w.Header().Get("X-Next-Token") == "" {
		t.Fatal(w.Code, w.Body.String())
	}
	var rows []snapshotservice.Info
	if json.Unmarshal(w.Body.Bytes(), &rows) != nil || len(rows) != 1 {
		t.Fatal("incorrect page")
	}
	wire, _ := json.Marshal(snapshotCursor{Tenant: "foreign", ID: "first", Time: now, Name: "saved"})
	foreign := base64.RawURLEncoding.EncodeToString(wire)
	calls := service.pageCalls
	for _, path := range []string{"/snapshots?limit=0", "/snapshots?limit=1&limit=2", "/snapshots?name=saved&nextToken=" + foreign, "/snapshots?nextToken=" + w.Header().Get("X-Next-Token")} {
		r := request(h, "GET", path, "")
		if r.Code != 400 {
			t.Fatal(path, r.Code)
		}
	}
	if calls != service.pageCalls {
		t.Fatal("invalid cursor reached directory")
	}
}

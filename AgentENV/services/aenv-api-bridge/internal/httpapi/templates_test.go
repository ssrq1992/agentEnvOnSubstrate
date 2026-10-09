package httpapi

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"agentenv/services/aenv-api-bridge/internal/creation"
	"agentenv/services/aenv-api-bridge/internal/metadata"
	"agentenv/services/aenv-api-bridge/internal/templateregistry"
)

type templateFixture struct {
	rows  []templateregistry.Record
	calls int
	err   error
}

func (f *templateFixture) Get(_ context.Context, tenant, reference string) (templateregistry.Record, error) {
	f.calls++
	if f.err != nil {
		return templateregistry.Record{}, f.err
	}
	for _, r := range f.rows {
		if r.Profile.Tenant == tenant && (r.Profile.ID == reference || r.Profile.Alias == reference) {
			return r, nil
		}
	}
	return templateregistry.Record{}, metadata.ErrNotFound
}
func (f *templateFixture) Page(_ context.Context, tenant string, cursor *templateregistry.Cursor, limit int) ([]templateregistry.Record, error) {
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	out := []templateregistry.Record{}
	for _, r := range f.rows {
		if r.Profile.Tenant == tenant && (cursor == nil || r.CreatedAt.Before(cursor.Time) || r.CreatedAt.Equal(cursor.Time) && r.Profile.ID < cursor.ID) {
			out = append(out, r)
		}
	}
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}
func templateHandler(t *testing.T, f *templateFixture) http.Handler {
	t.Helper()
	s := &Server{Templates: f, Auth: authStub{}, Store: &storeStub{}, Control: &controlStub{}, RequestTimeout: time.Minute}
	h, err := s.Handler()
	if err != nil {
		t.Fatal(err)
	}
	return h
}
func TestTemplateListPaginationAndAlias(t *testing.T) {
	now := time.Now().UTC()
	f := &templateFixture{}
	for _, id := range []string{"c", "b", "a"} {
		f.rows = append(f.rows, templateregistry.Record{Profile: creation.Template{Tenant: "tenant", ID: id, Alias: "alias-" + id, NativeUID: "native-" + id, CPUCount: 1, MemoryMB: 128, DiskSizeMB: 1024, EnvdVersion: "0.5.1"}, CreatedAt: now, UpdatedAt: now})
	}
	h := templateHandler(t, f)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/v2/templates?limit=2", nil))
	var page []templateView
	if err := json.Unmarshal(w.Body.Bytes(), &page); err != nil || w.Code != 200 || len(page) != 2 || page[0].TemplateID != "c" {
		t.Fatal(w.Code, w.Body.String(), err)
	}
	token := w.Header().Get("X-Next-Token")
	if token == "" {
		t.Fatal("missing next token")
	}
	w = httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/v2/templates?limit=2&nextToken="+url.QueryEscape(token), nil))
	_ = json.Unmarshal(w.Body.Bytes(), &page)
	if w.Code != 200 || len(page) != 1 || page[0].TemplateID != "a" || w.Header().Get("X-Next-Token") != "" {
		t.Fatal(w.Code, w.Body.String())
	}
	w = httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/templates/aliases/alias-a", nil))
	var alias map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &alias)
	if w.Code != 200 || alias["templateID"] != "a" || alias["public"] != false {
		t.Fatal(w.Code, w.Body.String())
	}
	w = httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/templates/aliases/a", nil))
	if w.Code != 404 {
		t.Fatal("ID treated as alias", w.Code)
	}
	w = httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/templates", nil))
	_ = json.Unmarshal(w.Body.Bytes(), &page)
	if w.Code != 200 || len(page) != 3 {
		t.Fatal(w.Code, w.Body.String())
	}
	before := f.calls
	w = httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/v2/templates?limit=0", nil))
	if w.Code != 200 || strings.TrimSpace(w.Body.String()) != "[]" || f.calls != before {
		t.Fatal("zero limit changed", w.Code, w.Body.String())
	}
}
func TestTemplateRejectsInvalidPagesAndSanitizesCatalogFailures(t *testing.T) {
	f := &templateFixture{}
	h := templateHandler(t, f)
	raw, _ := json.Marshal(templateregistry.Cursor{Tenant: "other", ID: "id", Time: time.Now()})
	foreign := base64.RawURLEncoding.EncodeToString(raw)
	for _, path := range []string{"/v2/templates?limit=-1", "/v2/templates?limit=1&limit=2", "/v2/templates?nextToken=bad", "/v2/templates?nextToken=", "/v2/templates?nextToken=" + foreign, "/templates/aliases/invalid.name"} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		if w.Code != 400 {
			t.Fatal(path, w.Code, w.Body.String())
		}
	}
	if f.calls != 0 {
		t.Fatal("invalid query reached catalog")
	}
	f.err = errors.New("secret database password")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/templates", nil))
	if w.Code != 503 || strings.Contains(w.Body.String(), "password") {
		t.Fatal(w.Code, w.Body.String())
	}
}

func TestTemplateInfoPresentsCommittedImportedBuild(t *testing.T) {
	now := time.Now().UTC()
	f := &templateFixture{rows: []templateregistry.Record{{Profile: creation.Template{Tenant: "tenant", ID: "template", Alias: "python", NativeUID: "11111111-1111-4111-8111-111111111111", CPUCount: 1, MemoryMB: 128, DiskSizeMB: 1024, EnvdVersion: "0.5.1"}, CreatedAt: now, UpdatedAt: now}}}
	h := templateHandler(t, f)
	for _, path := range []string{"/templates/template", "/templates/template?limit=0", "/templates/template?nextToken=cursor"} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		var body struct {
			TemplateID string `json:"templateID"`
			Public     bool   `json:"public"`
			Builds     []struct {
				ID     string `json:"buildID"`
				Status string `json:"status"`
			} `json:"builds"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil || w.Code != 200 || body.TemplateID != "template" || body.Public {
			t.Fatal(w.Code, w.Body.String(), err)
		}
		count := 0
		if path == "/templates/template" {
			count = 1
		}
		if len(body.Builds) != count {
			t.Fatal("wrong build page", w.Body.String())
		}
		if count == 1 && (body.Builds[0].ID != f.rows[0].Profile.NativeUID || body.Builds[0].Status != "ready") {
			t.Fatal(w.Body.String())
		}
	}
}

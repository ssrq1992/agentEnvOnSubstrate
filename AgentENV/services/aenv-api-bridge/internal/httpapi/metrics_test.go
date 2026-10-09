package httpapi

import (
	"agentenv/services/aenv-api-bridge/internal/guestmetrics"
	"agentenv/services/aenv-api-bridge/internal/metadata"
	"context"
	"encoding/json"
	"net/http/httptest"
	"testing"
	"time"
)

type metricsStub struct {
	calls      int
	start, end *int64
}

func (s *metricsStub) History(_ context.Context, b metadata.Sandbox, start, end *int64) ([]guestmetrics.Sample, error) {
	s.calls++
	s.start = start
	s.end = end
	if b.Tenant != "tenant" || b.ActorUID != "uid" {
		panic("unbound history query")
	}
	return []guestmetrics.Sample{}, nil
}
func TestMetricsIntervalAndTenantValidation(t *testing.T) {
	_, store, control := setup(t)
	reader := &metricsStub{}
	h, err := (&Server{Auth: authStub{}, Store: store, Control: control, Metrics: reader, RequestTimeout: time.Second}).Handler()
	if err != nil {
		t.Fatal(err)
	}
	for _, query := range []string{"start=-1", "end=no", "start=3&end=2", "start=1&start=2", "start=9223372036854775808", "start=%zz"} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("GET", "/sandboxes/sandbox/metrics?"+query, nil))
		if w.Code != 400 {
			t.Fatalf("invalid interval accepted %s: %d", query, w.Code)
		}
	}
	if reader.calls != 0 {
		t.Fatal("invalid query reached history")
	}
	w := request(h, "GET", "/sandboxes/sandbox/metrics?start=2&end=2", "")
	if w.Code != 200 || reader.start == nil || *reader.start != 2 || *reader.end != 2 {
		t.Fatal("bounds not preserved", w.Code)
	}
	var result []guestmetrics.Sample
	if err = json.Unmarshal(w.Body.Bytes(), &result); err != nil || result == nil || len(result) != 0 {
		t.Fatal("empty history must be an array", err)
	}
	store.row.ActorAtespace = "other"
	if w = request(h, "GET", "/sandboxes/sandbox/metrics", ""); w.Code != 404 || reader.calls != 1 {
		t.Fatal("cross-tenant history query")
	}
}

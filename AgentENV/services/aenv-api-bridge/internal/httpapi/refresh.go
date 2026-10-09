package httpapi

import (
	"context"
	"encoding/json"
	"io"
	"mime"
	"net/http"

	"agentenv/services/aenv-api-bridge/internal/auth"
	"agentenv/services/aenv-api-bridge/internal/metadata"
)

type Refresher interface {
	Refresh(context.Context, auth.Tenant, metadata.Sandbox, *int) error
}

func (s *Server) refresh(w http.ResponseWriter, r *http.Request) {
	t, b, err := s.sandbox(r)
	if err != nil {
		report(w, err)
		return
	}
	var body *struct {
		Duration *int `json:"duration"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
	err = decoder.Decode(&body)
	if err != io.EOF {
		media, _, mediaErr := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if err != nil || mediaErr != nil || media != "application/json" {
			failure(w, 400, "invalid refresh request")
			return
		}
		var extra any
		if decoder.Decode(&extra) != io.EOF {
			failure(w, 400, "exactly one JSON request required")
			return
		}
	}
	var duration *int
	if body != nil {
		duration = body.Duration
	}
	if duration != nil && (*duration < 0 || uint64(*duration) > 4294967295) {
		failure(w, 400, "invalid refresh duration")
		return
	}
	if err = s.Refreshes.Refresh(r.Context(), t, b, duration); err != nil {
		report(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

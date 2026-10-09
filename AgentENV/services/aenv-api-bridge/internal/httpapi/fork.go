package httpapi

import (
	"agentenv/services/aenv-api-bridge/internal/auth"
	forkservice "agentenv/services/aenv-api-bridge/internal/fork"
	"agentenv/services/aenv-api-bridge/internal/metadata"
	"context"
	"encoding/json"
	"io"
	"mime"
	"net/http"
)

type Forker interface {
	Fork(context.Context, auth.Tenant, metadata.Sandbox, forkservice.Input, string) ([]forkservice.Outcome, error)
}

func (s *Server) fork(w http.ResponseWriter, r *http.Request) {
	t, b, err := s.sandbox(r)
	if err != nil {
		report(w, err)
		return
	}
	var input forkservice.Input
	if r.Body != nil && r.ContentLength != 0 {
		media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if err != nil || media != "application/json" {
			failure(w, 400, "JSON fork request required")
			return
		}
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&input); err != nil {
			failure(w, 400, "invalid fork request")
			return
		}
		var extra any
		if decoder.Decode(&extra) != io.EOF {
			failure(w, 400, "exactly one JSON request required")
			return
		}
	}
	key := r.Header.Get("Idempotency-Key")
	if len(r.Header.Values("Idempotency-Key")) > 1 || len(key) > 200 {
		failure(w, 400, "invalid idempotency key")
		return
	}
	outcomes, err := s.Forks.Fork(r.Context(), t, b, input, key)
	if err != nil {
		report(w, err)
		return
	}
	type result struct {
		Sandbox *sandboxInfo `json:"sandbox,omitempty"`
		Error   *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error,omitempty"`
	}
	response := make([]result, len(outcomes))
	for n, outcome := range outcomes {
		if outcome.Sandbox != nil {
			info, err := s.readInfo(r.Context(), t, *outcome.Sandbox, true)
			if err == nil {
				response[n].Sandbox = &info
				continue
			}
			outcome.Error = "fork child information is unavailable"
		}
		response[n].Error = &struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		}{500, outcome.Error}
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(201)
	_ = json.NewEncoder(w).Encode(response)
}

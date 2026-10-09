package httpapi

import (
	"agentenv/services/aenv-api-bridge/internal/auth"
	"agentenv/services/aenv-api-bridge/internal/metadata"
	"context"
	"net/http"
)

type Pauser interface {
	Pause(context.Context, auth.Tenant, metadata.Sandbox) error
}

func (s *Server) pause(w http.ResponseWriter, r *http.Request) {
	t, b, err := s.sandbox(r)
	if err != nil {
		report(w, err)
		return
	}
	if err = s.Pauses.Pause(r.Context(), t, b); err != nil {
		report(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

package httpapi

import (
	"context"
	"encoding/json"
	"io"
	"mime"
	"net/http"

	"agentenv/services/aenv-api-bridge/internal/auth"
	"agentenv/services/aenv-api-bridge/internal/creation"
	"agentenv/services/aenv-api-bridge/internal/metadata"
	pb "github.com/agent-substrate/substrate/pkg/proto/ateapipb"
)

type ColdCreator interface {
	CreateCold(context.Context, auth.Tenant, creation.ColdInput, string) (metadata.Sandbox, *pb.ConnectActorResponse, error)
}

func (s *Server) cold(w http.ResponseWriter, r *http.Request) {
	media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || media != "application/json" {
		failure(w, 400, "JSON cold creation request required")
		return
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	decoder.DisallowUnknownFields()
	var input *creation.ColdInput
	if err = decoder.Decode(&input); err != nil || input == nil {
		failure(w, 400, "invalid cold creation request")
		return
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		failure(w, 400, "exactly one JSON request required")
		return
	}
	key := r.Header.Get("Idempotency-Key")
	if len(r.Header.Values("Idempotency-Key")) > 1 || len(key) > 200 {
		failure(w, 400, "invalid idempotency key")
		return
	}
	tenant := r.Context().Value(tenantKey{}).(auth.Tenant)
	b, connection, err := s.ColdCreations.CreateCold(r.Context(), tenant, *input, key)
	if err != nil {
		report(w, err)
		return
	}
	w.Header().Set("X-Agentenv-Sandbox-Id", b.ExternalID)
	s.writeConnection(w, r, b, connection, true)
}

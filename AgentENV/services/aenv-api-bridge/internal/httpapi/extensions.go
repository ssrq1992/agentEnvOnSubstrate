package httpapi

import (
	"agentenv/services/aenv-api-bridge/internal/auth"
	extensionservice "agentenv/services/aenv-api-bridge/internal/extensions"
	"agentenv/services/aenv-api-bridge/internal/metadata"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
)

type Extensions interface {
	Get(context.Context, auth.Tenant, metadata.Sandbox) (json.RawMessage, error)
	Patch(context.Context, auth.Tenant, metadata.Sandbox, json.RawMessage, string) (json.RawMessage, error)
}

func (s *Server) extensionParams(w http.ResponseWriter, r *http.Request) {
	t, b, err := s.sandbox(r)
	if err != nil {
		report(w, err)
		return
	}
	var result json.RawMessage
	if r.Method == http.MethodGet || r.Method == http.MethodHead {
		result, err = s.Extensions.Get(r.Context(), t, b)
	} else {
		media, _, e := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if e != nil || media != "application/json" {
			failure(w, 400, "JSON extension request required")
			return
		}
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 65536))
		var patch json.RawMessage
		if e = decoder.Decode(&patch); e != nil {
			failure(w, 400, "invalid extension object")
			return
		}
		var object map[string]json.RawMessage
		if e = json.Unmarshal(patch, &object); e != nil || object == nil {
			failure(w, 400, "extension patch must be a JSON object")
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
		result, err = s.Extensions.Patch(r.Context(), t, b, patch, key)
	}
	if errors.Is(err, extensionservice.ErrRejected) {
		failure(w, 400, "extension patch rejected without effect")
		return
	}
	if err != nil {
		report(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(result)
}

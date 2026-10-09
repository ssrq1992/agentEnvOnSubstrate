package httpapi

import (
	"agentenv/services/aenv-api-bridge/internal/auth"
	"agentenv/services/aenv-api-bridge/internal/metadata"
	"context"
	"encoding/json"
	pb "github.com/agent-substrate/substrate/pkg/proto/ateapipb"
	"io"
	"mime"
	"net/http"
)

type Connector interface {
	Connect(context.Context, auth.Tenant, metadata.Sandbox, int, bool) (*pb.ConnectActorResponse, bool, error)
}

func (s *Server) connect(w http.ResponseWriter, r *http.Request) {
	t, b, err := s.sandbox(r)
	if err != nil {
		report(w, err)
		return
	}
	v2 := r.URL.Path == "/v2/sandboxes/"+r.PathValue("sandboxID")+"/connect"
	legacy := r.URL.Path == "/sandboxes/"+r.PathValue("sandboxID")+"/resume"
	var body *struct {
		Timeout *int `json:"timeout"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
	err = decoder.Decode(&body)
	seconds := 300
	if legacy {
		seconds = 15
	}
	if err == io.EOF && v2 {
		// Optional v2 body defaults to 300 seconds.
	} else {
		media, _, mediaErr := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if err != nil || body == nil || mediaErr != nil || media != "application/json" {
			failure(w, 400, "JSON connect request required")
			return
		}
		var extra any
		if decoder.Decode(&extra) != io.EOF {
			failure(w, 400, "exactly one JSON request required")
			return
		}
		if body.Timeout != nil {
			seconds = *body.Timeout
		} else if !v2 && !legacy {
			failure(w, 400, "timeout required")
			return
		}
	}
	if seconds < 0 || uint64(seconds) > 4294967295 || v2 && seconds == 0 {
		failure(w, 400, "invalid timeout")
		return
	}
	connection, resumed, err := s.Connections.Connect(r.Context(), t, b, seconds, legacy)
	if err != nil {
		report(w, err)
		return
	}
	s.writeConnection(w, r, b, connection, resumed || legacy)
}

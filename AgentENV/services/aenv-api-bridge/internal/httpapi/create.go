package httpapi

import (
	"agentenv/services/aenv-api-bridge/internal/auth"
	"agentenv/services/aenv-api-bridge/internal/creation"
	"agentenv/services/aenv-api-bridge/internal/metadata"
	"context"
	"encoding/json"
	pb "github.com/agent-substrate/substrate/pkg/proto/ateapipb"
	"io"
	"mime"
	"net/http"
)

type Creator interface {
	Create(context.Context, auth.Tenant, creation.Input, string, bool) (metadata.Sandbox, *pb.ConnectActorResponse, error)
}
type ProfileReader interface {
	Profile(context.Context, metadata.Sandbox) (metadata.Profile, error)
}

func (s *Server) create(w http.ResponseWriter, r *http.Request) {
	t := r.Context().Value(tenantKey{}).(auth.Tenant)
	media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || media != "application/json" {
		failure(w, 400, "JSON creation request required")
		return
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	decoder.DisallowUnknownFields()
	var input *creation.Input
	if err = decoder.Decode(&input); err != nil || input == nil {
		failure(w, 400, "invalid creation request")
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
	b, connection, err := s.Creations.Create(r.Context(), t, *input, key, r.URL.Path == "/v2/sandboxes")
	if err != nil {
		report(w, err)
		return
	}
	s.writeConnection(w, r, b, connection, true)
}
func (s *Server) writeConnection(w http.ResponseWriter, r *http.Request, b metadata.Sandbox, connection *pb.ConnectActorResponse, created bool) {
	template := connection.GetActor().GetActorTemplate().GetName()
	if connection.GetActor().GetMetadata().GetUid() != b.ActorUID || connection.GetEnvdAccessToken() == "" || connection.GetEnvdVersion() == "" || template == "" {
		failure(w, 503, "connection could not be confirmed")
		return
	}
	alias := b.TemplateAlias
	if s.Profiles != nil {
		profile, err := s.Profiles.Profile(r.Context(), b)
		if err != nil {
			report(w, err)
			return
		}
		template = profile.TemplateID
		alias = profile.Alias
	}
	envdToken := connection.EnvdAccessToken
	var trafficToken *string
	if s.Data != nil {
		var err error
		envdToken, trafficToken, err = s.Data.Credentials(r.Context(), b)
		if err != nil {
			report(w, err)
			return
		}
	}
	response := struct {
		TemplateID         string  `json:"templateID"`
		SandboxID          string  `json:"sandboxID"`
		Alias              string  `json:"alias,omitempty"`
		ClientID           string  `json:"clientID"`
		EnvdVersion        string  `json:"envdVersion"`
		EnvdAccessToken    string  `json:"envdAccessToken,omitempty"`
		Domain             string  `json:"domain"`
		TrafficAccessToken *string `json:"trafficAccessToken"`
	}{template, b.ExternalID, alias, "", connection.EnvdVersion, envdToken, s.SandboxDomain, trafficToken}
	w.Header().Set("Content-Type", "application/json")
	if created {
		w.WriteHeader(201)
	} else {
		w.WriteHeader(200)
	}
	_ = json.NewEncoder(w).Encode(response)
}

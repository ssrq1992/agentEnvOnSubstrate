package httpapi

import (
	"agentenv/services/aenv-api-bridge/internal/metadata"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	pb "github.com/agent-substrate/substrate/pkg/proto/ateapipb"
	"io"
	"mime"
	"net/http"
)

func (s *Server) network(w http.ResponseWriter, r *http.Request) {
	t, b, err := s.sandbox(r)
	if err != nil {
		report(w, err)
		return
	}
	media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || media != "application/json" {
		failure(w, 400, "JSON network request required")
		return
	}
	var body *struct {
		AllowOut []string `json:"allowOut"`
		DenyOut  []string `json:"denyOut"`
		Internet *bool    `json:"allow_internet_access"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	if err = decoder.Decode(&body); err != nil || body == nil {
		failure(w, 400, "invalid network request")
		return
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		failure(w, 400, "exactly one JSON request required")
		return
	}
	policy := &pb.AgentENVNetworkPolicy{AllowOut: body.AllowOut, DenyOut: body.DenyOut}
	if body.Internet != nil {
		if *body.Internet {
			policy.Base = pb.AgentENVNetworkPolicy_ALLOW
		} else {
			policy.Base = pb.AgentENVNetworkPolicy_DENY
		}
	}
	key := r.Header.Get("Idempotency-Key")
	if len(r.Header.Values("Idempotency-Key")) > 1 || len(key) > 200 {
		failure(w, 400, "invalid idempotency key")
		return
	}
	if key == "" {
		var token [16]byte
		if _, err = rand.Read(token[:]); err != nil {
			report(w, err)
			return
		}
		key = hex.EncodeToString(token[:])
	}
	payload, _ := json.Marshal(struct {
		Tenant, ID, UID string
		Policy          *pb.AgentENVNetworkPolicy
	}{t.ID, b.ExternalID, b.ActorUID, policy})
	digest := sha256.Sum256(payload)
	intent := metadata.Request{Tenant: t.ID, ExternalID: b.ExternalID, ID: "http-" + key, Kind: "policy", Digest: hex.EncodeToString(digest[:])}
	receipt, err := s.Store.Reserve(r.Context(), intent)
	if err != nil {
		report(w, err)
		return
	}
	if receipt.State == "rejected" {
		failure(w, 409, "request was rejected")
		return
	}
	if receipt.State != "completed" {
		if len(receipt.Result) == 0 {
			revision, e := s.Control.NetworkPolicyRevision(r.Context(), t, b)
			if e != nil {
				report(w, e)
				return
			}
			prepared, _ := json.Marshal(struct {
				Revision uint64 `json:"revision"`
			}{revision})
			receipt, err = s.Store.Prepare(r.Context(), intent, prepared)
			if err != nil {
				report(w, err)
				return
			}
		}
		if receipt.State == "completed" {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if receipt.State != "pending" {
			failure(w, 409, "request was rejected")
			return
		}
		var prepared struct {
			Revision *uint64 `json:"revision"`
		}
		if err = json.Unmarshal(receipt.Result, &prepared); err != nil || prepared.Revision == nil {
			failure(w, 503, "policy preparation is unavailable")
			return
		}
		if err = s.Control.ReplaceNetworkPolicy(r.Context(), t, b, policy, *prepared.Revision); err != nil {
			report(w, err)
			return
		}
		if err = s.Store.Complete(r.Context(), intent, false, []byte(`{}`)); err != nil {
			report(w, err)
			return
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

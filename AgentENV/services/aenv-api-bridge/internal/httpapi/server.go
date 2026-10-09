// Package httpapi exposes the AgentENV compatibility API through the trusted
// Gateway. Runtime state and deletion decisions remain owned by Substrate.
package httpapi

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"strings"
	"time"

	"agentenv/services/aenv-api-bridge/internal/auth"
	"agentenv/services/aenv-api-bridge/internal/metadata"
	pb "github.com/agent-substrate/substrate/pkg/proto/ateapipb"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type Authenticator interface {
	Authenticate(*http.Request) (auth.Tenant, error)
}
type Store interface {
	Get(context.Context, string, string) (metadata.Sandbox, error)
	Reserve(context.Context, metadata.Request) (metadata.Request, error)
	ConfirmDeleted(context.Context, metadata.Request) error
	Complete(context.Context, metadata.Request, bool, []byte) error
	Prepare(context.Context, metadata.Request, []byte) (metadata.Request, error)
	Extend(context.Context, string, string, int64, int) error
}
type Control interface {
	Delete(context.Context, auth.Tenant, metadata.Sandbox) error
	ReplaceNetworkPolicy(context.Context, auth.Tenant, metadata.Sandbox, *pb.AgentENVNetworkPolicy, uint64) error
	NetworkPolicyRevision(context.Context, auth.Tenant, metadata.Sandbox) (uint64, error)
}
type DataPlane interface {
	Handle(http.ResponseWriter, *http.Request) bool
	Credentials(context.Context, metadata.Sandbox) (string, *string, error)
}

type Server struct {
	Snapshots      Snapshots
	ColdCreations  ColdCreator
	Refreshes      Refresher
	Templates      Templates
	Extensions     Extensions
	Forks          Forker
	Infos          InfoStore
	InfoControl    InfoControl
	Creations      Creator
	Profiles       ProfileReader
	Data           DataPlane
	Auth           Authenticator
	Store          Store
	Control        Control
	LatestMetrics  LatestMetricsReader
	Metrics        MetricsReader
	Pauses         Pauser
	Connections    Connector
	SandboxDomain  string
	RequestTimeout time.Duration
}

func (s *Server) Handler() (http.Handler, error) {
	if s.Auth == nil || s.Store == nil || s.Control == nil || s.RequestTimeout <= 0 {
		return nil, fmt.Errorf("complete HTTP API configuration required")
	}
	if s.Connections != nil && (s.SandboxDomain == "" || strings.ContainsAny(s.SandboxDomain, "/\\?#@ \r\n")) {
		return nil, fmt.Errorf("explicit sandbox DNS domain required")
	}
	mux := http.NewServeMux()
	if s.Templates != nil {
		mux.HandleFunc("GET /templates", s.templates)
		mux.HandleFunc("GET /templates/{templateID}", s.templateInfo)
		mux.HandleFunc("GET /v2/templates", s.templates)
		mux.HandleFunc("GET /templates/aliases/{alias}", s.templateAlias)
	}
	if s.Extensions != nil {
		mux.HandleFunc("GET /sandboxes/{sandboxID}/custom-extension-params", s.extensionParams)
		mux.HandleFunc("PATCH /sandboxes/{sandboxID}/custom-extension-params", s.extensionParams)
	}
	if s.Forks != nil {
		if s.Infos == nil || s.InfoControl == nil {
			return nil, fmt.Errorf("fork presentation dependencies required")
		}
		mux.HandleFunc("POST /sandboxes/{sandboxID}/fork", s.fork)
	}
	if s.Infos != nil && s.InfoControl != nil {
		mux.HandleFunc("GET /sandboxes/{sandboxID}", s.info)
		mux.HandleFunc("GET /sandboxes", s.list)
		mux.HandleFunc("GET /v2/sandboxes", s.list)
	}
	if s.Creations != nil {
		mux.HandleFunc("POST /sandboxes", s.create)
		mux.HandleFunc("POST /v2/sandboxes", s.create)
	}
	if s.Connections != nil {
		mux.HandleFunc("POST /sandboxes/{sandboxID}/connect", s.connect)
		mux.HandleFunc("POST /v2/sandboxes/{sandboxID}/connect", s.connect)
		mux.HandleFunc("POST /sandboxes/{sandboxID}/resume", s.connect)
	}
	if s.Pauses != nil {
		mux.HandleFunc("POST /sandboxes/{sandboxID}/pause", s.pause)
	}
	if s.Metrics != nil {
		mux.HandleFunc("GET /sandboxes/{sandboxID}/metrics", s.metrics)
	}
	if s.Refreshes != nil {
		mux.HandleFunc("POST /sandboxes/{sandboxID}/refreshes", s.refresh)
	}
	if s.LatestMetrics != nil {
		if s.InfoControl == nil {
			return nil, fmt.Errorf("batch metrics runtime reader required")
		}
		mux.HandleFunc("GET /sandboxes/metrics", s.batchMetrics)
	}
	if s.ColdCreations != nil {
		mux.HandleFunc("POST /sandboxes/cold", s.cold)
	}
	if s.Snapshots != nil {
		mux.HandleFunc("POST /sandboxes/{sandboxID}/snapshots", s.capture)
		mux.HandleFunc("GET /snapshots", s.snapshots)
		mux.HandleFunc("GET /snapshots/{snapshotID}", s.snapshot)
	}
	mux.HandleFunc("PUT /sandboxes/{sandboxID}/network", s.network)
	mux.HandleFunc("DELETE /sandboxes/{sandboxID}", s.delete)
	mux.HandleFunc("POST /sandboxes/{sandboxID}/timeout", s.timeout)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		if s.Data != nil && s.Data.Handle(w, r) {
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), s.RequestTimeout)
		defer cancel()
		// Authenticate even unknown routes; neither caller headers nor URL path
		// can select an alternate tenant or control-plane principal.
		tenant, err := s.Auth.Authenticate(r)
		if err != nil {
			failure(w, http.StatusUnauthorized, "authentication required")
			return
		}
		mux.ServeHTTP(w, r.WithContext(context.WithValue(ctx, tenantKey{}, tenant)))
	}), nil
}

type tenantKey struct{}

func failure(w http.ResponseWriter, code int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	}{code, message})
}
func report(w http.ResponseWriter, err error) {
	switch {
	case status.Code(err) == codes.InvalidArgument:
		failure(w, 400, "invalid network policy")
	case errors.Is(err, metadata.ErrNotFound), status.Code(err) == codes.NotFound:
		failure(w, 404, "sandbox not found")
	case errors.Is(err, metadata.ErrConflict), status.Code(err) == codes.FailedPrecondition, status.Code(err) == codes.Aborted:
		failure(w, 409, "sandbox operation conflicts with current state")
	case errors.Is(err, context.DeadlineExceeded), status.Code(err) == codes.DeadlineExceeded:
		failure(w, 504, "operation outcome is pending; retry the request")
	case status.Code(err) == codes.PermissionDenied:
		failure(w, 403, "operation not permitted")
	default:
		failure(w, 503, "sandbox operation could not be confirmed")
	}
}
func (s *Server) sandbox(r *http.Request) (auth.Tenant, metadata.Sandbox, error) {
	t := r.Context().Value(tenantKey{}).(auth.Tenant)
	b, err := s.Store.Get(r.Context(), t.ID, r.PathValue("sandboxID"))
	if err != nil {
		return t, b, err
	}
	if b.Tenant != t.ID || b.ActorAtespace != t.Atespace || b.ExternalID != r.PathValue("sandboxID") {
		return t, b, metadata.ErrNotFound
	}

	if s.Profiles != nil {
		if _, err := s.Profiles.Profile(r.Context(), b); err != nil {
			return t, b, err
		}
	}
	return t, b, nil
}
func (s *Server) delete(w http.ResponseWriter, r *http.Request) {
	if locking, ok := s.Store.(interface {
		LockOperation(context.Context, string, string) (func(), error)
	}); ok {
		tenant := r.Context().Value(tenantKey{}).(auth.Tenant)
		unlock, err := locking.LockOperation(r.Context(), tenant.ID, r.PathValue("sandboxID"))
		if err != nil {
			report(w, err)
			return
		}
		defer unlock()
	}
	t, b, err := s.sandbox(r)
	if err != nil {
		report(w, err)
		return
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
	payload, _ := json.Marshal([]string{"delete", b.Tenant, b.ExternalID, b.ActorUID})
	digest := sha256.Sum256(payload)
	intent := metadata.Request{Tenant: t.ID, ExternalID: b.ExternalID, ID: "http-" + key, Kind: "delete", Digest: hex.EncodeToString(digest[:])}
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
		if err = s.Control.Delete(r.Context(), t, b); err != nil {
			report(w, err)
			return
		}
		if err = s.Store.ConfirmDeleted(r.Context(), intent); err != nil {
			report(w, err)
			return
		}
	}
	w.WriteHeader(http.StatusNoContent)
}
func (s *Server) timeout(w http.ResponseWriter, r *http.Request) {
	_, b, err := s.sandbox(r)
	if err != nil {
		report(w, err)
		return
	}
	contentType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || contentType != "application/json" {
		failure(w, 400, "JSON timeout request required")
		return
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
	var body struct {
		Timeout *int `json:"timeout"`
	}
	if err = decoder.Decode(&body); err != nil || body.Timeout == nil || *body.Timeout < 0 || uint64(*body.Timeout) > 4294967295 {
		failure(w, 400, "timeout must be an integer between 0 and 4294967295 seconds")
		return
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		failure(w, 400, "exactly one JSON request required")
		return
	}
	if err = s.Store.Extend(r.Context(), b.Tenant, b.ExternalID, b.Revision, *body.Timeout); err != nil {
		report(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

package dataplane

import (
	"agentenv/services/aenv-api-bridge/internal/auth"
	"agentenv/services/aenv-api-bridge/internal/metadata"
	"context"
	"fmt"
	pb "github.com/agent-substrate/substrate/pkg/proto/ateapipb"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type Store interface {
	LookupExternal(context.Context, string) (metadata.Sandbox, error)
	Access(context.Context, metadata.Sandbox) (metadata.Access, error)
}
type Control interface {
	Get(context.Context, auth.Tenant, metadata.Sandbox) (*pb.Actor, error)
	Connect(context.Context, auth.Tenant, metadata.Sandbox) (*pb.ConnectActorResponse, error)
}
type Lifecycle interface {
	Connect(context.Context, auth.Tenant, metadata.Sandbox, int, bool) (*pb.ConnectActorResponse, bool, error)
}
type Gateway interface{ TrustedGateway(*http.Request) bool }
type Handler struct {
	Domain            string
	Target            *url.URL
	Transport         http.RoundTripper
	Store             Store
	Control           Control
	Lifecycle         Lifecycle
	Gateway           Gateway
	Tokens            *Tokens
	Tenants           map[string]auth.Tenant
	AutoResumeTimeout int
}

func (h *Handler) Validate() error {
	if !validDomain(h.Domain) || h.Target == nil || h.Target.Scheme != "https" || h.Target.Hostname() == "" || h.Target.User != nil || h.Target.Path != "" || h.Target.RawQuery != "" || h.Target.Fragment != "" || h.Transport == nil || h.Store == nil || h.Control == nil || h.Lifecycle == nil || h.Gateway == nil || h.Tokens == nil || len(h.Tenants) == 0 || h.AutoResumeTimeout < 1 {
		return fmt.Errorf("complete data proxy configuration required")
	}
	return nil
}
func validDomain(d string) bool {
	if len(d) == 0 || len(d) > 253 || d != strings.ToLower(d) {
		return false
	}
	for _, label := range strings.Split(d, ".") {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, c := range label {
			if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-') {
				return false
			}
		}
	}
	return true
}
func single(r *http.Request, key string) string {
	v := r.Header.Values(key)
	if len(v) != 1 {
		return ""
	}
	return v[0]
}
func routing(r *http.Request, domain string) (id string, port int, matched bool, err error) {
	host := strings.ToLower(r.Host)
	if h, _, e := net.SplitHostPort(host); e == nil {
		host = h
	}
	host = strings.TrimSuffix(host, ".")
	suffix := "." + domain
	if strings.HasSuffix(host, suffix) {
		matched = true
		label := strings.TrimSuffix(host, suffix)
		p, s, ok := strings.Cut(label, "-")
		if !ok || !validDomain(s) || strings.Contains(s, ".") {
			return "", 0, true, fmt.Errorf("invalid data host")
		}
		port, err = strconv.Atoi(p)
		id = s
	} else if r.URL.Path == "/proxy" || strings.HasPrefix(r.URL.Path, "/proxy/") {
		matched = true
		id = single(r, "X-Agentenv-Sandbox-Id")
		if id == "" {
			id = single(r, "E2b-Sandbox-Id")
		}
		p := single(r, "X-Agentenv-Target-Port")
		if p == "" {
			p = single(r, "E2b-Sandbox-Port")
		}
		port, err = strconv.Atoi(p)
		if !validDomain(id) || strings.Contains(id, ".") {
			err = fmt.Errorf("invalid sandbox ID")
		}
	}
	if matched && (port < 1 || port > 65535) {
		err = fmt.Errorf("invalid data port")
	}
	return
}

// Handle returns false only for management API requests. Credential failures
// on data hosts never fall through to API-key authentication.
func (h *Handler) Handle(w http.ResponseWriter, r *http.Request) bool {
	id, port, matched, err := routing(r, h.Domain)
	if !matched {
		return false
	}
	w.Header().Set("Cache-Control", "no-store")
	fail := func(code int) { http.Error(w, http.StatusText(code), code) }
	if !h.Gateway.TrustedGateway(r) {
		fail(401)
		return true
	}
	if err != nil {
		fail(400)
		return true
	}
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
	defer cancel()
	b, err := h.Store.LookupExternal(ctx, id)
	if err != nil {
		fail(404)
		return true
	}
	t, ok := h.Tenants[b.Tenant]
	if !ok || t.Atespace != b.ActorAtespace || b.ActorUID == "" {
		fail(404)
		return true
	}
	access, err := h.Store.Access(ctx, b)
	if err != nil {
		fail(503)
		return true
	}
	envd := port == access.EnvdPort
	authorized := access.AllowPublicTraffic
	if envd {
		authorized = !access.Secure || h.Tokens.Valid(b, "envd", single(r, "X-Access-Token"))
	} else if !authorized {
		authorized = h.Tokens.Valid(b, "traffic", single(r, "E2b-Traffic-Access-Token"))
	}
	if !authorized {
		fail(401)
		return true
	}
	actor, err := h.Control.Get(ctx, t, b)
	if err != nil {
		fail(503)
		return true
	}
	var connection *pb.ConnectActorResponse
	switch actor.GetStatus().GetState() {
	case pb.ActorState_ACTOR_STATE_RUNNING:
		connection, err = h.Control.Connect(ctx, t, b)
	case pb.ActorState_ACTOR_STATE_SUSPENDED:
		if !access.AutoResume {
			fail(409)
			return true
		}
		connection, _, err = h.Lifecycle.Connect(ctx, t, b, h.AutoResumeTimeout, true)
	default:
		fail(409)
		return true
	}
	if err != nil {
		fail(503)
		return true
	}
	actor = connection.GetActor()
	generation := actor.GetStatus().GetWorkerAssignment().GetAssignmentGeneration()
	if actor.GetMetadata().GetUid() != b.ActorUID || actor.GetStatus().GetState() != pb.ActorState_ACTOR_STATE_RUNNING || generation == 0 || connection.GetEnvdAccessToken() == "" {
		fail(503)
		return true
	}
	// A stream's lifetime belongs to the client; only resolution has a deadline.
	_ = http.NewResponseController(w).EnableFullDuplex()
	proxy := &httputil.ReverseProxy{Transport: h.Transport, FlushInterval: -1,
		Rewrite: func(p *httputil.ProxyRequest) {
			p.SetURL(h.Target)
			p.Out.Host = p.In.Host
			if !strings.HasSuffix(strings.TrimSuffix(strings.ToLower(strings.Split(p.In.Host, ":")[0]), "."), "."+h.Domain) {
				p.Out.URL.Path = strings.TrimPrefix(p.Out.URL.Path, "/proxy")
				if p.Out.URL.RawPath != "" {
					p.Out.URL.RawPath = strings.TrimPrefix(p.Out.URL.RawPath, "/proxy")
				}
				if p.Out.URL.Path == "" {
					p.Out.URL.Path = "/"
				}
			}
			for _, key := range []string{"X-API-Key", "X-Access-Token", "E2b-Traffic-Access-Token", "X-Agentenv-Tenant", "X-Substrate-Actor", "X-Agentenv-Node-Id", "X-Agentenv-Sandbox-Id", "X-Agentenv-Target-Port", "E2b-Sandbox-Id", "E2b-Sandbox-Port", "Ate-Target-Actor", "Ate-Target-Actor-Uid", "Ate-Target-Assignment-Generation", "X-Ate-Target-Port"} {
				p.Out.Header.Del(key)
			}
			p.Out.Header.Set("Ate-Target-Actor", b.ActorAtespace+"/"+b.ActorName)
			p.Out.Header.Set("Ate-Target-Actor-Uid", b.ActorUID)
			p.Out.Header.Set("Ate-Target-Assignment-Generation", strconv.FormatUint(generation, 10))
			p.Out.Header.Set("X-Ate-Target-Port", strconv.Itoa(port))
			if envd {
				p.Out.Header.Set("X-Access-Token", connection.EnvdAccessToken)
			}
			p.SetXForwarded()
		}, ErrorHandler: func(w http.ResponseWriter, _ *http.Request, _ error) {
			http.Error(w, "sandbox data route unavailable", 502)
		}}
	proxy.ServeHTTP(w, r)
	return true
}

// Credentials returns only sandbox-scoped proxy credentials. Per-allocation
// guest secrets are never exposed when the compatibility data proxy is enabled.
func (h *Handler) Credentials(ctx context.Context, b metadata.Sandbox) (string, *string, error) {
	a, err := h.Store.Access(ctx, b)
	if err != nil {
		return "", nil, err
	}
	envd := ""
	if a.Secure {
		envd = h.Tokens.Token(b, "envd")
	}
	var traffic *string
	if !a.AllowPublicTraffic {
		v := h.Tokens.Token(b, "traffic")
		traffic = &v
	}
	return envd, traffic, nil
}

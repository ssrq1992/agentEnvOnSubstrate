// Copyright 2026 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
// http://www.apache.org/licenses/LICENSE-2.0
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package main

import (
	"context"
	"crypto/tls"
	"errors"
	"flag"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/agent-substrate/substrate/internal/localca"
	"github.com/agent-substrate/substrate/internal/podidentityissuer"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

func main() {
	listen := flag.String("listen", ":8443", "HTTPS listen address")
	ca := flag.String("ca-pool", "", "localca signing-pool file")
	cert := flag.String("tls-cert", "", "issuer HTTPS certificate file")
	key := flag.String("tls-key", "", "issuer HTTPS private key file")
	allow := flag.String("service-accounts", "", "comma-separated namespace/service-account allowlist")
	flag.Parse()
	if *ca == "" || *cert == "" || *key == "" || *allow == "" {
		slog.Error("ca-pool, tls-cert, tls-key and service-accounts are required")
		os.Exit(1)
	}
	pool, err := localca.NewRefreshingPool(*ca)
	if err != nil {
		slog.Error("load signing pool", "error", err)
		os.Exit(1)
	}
	config, err := rest.InClusterConfig()
	if err != nil {
		slog.Error("load Kubernetes config", "error", err)
		os.Exit(1)
	}
	client, err := kubernetes.NewForConfig(config)
	if err != nil {
		slog.Error("create Kubernetes client", "error", err)
		os.Exit(1)
	}
	accounts := map[string]bool{}
	for _, account := range strings.Split(*allow, ",") {
		p := strings.Split(strings.TrimSpace(account), "/")
		if len(p) != 2 || p[0] == "" || p[1] == "" {
			slog.Error("invalid service account allowlist")
			os.Exit(1)
		}
		accounts[p[0]+"/"+p[1]] = true
	}
	server := &http.Server{Addr: *listen, Handler: &podidentityissuer.Issuer{Client: client, Pool: pool, AllowedServiceAccounts: accounts},
		ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 25 * time.Second, WriteTimeout: 25 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 16 << 10,
		TLSConfig: &tls.Config{MinVersion: tls.VersionTLS13, GetCertificate: func(*tls.ClientHelloInfo) (*tls.Certificate, error) {
			c, e := tls.LoadX509KeyPair(*cert, *key)
			return &c, e
		}}}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdown)
	}()
	if err := server.ListenAndServeTLS("", ""); err != nil && !errors.Is(err, http.ErrServerClosed) {
		slog.Error("serve issuer", "error", err)
		os.Exit(1)
	}
}

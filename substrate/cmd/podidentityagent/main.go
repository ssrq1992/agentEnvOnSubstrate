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
	"flag"
	"github.com/agent-substrate/substrate/internal/podidentityissuer"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"
)

func main() {
	endpoint := flag.String("issuer", "", "HTTPS issuer endpoint")
	token := flag.String("token", "/run/podidentity-token/token", "projected audience-scoped token")
	roots := flag.String("roots", "/run/podidentity-roots/ca.crt", "issuer trust roots")
	bundle := flag.String("bundle", "/run/podidentity/credential.pem", "atomic credential bundle destination")
	mode := flag.Uint("bundle-mode", 0600, "credential permissions: 0600 or 0640 for a dedicated shared Pod group")
	once := flag.Bool("once", false, "issue once for an init container")
	flag.Parse()
	if *endpoint == "" || (*mode != 0600 && *mode != 0640) {
		slog.Error("issuer and credential mode 0600 or 0640 are required")
		os.Exit(1)
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	for {
		renewAt, err := podidentityissuer.Renew(ctx, *endpoint, *token, *roots, *bundle, os.FileMode(*mode))
		if err != nil {
			slog.Error("Pod credential renewal failed", "error", err)
			if *once {
				os.Exit(1)
			}
			renewAt = time.Now().Add(10 * time.Second)
		} else if *once {
			return
		}
		timer := time.NewTimer(time.Until(renewAt))
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

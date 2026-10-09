// Copyright 2026 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// Package testpostgres provides opt-in isolated databases on an existing test
// PostgreSQL server. It never migrates or clears the database named in the DSN.
package testpostgres

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"os"
	"testing"
	"time"
)

// Open returns nil when no external test server is configured. A supplied but
// unusable DSN is a test failure, never a skip or a fallback to another server.
func Open(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("ATE_TEST_POSTGRES_DSN")
	if dsn == "" {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal("invalid ATE_TEST_POSTGRES_DSN")
	}
	admin, err := pgxpool.NewWithConfig(ctx, cfg.Copy())
	if err != nil {
		t.Fatal("external PostgreSQL connection unavailable")
	}
	var token [12]byte
	if _, err = rand.Read(token[:]); err != nil {
		admin.Close()
		t.Fatal(err)
	}
	name := "ate_test_" + hex.EncodeToString(token[:])
	if _, err = admin.Exec(ctx, "CREATE DATABASE "+pgx.Identifier{name}.Sanitize()); err != nil {
		admin.Close()
		t.Fatalf("create isolated external test database: %v", err)
	}
	cfg.ConnConfig.Database = name
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	t.Cleanup(func() {
		if pool != nil {
			pool.Close()
		}
		cleanup, done := context.WithTimeout(context.Background(), 30*time.Second)
		defer done()
		defer admin.Close()
		if _, err := admin.Exec(cleanup, "DROP DATABASE "+pgx.Identifier{name}.Sanitize()); err != nil {
			t.Errorf("drop isolated external test database: %v", err)
		}
	})
	if err != nil {
		t.Fatal("connect isolated external test database failed")
	}
	if err = pool.Ping(ctx); err != nil {
		t.Fatal("ping isolated external test database failed")
	}
	return pool
}

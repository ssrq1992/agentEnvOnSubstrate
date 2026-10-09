// Package templateregistry persists compatibility identities for committed native templates.
package templateregistry

import (
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"agentenv/services/aenv-api-bridge/internal/creation"
	"agentenv/services/aenv-api-bridge/internal/metadata"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed schema.sql
var schema string

type Store struct{ Pool *pgxpool.Pool }
type Record struct {
	Profile              creation.Template
	CreatedAt, UpdatedAt time.Time
}
type Cursor struct {
	Tenant, ID string
	Time       time.Time
}

func (s *Store) Migrate(ctx context.Context) error {
	if err := s.migrateRegistry(ctx); err != nil {
		return err
	}
	return s.migrateBuilds(ctx)
}

func (s *Store) migrateRegistry(ctx context.Context) error {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(718304249)`); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `CREATE SCHEMA IF NOT EXISTS aenv_bridge; CREATE TABLE IF NOT EXISTS aenv_bridge.template_registry_schema(version integer PRIMARY KEY,digest text NOT NULL)`); err != nil {
		return err
	}
	sum := sha256.Sum256([]byte(schema))
	expected := hex.EncodeToString(sum[:])
	var version int
	var stored string
	err = tx.QueryRow(ctx, `SELECT version,digest FROM aenv_bridge.template_registry_schema ORDER BY version DESC LIMIT 1`).Scan(&version, &stored)
	if err == nil {
		if version != 1 || stored != expected {
			return fmt.Errorf("unsupported or modified template registry migration")
		}
		return tx.Commit(ctx)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	if _, err = tx.Exec(ctx, schema); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO aenv_bridge.template_registry_schema VALUES(1,$1)`, expected); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
func component(value string) bool {
	return value != "" && len(value) <= 256 && !strings.ContainsAny(value, "\x00\r\n")
}

// Publish is for verified, committed native artifacts, never a pending build.
// IDs and specifications are immutable. Existing references cannot be stolen.
func (s *Store) Publish(ctx context.Context, p creation.Template, created, updated time.Time) error {
	return s.publish(ctx, p, created, updated, "")
}

// PublishReplacingAlias commits a verified replacement and moves its alias in
// the same transaction. expectedID is the previously observed target; a late
// build cannot overwrite a newer publication. Template IDs remain immutable.
func (s *Store) PublishReplacingAlias(ctx context.Context, p creation.Template, created, updated time.Time, expectedID string) error {
	if !component(expectedID) || !component(p.Alias) || p.Alias == p.ID {
		return fmt.Errorf("replacement requires an alias and its previous template ID")
	}
	return s.publish(ctx, p, created, updated, expectedID)
}

func (s *Store) publish(ctx context.Context, p creation.Template, created, updated time.Time, expectedID string) error {
	if !component(p.Tenant) || !component(p.ID) || !component(p.Name) || !component(p.NativeUID) || len(p.NativeDigest) != 64 || p.CPUCount < 1 || p.MemoryMB < 1 || p.DiskSizeMB < 1 || p.EnvdVersion == "" || p.EnvdPort < 1 || p.EnvdPort > 65535 || (!component(p.Alias) && p.Alias != "") || created.IsZero() || updated.Before(created) {
		return fmt.Errorf("complete immutable template identity required")
	}
	if _, err := hex.DecodeString(p.NativeDigest); err != nil {
		return fmt.Errorf("invalid native template digest")
	}
	wire, err := json.Marshal(p)
	if err != nil {
		return err
	}
	digest := sha256.Sum256(wire)
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, "aenv-template-registry:"+p.Tenant); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO aenv_bridge.template_registry(tenant,template_id,profile,profile_digest,created_at,updated_at) VALUES($1,$2,$3,$4,$5,$6) ON CONFLICT DO NOTHING`, p.Tenant, p.ID, wire, hex.EncodeToString(digest[:]), created, updated); err != nil {
		return err
	}
	var actual string
	if err = tx.QueryRow(ctx, `SELECT profile_digest FROM aenv_bridge.template_registry WHERE tenant=$1 AND template_id=$2`, p.Tenant, p.ID).Scan(&actual); err != nil {
		return err
	}
	if actual != hex.EncodeToString(digest[:]) {
		return metadata.ErrConflict
	}
	if expectedID != "" {
		var current string
		if err = tx.QueryRow(ctx, `SELECT template_id FROM aenv_bridge.template_references WHERE tenant=$1 AND reference=$2`, p.Tenant, p.Alias).Scan(&current); errors.Is(err, pgx.ErrNoRows) {
			return metadata.ErrConflict
		} else if err != nil {
			return err
		}
		if current != expectedID && current != p.ID {
			return metadata.ErrConflict
		}
	}
	for _, ref := range []string{p.ID, p.Alias} {
		if ref == "" {
			continue
		}
		if _, err = tx.Exec(ctx, `INSERT INTO aenv_bridge.template_references VALUES($1,$2,$3) ON CONFLICT DO NOTHING`, p.Tenant, ref, p.ID); err != nil {
			return err
		}
		var id string
		if err = tx.QueryRow(ctx, `SELECT template_id FROM aenv_bridge.template_references WHERE tenant=$1 AND reference=$2`, p.Tenant, ref).Scan(&id); err != nil {
			return err
		}
		if id != p.ID {
			if expectedID == "" || ref != p.Alias || id != expectedID || ref == expectedID {
				return metadata.ErrConflict
			}
			// IDs and aliases share a namespace: an ID reference must never move.
			var isID bool
			if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM aenv_bridge.template_registry WHERE tenant=$1 AND template_id=$2)`, p.Tenant, ref).Scan(&isID); err != nil {
				return err
			}
			if isID {
				return metadata.ErrConflict
			}
			if _, err = tx.Exec(ctx, `UPDATE aenv_bridge.template_references SET template_id=$3 WHERE tenant=$1 AND reference=$2 AND template_id=$4`, p.Tenant, ref, p.ID, expectedID); err != nil {
				return err
			}
		}
	}
	return tx.Commit(ctx)
}
func scan(row pgx.Row, tenant string) (Record, error) {
	var r Record
	var wire []byte
	var id, storedDigest string
	err := row.Scan(&wire, &storedDigest, &id, &r.CreatedAt, &r.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return r, metadata.ErrNotFound
	}
	if err != nil {
		return r, err
	}
	if err = json.Unmarshal(wire, &r.Profile); err != nil {
		return Record{}, err
	}
	// PostgreSQL jsonb changes whitespace and key ordering. Hash the same typed
	// representation used by Publish, rather than its database encoding.
	canonical, err := json.Marshal(r.Profile)
	if err != nil {
		return Record{}, err
	}
	digest := sha256.Sum256(canonical)
	if storedDigest != hex.EncodeToString(digest[:]) || r.Profile.Tenant != tenant || r.Profile.ID != id {
		return Record{}, fmt.Errorf("template registry integrity failure")
	}
	return r, nil
}
func (s *Store) Get(ctx context.Context, tenant, reference string) (Record, error) {
	if !component(tenant) || !component(reference) {
		return Record{}, metadata.ErrNotFound
	}
	return scan(s.Pool.QueryRow(ctx, `SELECT t.profile,t.profile_digest,t.template_id,t.created_at,t.updated_at FROM aenv_bridge.template_registry t JOIN aenv_bridge.template_references r USING(tenant,template_id) WHERE r.tenant=$1 AND r.reference=$2`, tenant, reference), tenant)
}
func (s *Store) Lookup(ctx context.Context, tenant, reference string) (creation.Template, error) {
	r, err := s.Get(ctx, tenant, reference)
	return r.Profile, err
}
func (s *Store) Page(ctx context.Context, tenant string, cursor *Cursor, limit int) ([]Record, error) {
	if !component(tenant) || limit < 1 || limit > 1000 {
		return nil, fmt.Errorf("invalid template page")
	}
	var cut any
	var id any
	if cursor != nil {
		if cursor.Tenant != tenant || cursor.Time.IsZero() || !component(cursor.ID) {
			return nil, fmt.Errorf("invalid template cursor")
		}
		cut = cursor.Time
		id = cursor.ID
	}
	rows, err := s.Pool.Query(ctx, `SELECT profile,profile_digest,template_id,created_at,updated_at FROM aenv_bridge.template_registry WHERE tenant=$1 AND ($2::timestamptz IS NULL OR (created_at,template_id)<($2::timestamptz,$3::text)) ORDER BY created_at DESC,template_id DESC LIMIT $4`, tenant, cut, id, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Record{}
	for rows.Next() {
		r, err := scan(rows, tenant)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

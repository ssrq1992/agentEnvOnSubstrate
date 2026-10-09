// Package layers orders object publication around durable catalog references.
package layers

import (
	"context"
	"fmt"
	"path/filepath"

	"agentenv/services/aenv-api-bridge/internal/catalog"
)

type References interface {
	Reserve(context.Context, string, string, []catalog.Layer) error
	ConfirmUploaded(context.Context, string, string, catalog.Layer) error
	CommitOwner(context.Context, string, catalog.Owner) error
}
type Objects interface {
	Upload(context.Context, catalog.Layer, string) error
}
type File struct {
	Layer catalog.Layer
	Path  string
}
type Publisher struct {
	Catalog References
	Objects Objects
}

// Stage retains every dependency before the first upload. Any error retains
// the operation references: a timeout cannot prove the object wasn't written,
// and a later control-plane reconciliation may still commit the snapshot.
func (p *Publisher) Stage(ctx context.Context, tenant, operation string, files []File) error {
	if p.Catalog == nil || p.Objects == nil || len(files) == 0 {
		return fmt.Errorf("complete layer publisher and files required")
	}
	layers := make([]catalog.Layer, 0, len(files))
	seen := map[string]bool{}
	for _, file := range files {
		if !filepath.IsAbs(file.Path) || seen[file.Layer.Digest] || file.Layer.Size < 0 {
			return fmt.Errorf("invalid or duplicate layer file")
		}
		if _, err := catalog.ObjectKey(file.Layer.Digest); err != nil {
			return err
		}
		seen[file.Layer.Digest] = true
		layers = append(layers, file.Layer)
	}
	if err := p.Catalog.Reserve(ctx, tenant, operation, layers); err != nil {
		return err
	}
	for _, file := range files {
		if err := p.Objects.Upload(ctx, file.Layer, file.Path); err != nil {
			return fmt.Errorf("upload retained layer: %w", err)
		}
		if err := p.Catalog.ConfirmUploaded(ctx, tenant, operation, file.Layer); err != nil {
			return fmt.Errorf("confirm retained layer: %w", err)
		}
	}
	return nil
}

// CommitConfirmed transfers staging references to a durable owner after the
// caller has confirmed the Substrate snapshot/Actor transaction committed.
// It is intentionally separate from Stage: a successful upload is not a
// successful control-plane commit.
func (p *Publisher) CommitConfirmed(ctx context.Context, operation string, owner catalog.Owner) error {
	if p.Catalog == nil {
		return fmt.Errorf("catalog required")
	}
	return p.Catalog.CommitOwner(ctx, operation, owner)
}

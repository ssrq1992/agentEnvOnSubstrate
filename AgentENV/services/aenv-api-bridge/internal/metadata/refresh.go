package metadata

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// RefreshMinimum never shortens a timer. Row locks order keep-alive against
// expiry and persistent pause/resume intent, including no-op refreshes.
func (s *Store) RefreshMinimum(ctx context.Context, b Sandbox, seconds int) error {
	if err := identity(b.Tenant, b.ExternalID); err != nil {
		return err
	}
	if b.ActorUID == "" || b.Revision <= 0 || seconds < 0 || uint64(seconds) > 4294967295 {
		return fmt.Errorf("invalid refresh precondition")
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var revision int64
	var uid string
	err = tx.QueryRow(ctx, `SELECT revision,actor_uid FROM aenv_bridge.sandboxes WHERE tenant=$1 AND external_id=$2 AND NOT deleted FOR UPDATE`, b.Tenant, b.ExternalID).Scan(&revision, &uid)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if revision != b.Revision || uid != b.ActorUID {
		return ErrConflict
	}
	var held bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM aenv_bridge.expiry_claims WHERE tenant=$1 AND external_id=$2) OR EXISTS(SELECT 1 FROM aenv_bridge.suspend_intents WHERE tenant=$1 AND external_id=$2) OR EXISTS(SELECT 1 FROM aenv_bridge.resume_intents WHERE tenant=$1 AND external_id=$2)`, b.Tenant, b.ExternalID).Scan(&held); err != nil {
		return err
	}
	if held {
		return ErrConflict
	}
	_, err = tx.Exec(ctx, `UPDATE aenv_bridge.sandboxes SET timeout_seconds=$3,expires_at=clock_timestamp()+make_interval(secs=>$3::bigint::double precision),revision=revision+1 WHERE tenant=$1 AND external_id=$2 AND expires_at<clock_timestamp()+make_interval(secs=>$3::bigint::double precision)`, b.Tenant, b.ExternalID, seconds)
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}

package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"crmai/internal/modelgw/whispercpp"
)

// ErrAudioNotFound means no recording with that ref exists for the tenant.
var ErrAudioNotFound = errors.New("postgres: audio not found")

// PutAudio stores or replaces a recording. Returns its sha256 and whether the
// bytes changed.
func (s *Store) PutAudio(ctx context.Context, tenantID, ref, contentType, createdBy string, data []byte) (string, bool, error) {
	sum := sha256.Sum256(data)
	hash := hex.EncodeToString(sum[:])
	changed := true
	err := s.WithTenant(ctx, tenantID, func(tx pgx.Tx) error {
		var old string
		err := tx.QueryRow(ctx, `SELECT sha256 FROM ai_audio WHERE tenant_id = $1 AND ref = $2`, tenantID, ref).Scan(&old)
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			_, err = tx.Exec(ctx, `INSERT INTO ai_audio (tenant_id, ref, content_type, bytes, sha256, created_by)
				VALUES ($1, $2, $3, $4, $5, $6)`, tenantID, ref, contentType, data, hash, createdBy)
			return err
		case err != nil:
			return err
		case old == hash:
			changed = false
			return nil
		default:
			// A re-upload restarts the retention clock.
			_, err = tx.Exec(ctx, `UPDATE ai_audio SET content_type = $3, bytes = $4, sha256 = $5, created_by = $6,
				created_at = now() WHERE tenant_id = $1 AND ref = $2`, tenantID, ref, contentType, data, hash, createdBy)
			return err
		}
	})
	if err != nil {
		return "", false, fmt.Errorf("postgres: put audio: %w", err)
	}
	return hash, changed, nil
}

// GetAudio implements whispercpp.AudioSource.
func (s *Store) GetAudio(ctx context.Context, tenantID, ref string) (whispercpp.Audio, bool, error) {
	var a whispercpp.Audio
	err := s.WithTenant(ctx, tenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT bytes, content_type FROM ai_audio WHERE tenant_id = $1 AND ref = $2`,
			tenantID, ref).Scan(&a.Bytes, &a.ContentType)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return whispercpp.Audio{}, false, nil
	}
	if err != nil {
		return whispercpp.Audio{}, false, fmt.Errorf("postgres: get audio: %w", err)
	}
	return a, true, nil
}

// DeleteAudio removes a recording; ErrAudioNotFound if there was none.
func (s *Store) DeleteAudio(ctx context.Context, tenantID, ref string) error {
	return s.WithTenant(ctx, tenantID, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `DELETE FROM ai_audio WHERE tenant_id = $1 AND ref = $2`, tenantID, ref)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return ErrAudioNotFound
		}
		return nil
	})
}

// PurgeAudio deletes up to limit recordings older than hours, over all tenants,
// through ai_purge_audio(). Returns the number deleted.
func (s *Store) PurgeAudio(ctx context.Context, hours, limit int) (int, error) {
	var n int
	if err := s.pool.QueryRow(ctx, `SELECT ai_purge_audio($1, $2)`, hours, limit).Scan(&n); err != nil {
		return 0, fmt.Errorf("postgres: purge audio: %w", err)
	}
	return n, nil
}

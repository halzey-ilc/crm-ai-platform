package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"crmai/internal/knowledge"
)

// ErrDocumentNotFound means no document with that ref exists for the tenant.
var ErrDocumentNotFound = errors.New("postgres: document not found")

// Document is what the CRM sends for one document.
type Document struct {
	Ref       string
	Title     string
	Language  string
	Tags      []string
	Text      string
	CreatedBy string
}

// PutDocument creates or replaces a document and its passages. Returns the
// number of passages and whether the content changed.
func (s *Store) PutDocument(ctx context.Context, tenantID string, d Document) (int, bool, error) {
	sum := sha256.Sum256([]byte(d.Title + "\x00" + d.Text))
	hash := hex.EncodeToString(sum[:])
	chunks := knowledge.Chunk(d.Text)
	changed := true
	err := s.WithTenant(ctx, tenantID, func(tx pgx.Tx) error {
		var id, oldHash string
		err := tx.QueryRow(ctx, `SELECT id::text, sha256 FROM kb_document WHERE tenant_id = $1 AND ref = $2`,
			tenantID, d.Ref).Scan(&id, &oldHash)
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			err = tx.QueryRow(ctx, `INSERT INTO kb_document (tenant_id, ref, title, language, access_tags, sha256, created_by)
				VALUES ($1, $2, $3, $4, $5, $6, $7) RETURNING id::text`,
				tenantID, d.Ref, d.Title, d.Language, d.Tags, hash, d.CreatedBy).Scan(&id)
			if err != nil {
				return err
			}
		case err != nil:
			return err
		default:
			// Access tags may change without the content changing.
			if _, err := tx.Exec(ctx, `UPDATE kb_document SET title = $2, language = $3, access_tags = $4,
				sha256 = $5, updated_at = now() WHERE id = $1`, id, d.Title, d.Language, d.Tags, hash); err != nil {
				return err
			}
			if oldHash == hash {
				changed = false
				return nil
			}
			if _, err := tx.Exec(ctx, `DELETE FROM kb_passage WHERE document_id = $1`, id); err != nil {
				return err
			}
		}
		for n, body := range chunks {
			if _, err := tx.Exec(ctx, `INSERT INTO kb_passage (document_id, tenant_id, n, body) VALUES ($1, $2, $3, $4)`,
				id, tenantID, n, body); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return 0, false, fmt.Errorf("postgres: put document: %w", err)
	}
	return len(chunks), changed, nil
}

// DeleteDocument removes a document and its passages.
func (s *Store) DeleteDocument(ctx context.Context, tenantID, ref string) error {
	return s.WithTenant(ctx, tenantID, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `DELETE FROM kb_document WHERE tenant_id = $1 AND ref = $2`, tenantID, ref)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return ErrDocumentNotFound
		}
		return nil
	})
}

// Search returns the passages that best match query among the documents the
// asker may see (at least one shared access tag). The tag filter is part of
// the same SQL statement: passages of other documents are never read.
func (s *Store) Search(ctx context.Context, tenantID string, tags []string, query string, limit int) ([]knowledge.Passage, error) {
	if query == "" {
		return nil, nil
	}
	var out []knowledge.Passage
	err := s.WithTenant(ctx, tenantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT d.ref || '#' || p.n, d.title, p.body
			  FROM kb_passage p
			  JOIN kb_document d ON d.id = p.document_id
			 WHERE d.tenant_id = $1
			   AND d.access_tags && $2::text[]
			   AND p.tsv @@ to_tsquery('simple', $3)
			 ORDER BY ts_rank(p.tsv, to_tsquery('simple', $3)) DESC, d.ref, p.n
			 LIMIT $4`, tenantID, tags, query, limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var p knowledge.Passage
			if err := rows.Scan(&p.Ref, &p.Title, &p.Text); err != nil {
				return err
			}
			out = append(out, p)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("postgres: search: %w", err)
	}
	return out, nil
}

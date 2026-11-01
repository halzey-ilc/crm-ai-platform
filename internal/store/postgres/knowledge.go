package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"

	"github.com/jackc/pgx/v5"

	"crmai/internal/embedding"
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
	// Embeddings, when set, hold one vector per passage of knowledge.Chunk(Text),
	// in order; a length mismatch stores no vectors.
	Embeddings [][]float32
}

// PutDocument creates or replaces a document and its passages. Returns the
// number of passages and whether the content changed.
func (s *Store) PutDocument(ctx context.Context, tenantID string, d Document) (int, bool, error) {
	sum := sha256.Sum256([]byte(d.Title + "\x00" + d.Text))
	hash := hex.EncodeToString(sum[:])
	chunks := knowledge.Chunk(d.Text)
	vectors := d.Embeddings
	if len(vectors) != len(chunks) {
		vectors = nil
	}
	vec := func(n int) any {
		if vectors == nil {
			return nil
		}
		return vectors[n]
	}
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
				// Same text: only fill in vectors (e.g. an embedding server was added later).
				for n := range chunks {
					if vectors == nil {
						break
					}
					if _, err := tx.Exec(ctx, `UPDATE kb_passage SET embedding = $3 WHERE document_id = $1 AND n = $2`,
						id, n, vectors[n]); err != nil {
						return err
					}
				}
				return nil
			}
			if _, err := tx.Exec(ctx, `DELETE FROM kb_passage WHERE document_id = $1`, id); err != nil {
				return err
			}
		}
		for n, body := range chunks {
			if _, err := tx.Exec(ctx, `INSERT INTO kb_passage (document_id, tenant_id, n, body, embedding) VALUES ($1, $2, $3, $4, $5)`,
				id, tenantID, n, body, vec(n)); err != nil {
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

// maxVectorCandidates bounds how many passages one semantic search ranks.
const maxVectorCandidates = 20000

// SearchVector returns the passages closest to the question vector q among the
// documents the asker may see, ranked in the application (cosine of normalised
// vectors). The access filter is the same as in Search and part of the query.
func (s *Store) SearchVector(ctx context.Context, tenantID string, tags []string, q []float32, limit int) ([]knowledge.Passage, error) {
	type scored struct {
		p     knowledge.Passage
		score float32
	}
	var top []scored
	err := s.WithTenant(ctx, tenantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT d.ref || '#' || p.n, d.title, p.body, p.embedding
			  FROM kb_passage p
			  JOIN kb_document d ON d.id = p.document_id
			 WHERE d.tenant_id = $1
			   AND d.access_tags && $2::text[]
			   AND p.embedding IS NOT NULL
			 LIMIT $3`, tenantID, tags, maxVectorCandidates)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var sc scored
			var v []float32
			if err := rows.Scan(&sc.p.Ref, &sc.p.Title, &sc.p.Text, &v); err != nil {
				return err
			}
			sc.score = embedding.Dot(q, v)
			// Keep the best `limit` in a small sorted slice.
			i := sort.Search(len(top), func(i int) bool { return top[i].score < sc.score })
			if i < limit {
				top = append(top, scored{})
				copy(top[i+1:], top[i:])
				top[i] = sc
				if len(top) > limit {
					top = top[:limit]
				}
			}
		}
		return rows.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("postgres: vector search: %w", err)
	}
	out := make([]knowledge.Passage, len(top))
	for i, sc := range top {
		out[i] = sc.p
	}
	return out, nil
}

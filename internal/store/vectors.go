package store

import (
	"errors"
	"fmt"
	"math"
)

// quantScale maps a unit-norm float32 component onto the int8 range used by the
// on-disk vector format. 127 (not 128) keeps the quantization symmetric around
// zero so −x and +x round to opposite values, avoiding a one-LSB bias.
const quantScale = 127.0

// encodeVector L2-normalizes vec and quantizes each component to a signed byte,
// returning a dim-length BLOB ready for post_vectors.vec. Normalizing here means
// the stored vectors are (approximately) unit length, so a later dot product of
// two decoded vectors is their cosine similarity — no per-query renormalization
// needed. An all-zero vector encodes to all-zero bytes (it will score 0 against
// everything, which is the correct "no signal" behaviour). Returns an error
// only for an empty input.
func encodeVector(vec []float32) ([]byte, error) {
	if len(vec) == 0 {
		return nil, errors.New("encode vector: empty input")
	}
	var sum float64
	for _, x := range vec {
		sum += float64(x) * float64(x)
	}
	out := make([]byte, len(vec))
	norm := math.Sqrt(sum)
	if norm == 0 {
		return out, nil
	}
	scale := quantScale / norm
	for i, x := range vec {
		q := math.Round(float64(x) * scale)
		if q > 127 {
			q = 127
		} else if q < -127 {
			q = -127
		}
		out[i] = byte(int8(q))
	}
	return out, nil
}

// decodeVector reverses encodeVector: each signed byte becomes its float32
// component divided by quantScale, yielding an approximately unit-norm vector.
// The result is suitable for cosine scoring (it's a plain dot product, since
// both operands are ~unit length).
func decodeVector(blob []byte) []float32 {
	out := make([]float32, len(blob))
	for i, b := range blob {
		out[i] = float32(int8(b)) / quantScale
	}
	return out
}

// PendingEmbed is a post awaiting an embedding: just the id and the text to
// embed. Returned by PostsMissingVectors so the backfill pass can call the
// embeddings server without paying to unmarshal each post's full raw_json.
type PendingEmbed struct {
	ID      string
	Message string
}

// UpsertVector stores (or replaces) the embedding for one post under the given
// model. The vector is quantized via encodeVector. A nil/empty vec or empty
// postID is a silent no-op. now is unix-ms (caller-supplied so tests are
// deterministic); pass time.Now().UnixMilli().
func (s *Store) UpsertVector(postID string, vec []float32, model string, now int64) error {
	if s == nil || postID == "" || len(vec) == 0 {
		return nil
	}
	blob, err := encodeVector(vec)
	if err != nil {
		return fmt.Errorf("encode vector for %s: %w", postID, err)
	}
	if _, err := s.db.Exec(upsertVectorSQL, postID, blob, len(blob), model, now); err != nil {
		return fmt.Errorf("upsert vector %s: %w", postID, err)
	}
	return nil
}

const upsertVectorSQL = `
INSERT INTO post_vectors (post_id, vec, dim, model, created_at)
VALUES (?, ?, ?, ?, ?)
ON CONFLICT(post_id) DO UPDATE SET
    vec        = excluded.vec,
    dim        = excluded.dim,
    model      = excluded.model,
    created_at = excluded.created_at
`

// VectorInput pairs a post id with its freshly computed embedding for the batch
// path. Vec is the raw model output (any length / scale); UpsertVectors
// normalizes and quantizes it.
type VectorInput struct {
	PostID string
	Vec    []float32
}

// UpsertVectors writes a batch of embeddings under one model in a single
// transaction (the backfill / incremental-index path). Entries with an empty
// id or vector are skipped. now is unix-ms applied to every row.
func (s *Store) UpsertVectors(items []VectorInput, model string, now int64) error {
	if s == nil || len(items) == 0 {
		return nil
	}
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	stmt, err := tx.Prepare(upsertVectorSQL)
	if err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("prepare: %w", err)
	}
	defer stmt.Close()
	for _, it := range items {
		if it.PostID == "" || len(it.Vec) == 0 {
			continue
		}
		blob, err := encodeVector(it.Vec)
		if err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("encode vector for %s: %w", it.PostID, err)
		}
		if _, err := stmt.Exec(it.PostID, blob, len(blob), model, now); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("upsert vector %s: %w", it.PostID, err)
		}
	}
	return tx.Commit()
}

// VectorsFor returns the decoded embeddings for the given post ids, keyed by id.
// Ids without a stored vector are simply absent from the map (so len(result) may
// be < len(ids)). This is the lookup the hybrid ranker uses to score an FTS
// candidate pool against the query embedding. An empty ids slice returns an
// empty map without touching SQLite.
func (s *Store) VectorsFor(ids []string) (map[string][]float32, error) {
	out := make(map[string][]float32, len(ids))
	if s == nil || len(ids) == 0 {
		return out, nil
	}
	q := "SELECT post_id, vec FROM post_vectors WHERE post_id IN (" + inPlaceholders(len(ids)) + ")"
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, fmt.Errorf("query vectors: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		var blob []byte
		if err := rows.Scan(&id, &blob); err != nil {
			return nil, fmt.Errorf("scan vector: %w", err)
		}
		out[id] = decodeVector(blob)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("rows: %w", err)
	}
	return out, nil
}

// PostsMissingVectors returns up to limit non-deleted posts that have text but
// no embedding under the given model, newest first — so the backfill pass
// embeds the most recent (and most likely to be searched) content before
// grinding back through history. Posts with no embeddable text (attachment-only
// or system posts, and whitespace-only messages — which yield no chunks and so
// can never get a vector) are excluded in SQL, not skipped post-fetch: that
// keeps the returned count equal to the rows the LIMIT fetched, so the caller's
// "len == limit means the batch was saturated" signal stays truthful. Skipping
// post-fetch instead silently shrank a full batch below limit, which made
// Backfill mistake "one whitespace post in this window" for "queue drained" and
// stop after a single batch. trim's character set covers space, tab, newline,
// and CR — the whitespace that actually occurs in messages. Changing the model
// makes every post "missing" again, which is the intended re-embed trigger.
func (s *Store) PostsMissingVectors(model string, limit int) ([]PendingEmbed, error) {
	if s == nil || limit <= 0 {
		return nil, nil
	}
	const q = `
SELECT p.id, p.message
FROM posts p
LEFT JOIN post_vectors v ON v.post_id = p.id AND v.model = ?
WHERE p.delete_at = 0 AND trim(p.message, char(32,9,10,13)) != '' AND v.post_id IS NULL
ORDER BY p.create_at DESC
LIMIT ?`
	rows, err := s.db.Query(q, model, limit)
	if err != nil {
		return nil, fmt.Errorf("query missing vectors: %w", err)
	}
	defer rows.Close()
	var out []PendingEmbed
	for rows.Next() {
		var pe PendingEmbed
		if err := rows.Scan(&pe.ID, &pe.Message); err != nil {
			return nil, fmt.Errorf("scan pending: %w", err)
		}
		out = append(out, pe)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("rows: %w", err)
	}
	return out, nil
}

// PostsMissingVectorsCount reports how many non-deleted, embeddable posts have
// no vector yet under the given model — i.e. how many a full backfill still has
// to embed. The WHERE clause mirrors PostsMissingVectors exactly so the count
// matches what that query would eventually return.
func (s *Store) PostsMissingVectorsCount(model string) (int, error) {
	if s == nil {
		return 0, nil
	}
	const q = `
SELECT count(*)
FROM posts p
LEFT JOIN post_vectors v ON v.post_id = p.id AND v.model = ?
WHERE p.delete_at = 0 AND trim(p.message, char(32,9,10,13)) != '' AND v.post_id IS NULL`
	var n int
	if err := s.db.QueryRow(q, model).Scan(&n); err != nil {
		return 0, fmt.Errorf("count missing vectors: %w", err)
	}
	return n, nil
}

// VectorCount reports how many embeddings are stored for the given model (or,
// when model is "", across all models). Used to show backfill progress and to
// decide whether semantic search has enough coverage to be worth offering.
func (s *Store) VectorCount(model string) (int, error) {
	if s == nil {
		return 0, nil
	}
	var (
		n   int
		err error
	)
	if model == "" {
		err = s.db.QueryRow(`SELECT count(*) FROM post_vectors`).Scan(&n)
	} else {
		err = s.db.QueryRow(`SELECT count(*) FROM post_vectors WHERE model = ?`, model).Scan(&n)
	}
	if err != nil {
		return 0, fmt.Errorf("count vectors: %w", err)
	}
	return n, nil
}

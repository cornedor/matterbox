package store

import (
	"fmt"
	"math"
	"testing"

	"github.com/mattermost/mattermost/server/public/model"
)

// dot is the cosine similarity of two (approximately) unit-norm vectors.
func dot(a, b []float32) float64 {
	var s float64
	for i := range a {
		s += float64(a[i]) * float64(b[i])
	}
	return s
}

func TestEncodeDecodeRoundTrip(t *testing.T) {
	// Arbitrary, non-unit vector: encode must normalize then quantize, and the
	// decoded result must be ~unit length and point the same direction.
	in := []float32{3, 0, 4, 0} // norm 5 → unit (0.6, 0, 0.8, 0)
	blob, err := encodeVector(in)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	if len(blob) != len(in) {
		t.Fatalf("blob len = %d, want %d", len(blob), len(in))
	}
	got := decodeVector(blob)
	want := []float32{0.6, 0, 0.8, 0}
	for i := range want {
		if math.Abs(float64(got[i]-want[i])) > 0.02 { // int8 quantization slack
			t.Errorf("component %d = %v, want ~%v", i, got[i], want[i])
		}
	}
	if n := math.Sqrt(dot(got, got)); math.Abs(n-1) > 0.02 {
		t.Errorf("decoded norm = %v, want ~1", n)
	}
}

func TestEncodeVectorEmpty(t *testing.T) {
	if _, err := encodeVector(nil); err == nil {
		t.Error("encode of empty vector should error")
	}
}

func TestEncodeVectorZero(t *testing.T) {
	// All-zero has no direction: it must encode to zero bytes, not divide by 0.
	blob, err := encodeVector([]float32{0, 0, 0})
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	for i, b := range blob {
		if b != 0 {
			t.Errorf("byte %d = %d, want 0", i, b)
		}
	}
}

func TestUpsertAndVectorsFor(t *testing.T) {
	s := tempStore(t)
	// VectorsFor only returns rows that exist; it does not require a posts row,
	// but we add posts so the schema's foreign relationship is realistic.
	s.mustPost(t, "pa", "c1", "alpha", 100)
	s.mustPost(t, "pb", "c1", "beta", 200)

	if err := s.UpsertVector("pa", []float32{1, 0, 0}, "m1", 1000); err != nil {
		t.Fatalf("upsert pa: %v", err)
	}
	if err := s.UpsertVectors([]VectorInput{
		{PostID: "pb", Vec: []float32{0, 1, 0}},
		{PostID: "", Vec: []float32{9, 9}}, // skipped: empty id
		{PostID: "pc", Vec: nil},           // skipped: empty vec
	}, "m1", 1000); err != nil {
		t.Fatalf("upsert batch: %v", err)
	}

	got, err := s.VectorsFor([]string{"pa", "pb", "missing"})
	if err != nil {
		t.Fatalf("vectorsFor: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("want 2 vectors, got %d", len(got))
	}
	if _, ok := got["missing"]; ok {
		t.Error("missing id should be absent from result")
	}
	// pa points at +x; cosine with +x should be ~1, with +y ~0.
	if d := dot(got["pa"], []float32{1, 0, 0}); math.Abs(d-1) > 0.02 {
		t.Errorf("pa·x = %v, want ~1", d)
	}
	if d := dot(got["pa"], got["pb"]); math.Abs(d) > 0.02 {
		t.Errorf("pa·pb = %v, want ~0 (orthogonal)", d)
	}
}

func TestUpsertVectorReplace(t *testing.T) {
	s := tempStore(t)
	s.mustPost(t, "pa", "c1", "alpha", 100)
	if err := s.UpsertVector("pa", []float32{1, 0}, "m1", 1000); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if err := s.UpsertVector("pa", []float32{0, 1}, "m1", 2000); err != nil {
		t.Fatalf("re-upsert: %v", err)
	}
	got, _ := s.VectorsFor([]string{"pa"})
	if d := dot(got["pa"], []float32{0, 1}); math.Abs(d-1) > 0.02 {
		t.Errorf("replaced vector not stored: pa·y = %v, want ~1", d)
	}
	if n, _ := s.VectorCount("m1"); n != 1 {
		t.Errorf("count = %d, want 1 after replace", n)
	}
}

func TestPostsMissingVectors(t *testing.T) {
	s := tempStore(t)
	s.mustPost(t, "pa", "c1", "alpha", 100)
	s.mustPost(t, "pb", "c1", "beta", 200)
	s.mustPost(t, "pc", "c1", "", 300)    // empty message → never pending
	s.mustPost(t, "pd", "c1", "   ", 400) // whitespace-only → skipped

	// Before embedding anything, pa & pb are pending (newest first), not pc/pd.
	pend, err := s.PostsMissingVectors("m1", 10)
	if err != nil {
		t.Fatalf("missing: %v", err)
	}
	if len(pend) != 2 {
		t.Fatalf("want 2 pending, got %d (%v)", len(pend), pend)
	}
	if pend[0].ID != "pb" || pend[1].ID != "pa" {
		t.Errorf("want newest-first [pb pa], got [%s %s]", pend[0].ID, pend[1].ID)
	}

	// Embed pb under m1 → only pa remains pending for m1.
	if err := s.UpsertVector("pb", []float32{1, 0}, "m1", 1000); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	pend, _ = s.PostsMissingVectors("m1", 10)
	if len(pend) != 1 || pend[0].ID != "pa" {
		t.Fatalf("want [pa] pending, got %v", pend)
	}

	// A different model treats everything as un-embedded again.
	pend, _ = s.PostsMissingVectors("m2", 10)
	if len(pend) != 2 {
		t.Errorf("model change should re-pend all: got %d", len(pend))
	}
}

// TestPostsMissingVectorsBatchSaturation guards the regression where a
// whitespace-only post inside the LIMIT window shrank the returned batch below
// limit. The indexer reads "len(pending) == batch" as "there may be more", so a
// short batch made Backfill stop after one round even with thousands of posts
// left. A whitespace post must be excluded by the query, not subtracted from the
// fetched rows — so a full window of embeddable posts always returns exactly
// limit even when whitespace posts are interleaved among the newest.
func TestPostsMissingVectorsBatchSaturation(t *testing.T) {
	s := tempStore(t)
	// 5 real posts and a whitespace post newer than all of them, so a naive
	// post-fetch skip would return 4 for a limit of 5.
	for i := 0; i < 5; i++ {
		s.mustPost(t, fmt.Sprintf("p%d", i), "c1", "real text", int64(100+i))
	}
	s.mustPost(t, "ws", "c1", " \t\n", 999) // newest, whitespace-only

	pend, err := s.PostsMissingVectors("m1", 5)
	if err != nil {
		t.Fatalf("missing: %v", err)
	}
	if len(pend) != 5 {
		t.Fatalf("want a saturated batch of 5 (whitespace excluded by query, not subtracted), got %d", len(pend))
	}
	for _, pe := range pend {
		if pe.ID == "ws" {
			t.Errorf("whitespace post should never be pending, got %q", pe.ID)
		}
	}
}

func TestVectorDeletedWithPost(t *testing.T) {
	s := tempStore(t)
	s.mustPost(t, "pa", "c1", "alpha", 100)
	if err := s.UpsertVector("pa", []float32{1, 0}, "m1", 1000); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if err := s.Delete("pa"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if n, _ := s.VectorCount(""); n != 0 {
		t.Errorf("vector should be removed with its post, count = %d", n)
	}
}

// mustPost upserts a post and fails the test on error.
func (s *Store) mustPost(t *testing.T, id, ch, msg string, createAt int64) {
	t.Helper()
	p := &model.Post{Id: id, ChannelId: ch, UserId: "u1", Message: msg, CreateAt: createAt, UpdateAt: createAt}
	if err := s.Upsert(p); err != nil {
		t.Fatalf("upsert post %s: %v", id, err)
	}
}

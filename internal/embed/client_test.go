package embed

import (
	"context"
	"encoding/json"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestEmbeddingsURL(t *testing.T) {
	cases := map[string]string{
		"http://h:8322":     "http://h:8322/v1/embeddings",
		"http://h:8322/":    "http://h:8322/v1/embeddings",
		"http://h:8322/v1":  "http://h:8322/v1/embeddings",
		"http://h:8322/v1/": "http://h:8322/v1/embeddings",
		"":                  defaultEndpoint + "/v1/embeddings",
	}
	for in, want := range cases {
		if got := embeddingsURL(in); got != want {
			t.Errorf("embeddingsURL(%q) = %q, want %q", in, got, want)
		}
	}
}

// fakeServer returns vectors out of order (index 1 before 0) to prove the
// client realigns by reported index, and echoes the request so the test can
// assert model/input were sent.
func fakeServer(t *testing.T, gotModel *string, gotInputs *[]string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/embeddings" {
			http.Error(w, "wrong path", http.StatusNotFound)
			return
		}
		body, _ := io.ReadAll(r.Body)
		var req embeddingRequest
		if err := json.Unmarshal(body, &req); err != nil {
			http.Error(w, "bad json", http.StatusBadRequest)
			return
		}
		*gotModel = req.Model
		*gotInputs = req.Input
		// One distinct unit vector per input, returned in reverse order.
		resp := embeddingResponse{}
		for i := len(req.Input) - 1; i >= 0; i-- {
			vec := make([]float32, 4)
			vec[i%4] = float32(i + 1) // non-unit, distinct; client renormalizes
			resp.Data = append(resp.Data, struct {
				Index     int       `json:"index"`
				Embedding []float32 `json:"embedding"`
			}{Index: i, Embedding: vec})
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
}

func TestEmbedOrderAndNormalize(t *testing.T) {
	var gotModel string
	var gotInputs []string
	srv := fakeServer(t, &gotModel, &gotInputs)
	defer srv.Close()

	c := New(srv.URL, "", "embeddinggemma", 0)
	vecs, err := c.Embed(context.Background(), []string{"a", "b", "c"})
	if err != nil {
		t.Fatalf("embed: %v", err)
	}
	if gotModel != "embeddinggemma" {
		t.Errorf("model sent = %q", gotModel)
	}
	if len(vecs) != 3 {
		t.Fatalf("want 3 vectors, got %d", len(vecs))
	}
	// Despite the server reversing order, vecs[i] must correspond to input i:
	// input 0 → component 0 set, input 1 → component 1, input 2 → component 2.
	for i := range vecs {
		if vecs[i][i] <= 0 {
			t.Errorf("vec %d not aligned to its input: %v", i, vecs[i])
		}
		if n := math.Sqrt(float64(dot(vecs[i], vecs[i]))); math.Abs(n-1) > 1e-5 {
			t.Errorf("vec %d not unit-normalized: norm %v", i, n)
		}
	}
}

func TestEmbedDimTruncation(t *testing.T) {
	var m string
	var in []string
	srv := fakeServer(t, &m, &in)
	defer srv.Close()

	c := New(srv.URL, "", "m", 2) // truncate 4 → 2
	vecs, err := c.Embed(context.Background(), []string{"x"})
	if err != nil {
		t.Fatalf("embed: %v", err)
	}
	if len(vecs[0]) != 2 {
		t.Fatalf("want dim 2 after truncation, got %d", len(vecs[0]))
	}
}

func TestEmbedEmptyInputs(t *testing.T) {
	c := New("http://unused", "", "m", 0)
	vecs, err := c.Embed(context.Background(), nil)
	if err != nil || vecs != nil {
		t.Errorf("empty inputs: got (%v, %v), want (nil, nil)", vecs, err)
	}
}

func TestEmbedServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "model not loaded", http.StatusServiceUnavailable)
	}))
	defer srv.Close()
	c := New(srv.URL, "", "m", 0)
	if _, err := c.Embed(context.Background(), []string{"x"}); err == nil {
		t.Error("expected error on 503")
	}
}

func TestEmbedCountMismatch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Two inputs requested, one vector returned.
		resp := embeddingResponse{}
		resp.Data = append(resp.Data, struct {
			Index     int       `json:"index"`
			Embedding []float32 `json:"embedding"`
		}{Index: 0, Embedding: []float32{1, 0}})
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()
	c := New(srv.URL, "", "m", 0)
	if _, err := c.Embed(context.Background(), []string{"a", "b"}); err == nil {
		t.Error("expected error on vector/input count mismatch")
	}
}

func dot(a, b []float32) float32 {
	var s float32
	for i := range a {
		s += a[i] * b[i]
	}
	return s
}

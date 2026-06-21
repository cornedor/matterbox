package store

import (
	"sync"
	"testing"
)

func TestStateRoundTrip(t *testing.T) {
	st := openTemp(t)

	if _, ok, err := st.GetState("k"); ok || err != nil {
		t.Fatalf("absent key: ok=%v err=%v", ok, err)
	}
	if err := st.SetState("k", "v1"); err != nil {
		t.Fatalf("set: %v", err)
	}
	if v, ok, err := st.GetState("k"); err != nil || !ok || v != "v1" {
		t.Fatalf("get = %q ok=%v err=%v", v, ok, err)
	}
	if err := st.SetState("k", "v2"); err != nil { // upsert
		t.Fatalf("upsert: %v", err)
	}
	if v, _, _ := st.GetState("k"); v != "v2" {
		t.Fatalf("upsert value = %q want v2", v)
	}
	if err := st.DeleteState("k"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, ok, _ := st.GetState("k"); ok {
		t.Fatal("key should be gone after delete")
	}
	if err := st.DeleteState("k"); err != nil { // deleting absent is fine
		t.Fatalf("delete absent: %v", err)
	}
}

func TestIncrState(t *testing.T) {
	st := openTemp(t)

	// First increment creates the key from 0.
	if n, err := st.IncrState("c", 1); err != nil || n != 1 {
		t.Fatalf("incr from absent = %d err=%v, want 1", n, err)
	}
	if n, _ := st.IncrState("c", 2); n != 3 {
		t.Fatalf("incr = %d, want 3", n)
	}
	if n, _ := st.IncrState("c", -1); n != 2 { // decrement
		t.Fatalf("decrement = %d, want 2", n)
	}
	// A non-numeric value CASTs to 0, so incrementing it starts counting fresh.
	if err := st.SetState("c", "not a number"); err != nil {
		t.Fatalf("set: %v", err)
	}
	if n, _ := st.IncrState("c", 5); n != 5 {
		t.Fatalf("incr of non-numeric = %d, want 5", n)
	}
}

// TestIncrStateConcurrent checks that concurrent increments to the same key
// don't lose a write — the count must equal the number of increments.
func TestIncrStateConcurrent(t *testing.T) {
	st := openTemp(t)
	const n = 50
	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			if _, err := st.IncrState("hits", 1); err != nil {
				t.Errorf("incr: %v", err)
			}
		}()
	}
	wg.Wait()
	if v, _, _ := st.GetState("hits"); v != "50" {
		t.Fatalf("final count = %q, want 50", v)
	}
}

func TestAllState(t *testing.T) {
	st := openTemp(t)
	if m, err := st.AllState(); err != nil || len(m) != 0 {
		t.Fatalf("empty AllState = %v err=%v", m, err)
	}
	_ = st.SetState("a", "1")
	_ = st.SetState("b", "two")
	m, err := st.AllState()
	if err != nil {
		t.Fatalf("AllState: %v", err)
	}
	if m["a"] != "1" || m["b"] != "two" || len(m) != 2 {
		t.Fatalf("AllState = %v", m)
	}
}

// A nil *Store is a no-op everywhere, matching the rest of the listen store API
// (the daemon may run without a cache).
func TestStateNilStore(t *testing.T) {
	var s *Store
	if _, ok, err := s.GetState("k"); ok || err != nil {
		t.Fatalf("nil GetState: ok=%v err=%v", ok, err)
	}
	if err := s.SetState("k", "v"); err != nil {
		t.Fatalf("nil SetState: %v", err)
	}
	if n, err := s.IncrState("k", 1); n != 0 || err != nil {
		t.Fatalf("nil IncrState: n=%d err=%v", n, err)
	}
	if err := s.DeleteState("k"); err != nil {
		t.Fatalf("nil DeleteState: %v", err)
	}
	if m, err := s.AllState(); m != nil || err != nil {
		t.Fatalf("nil AllState: m=%v err=%v", m, err)
	}
}

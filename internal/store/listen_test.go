package store

import (
	"path/filepath"
	"testing"
)

func openTemp(t *testing.T) *Store {
	t.Helper()
	st, err := Open(filepath.Join(t.TempDir(), "listen.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func TestMetaRoundTrip(t *testing.T) {
	st := openTemp(t)
	if _, ok, err := st.GetMeta("k"); ok || err != nil {
		t.Fatalf("absent key: ok=%v err=%v", ok, err)
	}
	if err := st.SetMeta("k", "v1"); err != nil {
		t.Fatalf("set: %v", err)
	}
	if v, ok, err := st.GetMeta("k"); err != nil || !ok || v != "v1" {
		t.Fatalf("get = %q ok=%v err=%v", v, ok, err)
	}
	if err := st.SetMeta("k", "v2"); err != nil { // upsert
		t.Fatalf("upsert: %v", err)
	}
	if v, _, _ := st.GetMeta("k"); v != "v2" {
		t.Fatalf("upsert value = %q want v2", v)
	}
}

func TestNotifTargetRoundTrip(t *testing.T) {
	st := openTemp(t)
	if _, _, _, ok, err := st.GetNotifTarget(1); ok || err != nil {
		t.Fatalf("absent: ok=%v err=%v", ok, err)
	}
	if err := st.PutNotifTarget(42, "chan", "root", "post"); err != nil {
		t.Fatalf("put: %v", err)
	}
	ch, root, post, ok, err := st.GetNotifTarget(42)
	if err != nil || !ok || ch != "chan" || root != "root" || post != "post" {
		t.Fatalf("get = %q/%q/%q ok=%v err=%v", ch, root, post, ok, err)
	}
	// Upsert in place.
	if err := st.PutNotifTarget(42, "chan2", "root2", "post2"); err != nil {
		t.Fatalf("re-put: %v", err)
	}
	if ch, _, _, _, _ := st.GetNotifTarget(42); ch != "chan2" {
		t.Fatalf("upsert channel = %q want chan2", ch)
	}
}

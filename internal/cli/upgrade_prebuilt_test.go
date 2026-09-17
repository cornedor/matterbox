package cli

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// fakeRelease serves a release the way GitHub does: the tarball at one path,
// checksums.txt beside it. The "binary" is a shell script, which is enough for
// the one thing the upgrade asks of it — say what version it is.
type fakeRelease struct {
	version string
	files   map[string][]byte // asset name -> bytes
	sums    map[string]string // asset name -> sha256, as published
}

func newFakeRelease(t *testing.T, version, reports string) *fakeRelease {
	t.Helper()
	tarball := tarGz(t, "./"+binaryName, []byte("#!/bin/sh\necho "+reports+"\n"))
	name := assetName(version)
	sum := sha256.Sum256(tarball)
	return &fakeRelease{
		version: version,
		files:   map[string][]byte{name: tarball},
		sums:    map[string]string{name: hex.EncodeToString(sum[:])},
	}
}

// serve points releaseAssetBase at a local server for the duration of the test.
func (r *fakeRelease) serve(t *testing.T) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		name := filepath.Base(req.URL.Path)
		if name == "checksums.txt" {
			var b strings.Builder
			for asset, sum := range r.sums {
				fmt.Fprintf(&b, "%s  %s\n", sum, asset)
			}
			io.WriteString(w, b.String())
			return
		}
		body, ok := r.files[name]
		if !ok {
			http.NotFound(w, req)
			return
		}
		w.Write(body)
	}))
	t.Cleanup(srv.Close)
	prev := releaseAssetBase
	releaseAssetBase = srv.URL + "/releases/download"
	t.Cleanup(func() { releaseAssetBase = prev })
}

func tarGz(t *testing.T, name string, content []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, f := range []struct {
		name string
		body []byte
	}{
		{"./README.md", []byte("not the binary\n")},
		{name, content},
	} {
		if err := tw.WriteHeader(&tar.Header{
			Name: f.name, Mode: 0o755, Size: int64(len(f.body)), Typeflag: tar.TypeReg,
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write(f.body); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// existingBinary is what the upgrade will replace, in its own directory.
func existingBinary(t *testing.T) string {
	t.Helper()
	dst := filepath.Join(t.TempDir(), binaryName)
	if err := os.WriteFile(dst, []byte("#!/bin/sh\necho old\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return dst
}

func requireUnix(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the upgrade has no path on windows")
	}
}

func TestInstallPrebuiltReplacesTheBinary(t *testing.T) {
	requireUnix(t)
	rel := newFakeRelease(t, "v9.9.9", "v9.9.9")
	rel.serve(t)
	dst := existingBinary(t)

	var out bytes.Buffer
	if err := installPrebuilt(context.Background(), &out, "v9.9.9", dst); err != nil {
		t.Fatalf("installPrebuilt: %v", err)
	}
	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), "echo v9.9.9") {
		t.Fatalf("destination still holds %q", got)
	}
	info, err := os.Stat(dst)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm()&0o111 == 0 {
		t.Errorf("installed binary is not executable: %v", info.Mode())
	}
	leftovers(t, dst)
}

// The check the whole path exists for: a tarball that does not match the
// published checksum must not be installed, and must not be left lying around
// either.
func TestInstallPrebuiltRefusesAChecksumMismatch(t *testing.T) {
	requireUnix(t)
	rel := newFakeRelease(t, "v9.9.9", "v9.9.9")
	// The published checksum stays; the bytes served change underneath it,
	// which is what a tampered or corrupted download looks like from here.
	rel.files[assetName("v9.9.9")] = tarGz(t, "./"+binaryName, []byte("#!/bin/sh\necho pwned\n"))
	rel.serve(t)
	dst := existingBinary(t)

	err := installPrebuilt(context.Background(), io.Discard, "v9.9.9", dst)
	if err == nil {
		t.Fatal("installed a tarball that did not match its checksum")
	}
	if !strings.Contains(err.Error(), "does not match the checksum") {
		t.Fatalf("error does not say why: %v", err)
	}
	assertUntouched(t, dst)
	leftovers(t, dst)
}

// A release that has no tarball for this platform is a clear error, not a
// download of something else.
func TestInstallPrebuiltRefusesAnUnlistedAsset(t *testing.T) {
	requireUnix(t)
	rel := newFakeRelease(t, "v9.9.9", "v9.9.9")
	rel.sums = map[string]string{"matterbox_v9.9.9_plan9_386.tar.gz": strings.Repeat("a", 64)}
	rel.serve(t)
	dst := existingBinary(t)

	err := installPrebuilt(context.Background(), io.Discard, "v9.9.9", dst)
	if err == nil || !strings.Contains(err.Error(), "no matterbox_") {
		t.Fatalf("want a missing-asset error, got %v", err)
	}
	assertUntouched(t, dst)
}

// The last gate before the rename: a binary that matches its checksum but is
// not the version asked for never gets installed.
func TestInstallPrebuiltRefusesAWrongVersion(t *testing.T) {
	requireUnix(t)
	rel := newFakeRelease(t, "v9.9.9", "v0.0.1")
	rel.serve(t)
	dst := existingBinary(t)

	err := installPrebuilt(context.Background(), io.Discard, "v9.9.9", dst)
	if err == nil || !strings.Contains(err.Error(), "reports something other than") {
		t.Fatalf("want a version mismatch, got %v", err)
	}
	assertUntouched(t, dst)
	leftovers(t, dst)
}

// An archive without the binary in it is an error rather than an empty install.
func TestExtractBinaryNeedsTheBinary(t *testing.T) {
	dir := t.TempDir()
	tarball := filepath.Join(dir, "rel.tar.gz")
	if err := os.WriteFile(tarball, tarGz(t, "./something-else", []byte("x")), 0o600); err != nil {
		t.Fatal(err)
	}
	_, cleanup, err := extractBinary(tarball, dir)
	if cleanup != nil {
		cleanup()
	}
	if err == nil || !strings.Contains(err.Error(), "holds no matterbox") {
		t.Fatalf("want a missing-binary error, got %v", err)
	}
}

func assertUntouched(t *testing.T, dst string) {
	t.Helper()
	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), "echo old") {
		t.Fatalf("the binary was replaced anyway: %q", got)
	}
}

// A failed upgrade must not leave a half-downloaded binary in the directory the
// real one lives in.
func leftovers(t *testing.T, dst string) {
	t.Helper()
	entries, err := os.ReadDir(filepath.Dir(dst))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".matterbox-new-") {
			t.Errorf("left %s behind", e.Name())
		}
	}
}

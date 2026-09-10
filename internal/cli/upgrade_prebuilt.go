package cli

import (
	"archive/tar"
	"bufio"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// The prebuilt upgrade does not run anything it downloaded. It takes the
// release tarball and the checksums file GitHub published beside it, checks the
// one against the other, and only then unpacks a binary and moves it into
// place. What arrives over the network is data that gets validated, rather than
// a shell script that gets executed — which is the difference that matters,
// because a script is trusted the moment it is fetched and a tarball is not
// trusted until its hash matches.
//
// What this does not defend against is GitHub itself: the checksums come from
// the same release as the tarball, so a party who can rewrite one can rewrite
// the other. It closes the download — a corrupted transfer, a cache serving
// something stale, a single hijacked connection — and it removes remote code
// execution from the happy path. Making the release itself unforgeable needs a
// signature over the checksums with a key that is not served from the same
// place; it would go beside the checksum comparison in installPrebuilt.
//
// A var only so a test can point it at a local server.
var releaseAssetBase = "https://github.com/cornedor/matterbox/releases/download"

const (
	// assetTimeout bounds a single download. The tarball carries a statically
	// linked FFmpeg, so it is tens of megabytes and a slow line is not an
	// error; this is a ceiling, not a budget.
	assetTimeout = 10 * time.Minute
	// checksumsMaxBytes: one sha256 line per platform, so anything approaching
	// this is not our checksums file.
	checksumsMaxBytes = 64 << 10
	// tarballMaxBytes caps what will be written to disk from the archive. The
	// releases are well under this; the point is that a gzip bomb cannot fill
	// the disk before the hash is even checked.
	tarballMaxBytes = 512 << 20
	// binaryName is what the tarball holds and what gets installed.
	binaryName = "matterbox"
)

// installPrebuilt replaces the binary at dst with the release named by version.
func installPrebuilt(ctx context.Context, out io.Writer, version, dst string) error {
	asset := assetName(version)

	sums, err := fetchChecksums(ctx, version)
	if err != nil {
		return err
	}
	want, ok := sums[asset]
	if !ok {
		return fmt.Errorf("%s has no %s; this platform may not be in that release", version, asset)
	}

	tarball, cleanupTarball, err := downloadAsset(ctx, version, asset)
	if err != nil {
		return err
	}
	defer cleanupTarball()

	got, err := sha256File(tarball)
	if err != nil {
		return err
	}
	if got != want {
		// Deliberately loud and deliberately final: this is the check the whole
		// path exists for, and "try again" is the wrong advice when the answer
		// might be that someone is answering for github.com.
		return fmt.Errorf("%s does not match the checksum published with it\n"+
			"    expected %s\n    received %s\n"+
			"    nothing was installed", asset, want, got)
	}

	// Staged in the destination directory, so the final move is a rename within
	// one filesystem — atomic, and never a half-written binary on PATH.
	staged, cleanupStaged, err := extractBinary(tarball, filepath.Dir(dst))
	if err != nil {
		return err
	}
	defer cleanupStaged()

	if err := checkRuns(ctx, staged, version); err != nil {
		return err
	}
	if err := os.Rename(staged, dst); err != nil {
		return fmt.Errorf("could not install into %s: %w", dst, err)
	}
	fmt.Fprintf(out, "installed %s to %s\n", version, dst)
	return nil
}

// assetName is the tarball the release workflow packs for this platform. Keep
// it in step with the tar step in .github/workflows/release.yml.
func assetName(version string) string {
	return fmt.Sprintf("matterbox_%s_%s_%s.tar.gz", version, runtime.GOOS, runtime.GOARCH)
}

func assetURL(version, name string) string {
	return fmt.Sprintf("%s/%s/%s", strings.TrimSuffix(releaseAssetBase, "/"), version, name)
}

// fetchChecksums reads the release's checksums.txt into filename -> sha256.
// Anything it cannot parse is skipped rather than fatal: an unreadable line is
// only a problem if it was the line we needed, and the caller finds that out.
func fetchChecksums(ctx context.Context, version string) (map[string]string, error) {
	res, err := getAsset(ctx, assetURL(version, "checksums.txt"))
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()

	sums := map[string]string{}
	sc := bufio.NewScanner(io.LimitReader(res.Body, checksumsMaxBytes))
	for sc.Scan() {
		// sha256sum output: "<hex>  <name>", the name possibly "*name" from
		// binary mode.
		f := strings.Fields(sc.Text())
		if len(f) != 2 || len(f[0]) != hex.EncodedLen(sha256.Size) {
			continue
		}
		if _, err := hex.DecodeString(f[0]); err != nil {
			continue
		}
		sums[strings.TrimPrefix(f[1], "*")] = f[0]
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("could not read the checksums for %s: %w", version, err)
	}
	if len(sums) == 0 {
		return nil, fmt.Errorf("the checksums published with %s are empty", version)
	}
	return sums, nil
}

// downloadAsset streams an asset to a temp file. To a file rather than memory
// because the tarball is tens of megabytes and it has to be read twice — once
// to hash, once to unpack — and hashing a stream while also unpacking it would
// mean unpacking before the hash is known.
func downloadAsset(ctx context.Context, version, name string) (tmp string, cleanup func(), err error) {
	res, err := getAsset(ctx, assetURL(version, name))
	if err != nil {
		return "", nil, err
	}
	defer res.Body.Close()

	f, err := os.CreateTemp("", "matterbox-release-*.tar.gz")
	if err != nil {
		return "", nil, err
	}
	remove := func() { os.Remove(f.Name()) }
	n, err := io.Copy(f, io.LimitReader(res.Body, tarballMaxBytes+1))
	if err == nil && n > tarballMaxBytes {
		err = fmt.Errorf("%s is larger than %d bytes", name, int64(tarballMaxBytes))
	}
	if err != nil {
		f.Close()
		remove()
		return "", nil, fmt.Errorf("could not download %s: %w", name, err)
	}
	if err := f.Close(); err != nil {
		remove()
		return "", nil, err
	}
	return f.Name(), remove, nil
}

// getAsset performs the GET. Redirects are expected here — a GitHub release
// download lands on a storage host — so the rule is not the origin pinning the
// installer script gets, but the one thing that must hold across every hop:
// TLS. A redirect to http:// would put the download in the hands of whoever is
// on the wire, checksum or not, since the checksums travel the same way.
func getAsset(ctx context.Context, rawURL string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "matterbox")
	client := &http.Client{
		Timeout: assetTimeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if !secureURL(req.URL) {
				return fmt.Errorf("refusing a redirect to %s", req.URL.Scheme+"://"+req.URL.Host)
			}
			if len(via) >= 10 {
				return fmt.Errorf("%s redirected too many times", rawURL)
			}
			return nil
		},
	}
	res, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("could not download %s: %w", rawURL, err)
	}
	if res.StatusCode != http.StatusOK {
		res.Body.Close()
		if res.StatusCode == http.StatusNotFound {
			return nil, fmt.Errorf("%s: not found — is that a released version?", rawURL)
		}
		return nil, fmt.Errorf("could not download %s: %s", rawURL, res.Status)
	}
	return res, nil
}

// secureURL allows the plain-http exception a test server needs, and only for
// loopback, where there is no wire to be on.
func secureURL(u *url.URL) bool {
	if u.Scheme == "https" {
		return true
	}
	return u.Scheme == "http" && isLoopback(u.Hostname())
}

func isLoopback(host string) bool {
	return host == "localhost" || host == "127.0.0.1" || host == "::1"
}

func sha256File(name string) (string, error) {
	f, err := os.Open(name)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// extractBinary pulls the one file that matters out of the verified tarball and
// writes it, executable, into dir. Everything else in there — the licences, the
// README — is for people reading a tarball, not for an upgrade.
func extractBinary(tarball, dir string) (staged string, cleanup func(), err error) {
	f, err := os.Open(tarball)
	if err != nil {
		return "", nil, err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return "", nil, fmt.Errorf("the release archive is not readable: %w", err)
	}
	defer gz.Close()

	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return "", nil, fmt.Errorf("the release archive holds no %s", binaryName)
		}
		if err != nil {
			return "", nil, fmt.Errorf("the release archive is not readable: %w", err)
		}
		// The archive is packed as "./matterbox"; only the name is trusted, and
		// only after it is reduced to one. Nothing here joins a path from the
		// archive onto dir, so a "../.." entry has nowhere to go.
		if h.Typeflag != tar.TypeReg || path.Base(filepath.ToSlash(h.Name)) != binaryName {
			continue
		}
		out, err := os.CreateTemp(dir, ".matterbox-new-*")
		if err != nil {
			return "", nil, fmt.Errorf("could not write into %s: %w", dir, err)
		}
		remove := func() { os.Remove(out.Name()) }
		n, err := io.Copy(out, io.LimitReader(tr, tarballMaxBytes+1))
		if err == nil && n > tarballMaxBytes {
			err = fmt.Errorf("%s in the archive is larger than %d bytes", binaryName, int64(tarballMaxBytes))
		}
		if err == nil {
			err = out.Chmod(0o755)
		}
		if err != nil {
			out.Close()
			remove()
			return "", nil, err
		}
		if err := out.Close(); err != nil {
			remove()
			return "", nil, err
		}
		return out.Name(), remove, nil
	}
}

// checkRuns is the last thing before the rename: the downloaded binary is asked
// what it is. It catches the failures a checksum cannot — a release built for
// another architecture, a tarball that is ours but not the version asked for —
// while the old binary is still in place and nothing has been replaced.
func checkRuns(ctx context.Context, bin, version string) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, bin, "--version").CombinedOutput()
	if err != nil {
		return fmt.Errorf("the downloaded binary does not run here: %w\n%s", err, strings.TrimSpace(string(out)))
	}
	if !strings.Contains(string(out), version) {
		return fmt.Errorf("the downloaded binary reports something other than %s:\n%s",
			version, strings.TrimSpace(string(out)))
	}
	return nil
}

// installTarget is the path the new binary takes: the one being replaced, or
// the conventional name inside an explicit --dir.
func installTarget(dir string) (string, error) {
	if dir != "" {
		return filepath.Join(dir, binaryName), nil
	}
	exe, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("could not locate the running binary: %w", err)
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	return exe, nil
}

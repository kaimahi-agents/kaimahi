package toolchain

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func digestOf(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func tarGz(t *testing.T, member string, body []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(zw)
	if err := tw.WriteHeader(&tar.Header{Name: member, Mode: 0o755, Size: int64(len(body)), Typeflag: tar.TypeReg}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(body); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// Only the first field of a checksum file is ours to trust: the rest is the
// PUBLISHER's build path. kubectl publishes a bare digest, kind and Helm
// publish `<digest>  <name>`, and both must read the same.
func TestExpectedDigestTakesOnlyTheFirstField(t *testing.T) {
	want := digestOf([]byte("x"))
	for _, file := range []string{want, want + "\n", want + "  kind-linux-amd64\n", want + "  /home/publisher/dist/helm.tar.gz\n"} {
		got, err := ExpectedDigest([]byte(file))
		if err != nil {
			t.Fatalf("%q: %v", file, err)
		}
		if got != want {
			t.Errorf("%q: got %s, want %s", file, got, want)
		}
	}
}

func TestMalformedChecksumFilesAreRefused(t *testing.T) {
	for _, file := range []string{"", "   \n", "not-a-digest  kind", strings.Repeat("z", 64)} {
		if _, err := ExpectedDigest([]byte(file)); err == nil {
			t.Errorf("%q was accepted as a checksum file", file)
		}
	}
}

func TestVerifyFailsClosedOnAMismatch(t *testing.T) {
	if err := Verify("kind", []byte("substituted"), []byte(digestOf([]byte("published")))); err == nil {
		t.Fatal("a mismatched digest was accepted")
	}
}

// The pinned specs must name assets the upstreams actually publish. This is a
// spelling check, not a network call: a typo here surfaces as a 404 half way
// through someone's first run.
func TestPinnedSpecsAreWellFormed(t *testing.T) {
	for _, name := range Fetchable {
		spec, ok := Pinned(name, "linux", "amd64")
		if !ok {
			t.Fatalf("%s is listed as fetchable but has no spec", name)
		}
		if spec.Name != name || spec.Version == "" || spec.Why == "" {
			t.Errorf("%s: incomplete spec %+v", name, spec)
		}
		if !strings.HasPrefix(spec.URL, "https://") || !strings.HasPrefix(spec.ChecksumURL, "https://") {
			t.Errorf("%s: both URLs must be https: %+v", name, spec)
		}
		if !strings.Contains(spec.URL, spec.Version) {
			t.Errorf("%s: the URL does not carry the pinned version: %s", name, spec.URL)
		}
		if !strings.HasPrefix(spec.ChecksumURL, spec.URL) {
			t.Errorf("%s: the checksum must be published beside the asset it covers: %s vs %s", name, spec.ChecksumURL, spec.URL)
		}
	}
	if _, ok := Pinned("docker", "linux", "amd64"); ok {
		t.Error("a container engine must NOT be fetchable — it is a daemon, not a cached binary")
	}
}

// The whole mechanism, on a throwaway release: download, verify, cache,
// re-verify, refuse a substituted cache entry, and leave nothing behind when
// the digest does not match.
func TestEnsureDownloadsVerifiesCachesAndReVerifies(t *testing.T) {
	binary := []byte("#!/bin/sh\necho kind\n")
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, ".sha256sum"):
			fmt.Fprintf(w, "%s  kind-linux-amd64\n", digestOf(binary))
		default:
			hits++
			w.Write(binary)
		}
	}))
	defer srv.Close()

	spec, _ := Pinned("kind", "linux", "amd64")
	opt := Options{CacheDir: t.TempDir(), BaseOverride: srv.URL}
	path, err := Ensure(spec, opt)
	if err != nil {
		t.Fatalf("fetch failed: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&0o111 == 0 {
		t.Errorf("cached binary is not executable: %v", info.Mode())
	}
	// The cache key carries the version: a pin bump cannot be served the
	// previous binary.
	if !strings.Contains(filepath.Base(path), spec.Version) {
		t.Errorf("cache name %q does not carry the version", filepath.Base(path))
	}
	if _, err := Ensure(spec, opt); err != nil {
		t.Fatal(err)
	}
	if hits != 1 {
		t.Errorf("downloaded %d times, want 1 — the second call must hit the cache", hits)
	}

	// A cache hit is re-verified, so a substituted binary is refused and
	// re-fetched rather than executed.
	if err := os.WriteFile(path, []byte("#!/bin/sh\necho pwned\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := Ensure(spec, opt); err != nil {
		t.Fatalf("a tampered cache entry should be replaced, not fatal: %v", err)
	}
	if hits != 2 {
		t.Errorf("a tampered cache entry was NOT re-fetched (downloads: %d)", hits)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(binary) {
		t.Errorf("the cache still holds the substituted binary: %q", got)
	}

	// No recorded digest means it cannot be verified, so it is not run.
	if err := os.Remove(path + ".sha256"); err != nil {
		t.Fatal(err)
	}
	if _, err := Ensure(spec, opt); err != nil {
		t.Fatal(err)
	}
	if hits != 3 {
		t.Errorf("an unverifiable cache entry was NOT re-fetched (downloads: %d)", hits)
	}
}

// A rejected download must not be sitting in the cache when the next run
// looks — that is the difference between a failed fetch and a silent
// downgrade to unverified bytes.
func TestEnsureWritesNothingWhenTheChecksumFails(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, ".sha256sum") {
			fmt.Fprintf(w, "%s  kind-linux-amd64\n", digestOf([]byte("what was published")))
			return
		}
		w.Write([]byte("what was served"))
	}))
	defer srv.Close()

	spec, _ := Pinned("kind", "linux", "amd64")
	cache := t.TempDir()
	if _, err := Ensure(spec, Options{CacheDir: cache, BaseOverride: srv.URL}); err == nil {
		t.Fatal("a mismatched download was accepted")
	}
	entries, err := os.ReadDir(cache)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("the cache is not empty after a rejected download: %v", entries)
	}
}

// Helm ships in a tarball, and the published digest covers the TARBALL. The
// archive is verified before anything is extracted from it, and the extracted
// member is what gets installed.
func TestAnArchivedToolIsVerifiedBeforeItIsExtracted(t *testing.T) {
	binary := []byte("#!/bin/sh\necho helm\n")
	spec, _ := Pinned("helm", "linux", "amd64")
	archive := tarGz(t, spec.ArchiveMember, binary)
	tampered := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, ".sha256sum") {
			fmt.Fprintf(w, "%s  %s\n", digestOf(archive), filepath.Base(spec.URL))
			return
		}
		if tampered {
			w.Write(tarGz(t, spec.ArchiveMember, []byte("#!/bin/sh\necho pwned\n")))
			return
		}
		w.Write(archive)
	}))
	defer srv.Close()

	opt := Options{CacheDir: t.TempDir(), BaseOverride: srv.URL}
	path, err := Ensure(spec, opt)
	if err != nil {
		t.Fatalf("fetch failed: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(binary) {
		t.Errorf("installed %q, want the archive member %q", got, binary)
	}

	// A re-packed archive no longer matches the published digest, and is
	// refused before its contents are ever read.
	tampered = true
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if _, err := Ensure(spec, opt); err == nil {
		t.Fatal("a re-packed archive was accepted")
	}
}

func TestPrependPathIsIdempotent(t *testing.T) {
	sep := string(os.PathListSeparator)
	got := PrependPath("/usr/bin"+sep+"/bin", "/cache")
	want := "/cache" + sep + "/usr/bin" + sep + "/bin"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
	if again := PrependPath(got, "/cache"); again != got {
		t.Errorf("running twice grew PATH: %q", again)
	}
}

// What the operator already has wins, and only the rest is fetched.
func TestProvisionPrefersWhatIsAlreadyOnPath(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "kubectl"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	tools, err := Provision([]string{"kubectl"}, t.TempDir(), Options{CacheDir: t.TempDir(), BaseOverride: "http://127.0.0.1:1"})
	if err != nil {
		t.Fatalf("provision failed: %v", err)
	}
	if len(tools) != 1 || tools[0].Source != FromPath {
		t.Fatalf("did not use the operator's own kubectl: %+v", tools)
	}
}

// Anything kmx cannot pin is named as such rather than downloaded blind.
func TestProvisionRefusesAnUnknownTool(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	if _, err := Provision([]string{"docker"}, t.TempDir(), Options{CacheDir: t.TempDir()}); err == nil {
		t.Fatal("an unpinned tool was accepted for download")
	}
}

// TestASubstitutedCacheEntryIsReportedAsDownloadedNotCached.
//
// The Source a run reports is what an operator reads to decide whether
// anything happened to their machine. Deciding it from an os.Stat before the
// fetch got the security-relevant case exactly backwards: a binary somebody
// replaced in the cache is deleted and re-fetched, so the run used the
// DOWNLOADED bytes — but the entry existed when the run started, so a
// pre-flight Stat called it a cache hit. The one case worth surfacing was the
// one that label hid.
func TestASubstitutedCacheEntryIsReportedAsDownloadedNotCached(t *testing.T) {
	binary := []byte("#!/bin/sh\necho kind\n")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, ".sha256sum") {
			fmt.Fprintf(w, "%s  kind-linux-amd64\n", digestOf(binary))
			return
		}
		w.Write(binary)
	}))
	defer srv.Close()

	spec, _ := Pinned("kind", "linux", "amd64")
	opt := Options{CacheDir: t.TempDir(), BaseOverride: srv.URL}

	path, source, err := EnsureReporting(spec, opt)
	if err != nil {
		t.Fatalf("first fetch: %v", err)
	}
	if source != FromDownload {
		t.Errorf("first fetch reported %q, want %q", source, FromDownload)
	}

	// An intact cache entry really was used, and says so.
	if _, source, err = EnsureReporting(spec, opt); err != nil {
		t.Fatal(err)
	}
	if source != FromCache {
		t.Errorf("an intact cache entry reported %q, want %q", source, FromCache)
	}

	// Substituted: re-verification fails, the entry is replaced, and the run
	// must not call that a cache hit.
	if err := os.WriteFile(path, []byte("#!/bin/sh\necho pwned\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, source, err = EnsureReporting(spec, opt); err != nil {
		t.Fatalf("a tampered entry should be replaced, not fatal: %v", err)
	}
	if source != FromDownload {
		t.Fatalf("a tampered cache entry was reported as %q — the run re-downloaded it, and saying "+
			"%q is the wrong way round for the one case that matters", source, FromCache)
	}
}

// Provision reports what the fetch actually did, not what a Stat guessed.
func TestProvisionReportsTheSourceTheFetchUsed(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	binary := []byte("#!/bin/sh\necho kind\n")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, ".sha256sum") {
			fmt.Fprintf(w, "%s  kind-linux-amd64\n", digestOf(binary))
			return
		}
		w.Write(binary)
	}))
	defer srv.Close()

	cache, link := t.TempDir(), t.TempDir()
	opt := Options{CacheDir: cache, BaseOverride: srv.URL}
	spec, _ := Pinned("kind", "linux", "amd64")

	if _, _, err := EnsureReporting(spec, opt); err != nil {
		t.Fatal(err)
	}
	// Tamper, then provision: the entry exists, so the old pre-flight Stat
	// would have reported "cache".
	if err := os.WriteFile(spec.CachePath(cache), []byte("#!/bin/sh\necho pwned\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	tools, err := Provision([]string{"kind"}, link, opt)
	if err != nil {
		t.Fatal(err)
	}
	if len(tools) != 1 {
		t.Fatalf("provisioned %d tools, want 1", len(tools))
	}
	if tools[0].Source != FromDownload {
		t.Fatalf("Provision reported %q for a re-downloaded binary, want %q", tools[0].Source, FromDownload)
	}
}

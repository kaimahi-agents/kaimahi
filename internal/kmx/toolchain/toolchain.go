// Package toolchain fetches the external binaries kmx shells out to, pinned
// and checksum-verified, so that a machine with a container engine and
// nothing else can still run the journey.
//
// This is internal/kmx/kagentcli generalised. That package already had to
// solve the whole problem for the kagent CLI — a kmx installed with
// `go install` or downloaded from a release has no checkout to put a binary
// in, so the binary is cached, keyed by version and platform, and re-verified
// on every use rather than only when it was downloaded. The same reasoning
// applies to kind, kubectl and Helm, which is why the prerequisite list could
// be four items long: kmx knew how to fetch exactly one of the five things it
// needs.
//
// Two rules the kagent path established, kept here because they are the whole
// value of the mechanism:
//
//   - A CACHE HIT IS RE-VERIFIED. "Checksum-verified" has to mean the bytes
//     about to be executed, not the bytes that were fetched some other day.
//     Anything with write access to the cache could otherwise substitute a
//     binary that then runs with the operator's kubeconfig.
//   - A MISMATCH LEAVES NOTHING BEHIND. A rejected download must not be
//     sitting in the cache when the next run looks.
//
// What is deliberately NOT claimed: this is TLS trust in the publisher's own
// download host, not an independent signature. The digest and the bytes come
// from the same origin, so a compromised origin defeats both. It buys
// integrity against a corrupted or truncated transfer and against a tampered
// cache — which is what the kagent CLI path already bought, and refusing to
// grow it to the other three would have been the odd choice.
package toolchain

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// Pinned versions. They are pins rather than "latest" for the reason every
// other version in this repository is pinned: a journey that silently changes
// underneath an operator is not reproducible, and a first run is exactly when
// nobody can tell a new upstream bug from their own mistake.
const (
	KubectlVersion = "1.37.0"
	KindVersion    = "0.33.0"
	HelmVersion    = "3.20.0"
)

// Spec is one fetchable binary.
type Spec struct {
	// Name is the command name, as it appears on PATH.
	Name string
	// Version is the pinned upstream version, without a leading "v".
	Version string
	// URL is what gets downloaded: the binary itself, or an archive when
	// ArchiveMember is set.
	URL string
	// ChecksumURL serves a line whose FIRST field is the sha256 of the bytes
	// at URL. Every upstream here publishes that file; the rest of the line
	// is the publisher's own build path and is not ours to trust.
	ChecksumURL string
	// ArchiveMember, when set, is the path inside a .tar.gz whose bytes are
	// the binary. The published digest covers the ARCHIVE, so the archive is
	// what gets verified, before anything is extracted from it.
	ArchiveMember string
	// Why is the one-line reason this tool is needed, for the fetch line.
	Why string
}

// Platform returns the os/arch pair the upstreams name their assets with.
// Go's GOOS/GOARCH values are already the normalized names all three use.
func Platform() (string, string) { return runtime.GOOS, runtime.GOARCH }

// Pinned returns the spec for one of the tools kmx can fetch. The second
// return is false for anything else — a container engine, notably, which is a
// daemon and a system package rather than a binary that can be dropped into a
// cache directory.
func Pinned(name, goos, goarch string) (Spec, bool) {
	switch name {
	case "kubectl":
		return Spec{
			Name:        "kubectl",
			Version:     KubectlVersion,
			URL:         fmt.Sprintf("https://dl.k8s.io/release/v%s/bin/%s/%s/kubectl", KubectlVersion, goos, goarch),
			ChecksumURL: fmt.Sprintf("https://dl.k8s.io/release/v%s/bin/%s/%s/kubectl.sha256", KubectlVersion, goos, goarch),
			Why:         "to read and write Kubernetes resources",
		}, true
	case "kind":
		return Spec{
			Name:        "kind",
			Version:     KindVersion,
			URL:         fmt.Sprintf("https://github.com/kubernetes-sigs/kind/releases/download/v%s/kind-%s-%s", KindVersion, goos, goarch),
			ChecksumURL: fmt.Sprintf("https://github.com/kubernetes-sigs/kind/releases/download/v%s/kind-%s-%s.sha256sum", KindVersion, goos, goarch),
			Why:         "to manage the local Kubernetes cluster",
		}, true
	case "helm":
		return Spec{
			Name:          "helm",
			Version:       HelmVersion,
			URL:           fmt.Sprintf("https://get.helm.sh/helm-v%s-%s-%s.tar.gz", HelmVersion, goos, goarch),
			ChecksumURL:   fmt.Sprintf("https://get.helm.sh/helm-v%s-%s-%s.tar.gz.sha256sum", HelmVersion, goos, goarch),
			ArchiveMember: fmt.Sprintf("%s-%s/helm", goos, goarch),
			Why:           "to install kagent",
		}, true
	}
	return Spec{}, false
}

// Fetchable lists the tools Pinned knows, in the order an operator meets them.
var Fetchable = []string{"kind", "kubectl", "helm"}

// ExpectedDigest extracts the digest from a published checksum file.
//
// The file is `<digest>[  <path>]`, and the path is the PUBLISHER's, not
// anything on this machine — so only the first field may be trusted, and a
// file that does not start with a 64-character hex digest is refused rather
// than compared loosely. kubectl publishes the bare digest with no path at
// all, which is the same rule with one field.
func ExpectedDigest(checksumFile []byte) (string, error) {
	fields := strings.Fields(string(checksumFile))
	if len(fields) == 0 {
		return "", fmt.Errorf("checksum file is empty")
	}
	digest := strings.ToLower(fields[0])
	if len(digest) != 64 {
		return "", fmt.Errorf("checksum file does not begin with a sha256 digest")
	}
	if _, err := hex.DecodeString(digest); err != nil {
		return "", fmt.Errorf("checksum file does not begin with a sha256 digest")
	}
	return digest, nil
}

// Verify fails closed when bytes do not match a published digest.
func Verify(what string, payload, checksumFile []byte) error {
	want, err := ExpectedDigest(checksumFile)
	if err != nil {
		return fmt.Errorf("%s: %w", what, err)
	}
	sum := sha256.Sum256(payload)
	if got := hex.EncodeToString(sum[:]); got != want {
		return fmt.Errorf("%s checksum mismatch: got %s, want %s", what, got, want)
	}
	return nil
}

// Options configure a fetch.
type Options struct {
	CacheDir string
	// Log receives one line when a download actually happens. Fetching a
	// binary onto someone's machine is not something to do silently.
	Log io.Writer
	// BaseOverride replaces the scheme+host of both URLs (tests).
	BaseOverride string
}

func digest(payload []byte) string {
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:])
}

// CachePath is where a spec's binary lives once fetched. The version is in
// the name, so bumping a pin can never be served the previous version.
func (s Spec) CachePath(cacheDir string) string {
	goos, goarch := Platform()
	return filepath.Join(cacheDir, fmt.Sprintf("%s-%s-%s-%s", s.Name, s.Version, goos, goarch))
}

// Ensure returns the path to a verified binary, downloading it only when the
// cache does not already hold that exact version — and re-verifying it when
// it does.
func Ensure(spec Spec, opt Options) (string, error) {
	path, _, err := EnsureReporting(spec, opt)
	return path, err
}

// EnsureReporting is Ensure, and also says which of the two things happened.
//
// The caller cannot work this out beforehand: a cache entry that EXISTS may
// still be re-fetched here, because an entry whose recorded digest no longer
// matches is deleted rather than run. Deciding from an os.Stat before the
// call reports the tampered case — the one an operator most needs named — as
// an ordinary cache hit.
func EnsureReporting(spec Spec, opt Options) (string, Source, error) {
	path := spec.CachePath(opt.CacheDir)

	// A cache hit is re-verified against the digest recorded beside it when
	// it was installed. Hashing a few tens of megabytes costs milliseconds; a
	// substituted binary holds the kubeconfig.
	if cached, err := os.ReadFile(path); err == nil {
		if recorded, err := os.ReadFile(path + ".sha256"); err == nil && Verify(spec.Name, cached, recorded) == nil {
			return path, FromCache, nil
		}
		if opt.Log != nil {
			fmt.Fprintf(opt.Log, "cached %s at %s does not match its recorded digest — re-fetching\n", spec.Name, path)
		}
		// Unverifiable: no record, or it no longer matches. Fetch again
		// rather than run it.
		_ = os.Remove(path)
		_ = os.Remove(path + ".sha256")
	}

	url, checksumURL := spec.URL, spec.ChecksumURL
	if opt.BaseOverride != "" {
		url = rebase(url, opt.BaseOverride)
		checksumURL = rebase(checksumURL, opt.BaseOverride)
	}
	if opt.Log != nil {
		fmt.Fprintf(opt.Log, "fetching %s v%s (%s), checksum-verified — %s\n", spec.Name, spec.Version, url, spec.Why)
	}

	payload, err := get(url)
	if err != nil {
		return "", FromDownload, err
	}
	checksumFile, err := get(checksumURL)
	if err != nil {
		return "", FromDownload, err
	}
	// The published digest covers what was published: the binary, or the
	// archive it travels in. Verify THAT, then extract — never the other way
	// round, which would run an unverified archive through a decompressor.
	if err := Verify(spec.Name, payload, checksumFile); err != nil {
		return "", FromDownload, err
	}
	binary := payload
	if spec.ArchiveMember != "" {
		if binary, err = memberOf(payload, spec.ArchiveMember); err != nil {
			return "", FromDownload, fmt.Errorf("%s: %w", spec.Name, err)
		}
	}

	if err := install(path, binary); err != nil {
		return "", FromDownload, err
	}
	// Record the digest of the file that was installed, so the next run can
	// re-verify what it is about to execute without going back to the
	// network. Written after the rename: a digest with no binary is harmless,
	// a binary with no digest is simply re-fetched.
	line := fmt.Sprintf("%s  %s\n", digest(binary), spec.Name)
	if err := os.WriteFile(path+".sha256", []byte(line), 0o644); err != nil {
		return "", FromDownload, err
	}
	return path, FromDownload, nil
}

// install writes the binary atomically: a killed download must not leave a
// truncated file behind under the real name.
func install(path string, binary []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".partial-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(binary); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), 0o755); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// memberOf pulls one file out of a verified .tar.gz.
func memberOf(archive []byte, member string) ([]byte, error) {
	zr, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		return nil, fmt.Errorf("cannot read the downloaded archive: %w", err)
	}
	defer zr.Close()
	tr := tar.NewReader(zr)
	for {
		header, err := tr.Next()
		if err == io.EOF {
			return nil, fmt.Errorf("the archive does not contain %s", member)
		}
		if err != nil {
			return nil, fmt.Errorf("cannot read the downloaded archive: %w", err)
		}
		if header.Name != member || header.Typeflag != tar.TypeReg {
			continue
		}
		// Bounded: an archive claiming a preposterous size must not be able
		// to exhaust memory on the way to being rejected.
		body, err := io.ReadAll(io.LimitReader(tr, 512<<20))
		if err != nil {
			return nil, fmt.Errorf("cannot read %s out of the archive: %w", member, err)
		}
		return body, nil
	}
}

func rebase(url, base string) string {
	if i := strings.Index(url, "://"); i >= 0 {
		if j := strings.Index(url[i+3:], "/"); j >= 0 {
			return strings.TrimSuffix(base, "/") + url[i+3+j:]
		}
	}
	return url
}

func get(url string) ([]byte, error) {
	client := &http.Client{Timeout: 5 * time.Minute}
	resp, err := client.Get(url)
	if err != nil {
		return nil, fmt.Errorf("cannot download %s: %w", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("cannot download %s: HTTP %d", url, resp.StatusCode)
	}
	return io.ReadAll(resp.Body)
}

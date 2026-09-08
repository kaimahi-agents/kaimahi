package seamtls

import (
	"crypto/tls"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func writeFile(path, body string) error { return os.WriteFile(path, []byte(body), 0o600) }

// scripts/plane-upgrade-probe.sh runs the plane outside a cluster, so it mints
// the seam material with openssl rather than through `kmx plane`. This is the
// check that the two agree: the loader takes what that recipe produces, and a
// client verifying against the same authority completes a handshake with it.
//
// It is here rather than in the probe because the probe needs Postgres and a
// published old build to run at all, so a mismatch in the recipe would surface
// as a proxy that "exited before it served" deep inside a CI shard — a long
// way from the four openssl lines that caused it.
func TestTheMaterialTheUpgradeProbeMintsIsMaterialThePlaneCanServe(t *testing.T) {
	openssl, err := exec.LookPath("openssl")
	if err != nil {
		t.Skip("openssl is not installed")
	}
	dir := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		if out, err := exec.Command(openssl, args...).CombinedOutput(); err != nil {
			t.Fatalf("openssl %v: %v\n%s", args, err, out)
		}
	}
	// The same four commands, in the same order, as the probe.
	run("req", "-x509", "-newkey", "ec", "-pkeyopt", "ec_paramgen_curve:prime256v1", "-nodes",
		"-keyout", filepath.Join(dir, "ca.key"), "-out", filepath.Join(dir, authorityFile),
		"-days", "2", "-subj", "/CN=kaimahi-plane-ca-upgrade-probe")
	run("req", "-newkey", "ec", "-pkeyopt", "ec_paramgen_curve:prime256v1", "-nodes",
		"-keyout", filepath.Join(dir, keyFile), "-out", filepath.Join(dir, "tls.csr"),
		"-subj", "/CN=kaimahi-plane-seams")
	ext := filepath.Join(dir, "san.ext")
	if err := writeFile(ext, "subjectAltName=DNS:localhost,IP:127.0.0.1\nextendedKeyUsage=serverAuth\n"); err != nil {
		t.Fatal(err)
	}
	run("x509", "-req", "-in", filepath.Join(dir, "tls.csr"),
		"-CA", filepath.Join(dir, authorityFile), "-CAkey", filepath.Join(dir, "ca.key"),
		"-CAcreateserial", "-out", filepath.Join(dir, certFile), "-days", "2", "-extfile", ext)

	m, err := Load(dir)
	if err != nil {
		t.Fatalf("the plane cannot load what the probe mints: %v", err)
	}

	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "ok")
	}))
	srv.TLS = m.ServerConfig()
	srv.StartTLS()
	defer srv.Close()

	resp, err := m.LoopbackClient().Get(srv.URL)
	if err != nil {
		t.Fatalf("a client carrying the same authority could not verify the seam: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if string(body) != "ok" {
		t.Errorf("body = %q", body)
	}
	if resp.TLS == nil || resp.TLS.Version < tls.VersionTLS12 {
		t.Error("the connection was not TLS 1.2 or better")
	}
}

package seamtls

import (
	"crypto/tls"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// write lays out a mount the way the projected Secret does.
func write(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestLoadReadsTheProjectedSecret(t *testing.T) {
	dir := write(t, testMount(t))
	m, err := Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if m.Leaf.Subject.CommonName == "" {
		t.Error("the loaded certificate has no subject to name in an error")
	}
	if m.Leaf.Issuer.CommonName == "" {
		t.Error("the loaded certificate has no issuer to name in an error")
	}
}

// The proxy must not start without the material. A listener that quietly
// falls back to plaintext would report as governed and be in the clear, which
// is worse than never having claimed it.
func TestLoadRefusesAnIncompleteMount(t *testing.T) {
	full := testMount(t)
	for _, missing := range []string{certFile, keyFile, authorityFile} {
		t.Run("without "+missing, func(t *testing.T) {
			files := map[string]string{}
			for k, v := range full {
				if k != missing {
					files[k] = v
				}
			}
			_, err := Load(write(t, files))
			if err == nil {
				t.Fatal("an incomplete mount loaded")
			}
			if !strings.Contains(err.Error(), missing) {
				t.Errorf("the error does not name the missing file %q: %v", missing, err)
			}
		})
	}
}

func TestLoadRefusesAKeyThatDoesNotMatchTheCertificate(t *testing.T) {
	files := testMount(t)
	other := testMount(t)
	files[keyFile] = other[keyFile]
	_, err := Load(write(t, files))
	if err == nil {
		t.Fatal("a certificate loaded with somebody else's key")
	}
}

// The whole point: a client holding the authority connects, and one that
// does not is refused. Both halves are asserted against a real handshake.
func TestAListenerServesTheSeamAndOnlyToAClientThatVerifiesIt(t *testing.T) {
	files := testMount(t)
	m, err := Load(write(t, files))
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "governed")
	}))
	srv.TLS = m.ServerConfig()
	srv.StartTLS()
	defer srv.Close()

	t.Run("the plane's own loopback client", func(t *testing.T) {
		resp, err := m.LoopbackClient().Get(srv.URL)
		if err != nil {
			t.Fatalf("the plane could not reach its own seam: %v", err)
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		if string(body) != "governed" {
			t.Errorf("body = %q", body)
		}
	})

	t.Run("a client trusting a different authority", func(t *testing.T) {
		stranger, err := Load(write(t, testMount(t)))
		if err != nil {
			t.Fatal(err)
		}
		_, err = stranger.LoopbackClient().Get(srv.URL)
		if err == nil {
			t.Fatal("a client trusting a different authority connected")
		}
	})

	t.Run("a client with no custom trust at all", func(t *testing.T) {
		if _, err := http.Get(srv.URL); err == nil {
			t.Fatal("a client with only the system trust store connected")
		}
	})
}

// A client can check the chain and skip the hostname; this proves the
// listener's own material is not the reason that would pass.
func TestTheServedCertificateIsRejectedForAHostnameItDoesNotCarry(t *testing.T) {
	m, err := Load(write(t, testMount(t)))
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	srv.TLS = m.ServerConfig()
	srv.StartTLS()
	defer srv.Close()

	client := m.LoopbackClient()
	transport := client.Transport.(*http.Transport)
	cfg := transport.TLSClientConfig.Clone()
	cfg.ServerName = "kaimahi-proxy.somewhere-else.svc.cluster.local"
	transport.TLSClientConfig = cfg

	_, err = client.Get(srv.URL)
	if err == nil {
		t.Fatal("a hostname the certificate does not carry was accepted")
	}
	if !strings.Contains(err.Error(), "certificate is valid for") {
		t.Errorf("want a hostname failure naming the certificate, got: %v", err)
	}
}

func TestServerConfigDoesNotSpeakAnObsoleteProtocol(t *testing.T) {
	m, err := Load(write(t, testMount(t)))
	if err != nil {
		t.Fatal(err)
	}
	if got := m.ServerConfig().MinVersion; got < tls.VersionTLS12 {
		t.Errorf("MinVersion = %x, want at least TLS 1.2", got)
	}
}

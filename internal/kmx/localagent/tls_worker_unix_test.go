//go:build !windows

package localagent

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

// The test-only launcher supplies trust for its own TLS fixture without changing
// production TLS verification or placing an endpoint credential in the environment.
func residentTLSWorker(t *testing.T, server *httptest.Server) string {
	t.Helper()
	root := t.TempDir()
	cert := filepath.Join(root, "ca.pem")
	if err := os.WriteFile(cert, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw}), 0600); err != nil {
		t.Fatal(err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	launcher := filepath.Join(root, "worker")
	body := "#!/bin/sh\nunset KMX_LOCAL_AGENT_WORKER\nexport KMX_TEST_TLS_CERT=" + shellQuote(cert) + "\nexec " + shellQuote(executable) + " -test.run=^TestResidentTLSWorker$\n"
	if err := os.WriteFile(launcher, []byte(body), 0700); err != nil {
		t.Fatal(err)
	}
	return launcher
}

func TestResidentTLSWorker(t *testing.T) {
	path := os.Getenv("KMX_TEST_TLS_CERT")
	if path == "" {
		return
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(raw) {
		t.Fatal("fixture certificate unavailable")
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.TLSClientConfig = &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}
	http.DefaultTransport = transport
	defer transport.CloseIdleConnections()
	f := os.NewFile(4, "credential")
	var credential localAgentCredentialSnapshot
	err = json.NewDecoder(io.LimitReader(f, 128<<10)).Decode(&credential)
	f.Close()
	if err != nil {
		t.Fatal("fixture credential pipe invalid")
	}
	if err := runLocalAgentWorker(t.Context(), os.Getenv("KMX_LOCAL_AGENT_ID"), os.Getenv("KMX_LOCAL_AGENT_EVENT"), credential, os.NewFile(3, "readiness")); err != nil {
		t.Fatal(err)
	}
}

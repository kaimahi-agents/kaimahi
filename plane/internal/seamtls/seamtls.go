// Package seamtls loads the certificate the plane serves its two data seams
// with, and builds the client the plane dials its own seams by.
//
// What crosses those seams is why they are encrypted. The model seam carries
// the full text of what an agent was asked and what it answered; the tool
// seam carries the full body of what a tool returned. Neither is written to
// any artifact the plane keeps — the ledger holds identifiers, token counts
// and cost, and the tool audit holds a capped summary of declared argument
// fields — so that content exists in no other record and capture is the only
// way to obtain it.
//
// The material is minted by `kmx plane` and arrives as a projected Secret.
// The plane never mints and never holds the authority's private key: it has
// the serving key, which signs handshakes and cannot issue anything.
//
// Loading is fail-closed and says which file is missing. A listener that fell
// back to plaintext when its material was absent would be the worst of the
// options — indistinguishable from a governed one to everything that looks,
// and in the clear to anything that captures.
package seamtls

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

// The keys of the projected Secret. `tls.crt` and `tls.key` are what a
// kubernetes.io/tls Secret is required to carry; `ca.crt` rides along in the
// same Secret so the plane can verify its own seams without a second mount.
const (
	certFile      = "tls.crt"
	keyFile       = "tls.key"
	authorityFile = "ca.crt"
)

// Material is everything the plane needs to serve a seam and to reach one.
type Material struct {
	// Leaf is kept parsed so a message about this certificate can name it
	// — its subject, its issuer and when it expires — rather than leaving
	// an operator with a connection error that says nothing.
	Leaf *x509.Certificate

	certificate tls.Certificate
	roots       *x509.CertPool
}

// Load reads the material out of a mounted Secret directory.
func Load(dir string) (*Material, error) {
	certPEM, err := read(dir, certFile)
	if err != nil {
		return nil, err
	}
	keyPEM, err := read(dir, keyFile)
	if err != nil {
		return nil, err
	}
	caPEM, err := read(dir, authorityFile)
	if err != nil {
		return nil, err
	}

	// X509KeyPair is where a certificate and an unrelated key are caught.
	// Left to the handshake it would present as a client-side failure on a
	// cluster that looks correctly configured from every angle an operator
	// can see.
	certificate, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return nil, fmt.Errorf("the seam certificate and key in %s do not form a pair: %w", dir, err)
	}
	leaf, err := x509.ParseCertificate(certificate.Certificate[0])
	if err != nil {
		return nil, fmt.Errorf("reading the seam certificate in %s: %w", dir, err)
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(caPEM) {
		return nil, fmt.Errorf("%s holds no PEM certificate", filepath.Join(dir, authorityFile))
	}
	// The third pairing, and the one a half-applied Secret breaks: the
	// certificate this process SERVES has to be one the authority it ships
	// beside actually signed.
	//
	// All three files load individually without complaint when they do not
	// belong together — a restored backup, a hand-edited Secret, a partial
	// apply — and the startup log would then name a certificate nothing can
	// verify. What follows is a liveness probe failing on loopback and a
	// restart loop, which says nothing about the cause. The material is all
	// in hand here, so the check costs one signature verification.
	//
	// The SIGNATURE, not a chain: a chain is verified at a time, and the
	// question here is who signed this, which has no clock in it. Expiry is
	// reported separately and by name.
	authority, err := x509.ParseCertificate(rootCert(caPEM))
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", filepath.Join(dir, authorityFile), err)
	}
	if err := leaf.CheckSignatureFrom(authority); err != nil {
		return nil, fmt.Errorf(
			"the seam certificate in %s was not signed by the authority beside it "+
				"(%q is not the issuer of %q): %w",
			dir, authority.Subject.CommonName, leaf.Subject.CommonName, err)
	}
	return &Material{Leaf: leaf, certificate: certificate, roots: roots}, nil
}

// ServerConfig is what the two seam listeners serve with.
func (m *Material) ServerConfig() *tls.Config {
	return &tls.Config{
		Certificates: []tls.Certificate{m.certificate},
		MinVersion:   tls.VersionTLS12,
	}
}

// LoopbackClient is how the liveness probe asks each data listener
// whether it is answering. Verification stays on even over loopback:
// skipping it would hide an expired seam certificate from the probe.
func (m *Material) LoopbackClient() *http.Client {
	return &http.Client{Timeout: 2 * time.Second, Transport: m.Transport()}
}

// Transport verifies the plane's own authority when dialing a seam.
func (m *Material) Transport() *http.Transport {
	return &http.Transport{
		TLSClientConfig: &tls.Config{RootCAs: m.roots, MinVersion: tls.VersionTLS12},
	}
}

// Describe names the certificate for a log line or an error.
func (m *Material) Describe() string {
	return fmt.Sprintf("%s (issued by %s), expires %s",
		m.Leaf.Subject.CommonName, m.Leaf.Issuer.CommonName, m.Leaf.NotAfter.UTC().Format(time.RFC3339))
}

// ExpiresIn is published as a metric, so the certificate's remaining life is
// something a dashboard can alert on rather than something a person has to
// remember to look at.
func (m *Material) ExpiresIn(now time.Time) time.Duration { return m.Leaf.NotAfter.Sub(now) }

// rootCert returns the DER of the FIRST certificate in a PEM bundle. The
// authority file carries exactly one today; taking the first keeps a future
// bundle from turning this check into a parse error.
func rootCert(caPEM []byte) []byte {
	block, _ := pem.Decode(caPEM)
	if block == nil {
		return nil
	}
	return block.Bytes
}

func read(dir, name string) ([]byte, error) {
	path := filepath.Join(dir, name)
	raw, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("the seam certificate is not mounted: %s is missing "+
				"(it comes from the kaimahi-plane-seam-tls Secret — run `kmx plane`)", path)
		}
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}
	if len(raw) == 0 {
		return nil, fmt.Errorf("%s is empty", path)
	}
	return raw, nil
}

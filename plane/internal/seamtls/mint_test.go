package seamtls

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"testing"
	"time"
)

// testMount produces the three files the projected Secret carries, minted the
// way `kmx plane` mints them: a self-signed authority, and one serving
// certificate under it covering the loopback names the plane dials itself by.
//
// The plane only ever LOADS this material — minting belongs to kmx, which is
// the only side that holds the authority's key. What the plane needs in a
// test is something real to hand a client, so this makes one rather than
// carrying a fixture that would expire.
func testMount(t *testing.T) map[string]string {
	t.Helper()
	return mountAt(t, time.Now())
}

// expiredMount is the same material, signed long enough ago that its life is
// over — the state a cluster nobody has deployed to in over a year is in.
func expiredMount(t *testing.T) map[string]string {
	t.Helper()
	return mountAt(t, time.Now().Add(-400*24*time.Hour))
}

func mountAt(t *testing.T, now time.Time) map[string]string {
	t.Helper()

	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	caTemplate := &x509.Certificate{
		SerialNumber:          serial(t),
		Subject:               pkix.Name{CommonName: "kaimahi-plane-ca"},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageCertSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTemplate, caTemplate, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	ca, err := x509.ParseCertificate(caDER)
	if err != nil {
		t.Fatal(err)
	}

	leafKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	leafDER, err := x509.CreateCertificate(rand.Reader, &x509.Certificate{
		SerialNumber:          serial(t),
		Subject:               pkix.Name{CommonName: "kaimahi-plane-seams"},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		DNSNames:              []string{"kaimahi-proxy.kaimahi", "localhost"},
		IPAddresses:           []net.IP{net.IPv4(127, 0, 0, 1), net.IPv6loopback},
	}, ca, &leafKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}

	return map[string]string{
		certFile:      string(encode(t, "CERTIFICATE", leafDER)),
		keyFile:       string(encode(t, "PRIVATE KEY", marshal(t, leafKey))),
		authorityFile: string(encode(t, "CERTIFICATE", caDER)),
	}
}

func serial(t *testing.T) *big.Int {
	t.Helper()
	n, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func marshal(t *testing.T, key *ecdsa.PrivateKey) []byte {
	t.Helper()
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return der
}

func encode(t *testing.T, kind string, der []byte) []byte {
	t.Helper()
	return pem.EncodeToMemory(&pem.Block{Type: kind, Bytes: der})
}

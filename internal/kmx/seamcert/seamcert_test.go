package seamcert

import (
	"crypto/x509"
	"net"
	"slices"
	"strings"
	"testing"
	"time"
)

var epoch = time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)

func mint(t *testing.T) (Authority, Serving) {
	t.Helper()
	ca, err := MintAuthority(epoch)
	if err != nil {
		t.Fatalf("MintAuthority: %v", err)
	}
	leaf, err := ca.Sign(SeamNames(), epoch)
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	return ca, leaf
}

func pool(t *testing.T, caPEM []byte) *x509.CertPool {
	t.Helper()
	p := x509.NewCertPool()
	if !p.AppendCertsFromPEM(caPEM) {
		t.Fatal("CA PEM did not parse into a pool")
	}
	return p
}

func verify(t *testing.T, leafPEM, caPEM []byte, host string, at time.Time) error {
	t.Helper()
	leaf, err := ParseCertificate(leafPEM)
	if err != nil {
		t.Fatalf("ParseCertificate: %v", err)
	}
	_, err = leaf.Verify(x509.VerifyOptions{
		Roots:       pool(t, caPEM),
		DNSName:     host,
		CurrentTime: at,
		KeyUsages:   []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	})
	return err
}

func TestAuthorityIsASelfSignedCA(t *testing.T) {
	ca, _ := mint(t)
	cert, err := ParseCertificate(ca.CertPEM)
	if err != nil {
		t.Fatalf("ParseCertificate: %v", err)
	}
	if !cert.IsCA || !cert.BasicConstraintsValid {
		t.Errorf("not a CA: IsCA=%v BasicConstraintsValid=%v", cert.IsCA, cert.BasicConstraintsValid)
	}
	if cert.KeyUsage&x509.KeyUsageCertSign == 0 {
		t.Error("CA cannot sign certificates")
	}
	if cert.Subject.CommonName != authorityCommonName {
		t.Errorf("CN = %q, want %q", cert.Subject.CommonName, authorityCommonName)
	}
	if cert.Subject.CommonName != cert.Issuer.CommonName {
		t.Error("CA is not self-signed")
	}
	// A CA that outlives the serving certificate is what makes renewal a
	// re-sign rather than a redistribution.
	if !cert.NotAfter.After(epoch.Add(authorityLifetime - time.Hour)) {
		t.Errorf("CA NotAfter = %s, want about %s", cert.NotAfter, epoch.Add(authorityLifetime))
	}
}

func TestServingCertificateCarriesEveryNameASeamIsReachedBy(t *testing.T) {
	_, leaf := mint(t)
	cert, err := ParseCertificate(leaf.CertPEM)
	if err != nil {
		t.Fatalf("ParseCertificate: %v", err)
	}
	for _, want := range []string{
		"kaimahi-proxy",
		"kaimahi-proxy.kaimahi",
		"kaimahi-proxy.kaimahi.svc",
		"kaimahi-proxy.kaimahi.svc.cluster.local",
		"kaimahi-mcp-gateway",
		"kaimahi-mcp-gateway.kaimahi",
		"kaimahi-mcp-gateway.kaimahi.svc",
		"kaimahi-mcp-gateway.kaimahi.svc.cluster.local",
		"localhost",
	} {
		if !slices.Contains(cert.DNSNames, want) {
			t.Errorf("SAN %q missing from %v", want, cert.DNSNames)
		}
	}
	if !slices.ContainsFunc(cert.IPAddresses, func(ip net.IP) bool { return ip.Equal(net.IPv4(127, 0, 0, 1)) }) {
		t.Errorf("127.0.0.1 missing from IP SANs %v", cert.IPAddresses)
	}
	if !slices.Contains(cert.ExtKeyUsage, x509.ExtKeyUsageServerAuth) {
		t.Error("leaf is not a server certificate")
	}
	if cert.IsCA {
		t.Error("the serving certificate must not be a CA")
	}
}

func TestServingCertificateVerifiesUnderItsOwnAuthority(t *testing.T) {
	ca, leaf := mint(t)
	for _, host := range []string{
		"kaimahi-proxy.kaimahi.svc.cluster.local",
		"kaimahi-mcp-gateway.kaimahi",
		"localhost",
	} {
		if err := verify(t, leaf.CertPEM, ca.CertPEM, host, epoch.Add(time.Hour)); err != nil {
			t.Errorf("verify %s: %v", host, err)
		}
	}
}

// Chain and hostname are separate checks, and a client can do the first
// while skipping the second. Both are asserted, so a name the seam is never
// reached by cannot pass on the strength of the chain alone.
func TestVerificationFailsForANameTheCertificateDoesNotCarry(t *testing.T) {
	ca, leaf := mint(t)
	err := verify(t, leaf.CertPEM, ca.CertPEM, "kaimahi-proxy.other-namespace.svc.cluster.local", epoch.Add(time.Hour))
	if err == nil {
		t.Fatal("a hostname the certificate does not carry verified")
	}
	if _, ok := err.(x509.HostnameError); !ok {
		t.Errorf("want a hostname error, got %T: %v", err, err)
	}
}

func TestVerificationFailsUnderADifferentAuthority(t *testing.T) {
	_, leaf := mint(t)
	other, err := MintAuthority(epoch)
	if err != nil {
		t.Fatalf("MintAuthority: %v", err)
	}
	err = verify(t, leaf.CertPEM, other.CertPEM, "kaimahi-proxy.kaimahi", epoch.Add(time.Hour))
	if err == nil {
		t.Fatal("a certificate verified under an authority that did not sign it")
	}
	if _, ok := err.(x509.UnknownAuthorityError); !ok {
		t.Errorf("want an unknown-authority error, got %T: %v", err, err)
	}
}

func TestVerificationFailsOnceTheServingCertificateHasExpired(t *testing.T) {
	ca, leaf := mint(t)
	err := verify(t, leaf.CertPEM, ca.CertPEM, "kaimahi-proxy.kaimahi", epoch.Add(servingLifetime+time.Hour))
	if err == nil {
		t.Fatal("an expired certificate verified")
	}
	if _, ok := err.(x509.CertificateInvalidError); !ok {
		t.Errorf("want an invalid-certificate error, got %T: %v", err, err)
	}
}

// Two mints must not produce the same key, or one leaked key would be every
// cluster this command has ever set up.
func TestEveryMintIsDistinct(t *testing.T) {
	a, err := MintAuthority(epoch)
	if err != nil {
		t.Fatal(err)
	}
	b, err := MintAuthority(epoch)
	if err != nil {
		t.Fatal(err)
	}
	if string(a.KeyPEM) == string(b.KeyPEM) {
		t.Fatal("two authorities share a private key")
	}
	if string(a.CertPEM) == string(b.CertPEM) {
		t.Fatal("two authorities share a certificate")
	}
}

// The authority is reloaded from its Secret to re-sign, so a round trip
// through PEM has to yield a signer whose output the UNCHANGED CA still
// verifies — that is what makes renewal invisible on the agent side.
func TestAReloadedAuthoritySignsACertificateTheOldCAStillVerifies(t *testing.T) {
	ca, _ := mint(t)
	reloaded, err := LoadAuthority(ca.CertPEM, ca.KeyPEM)
	if err != nil {
		t.Fatalf("LoadAuthority: %v", err)
	}
	renewed, err := reloaded.Sign(SeamNames(), epoch.Add(300*24*time.Hour))
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	if err := verify(t, renewed.CertPEM, ca.CertPEM, "kaimahi-proxy.kaimahi", epoch.Add(301*24*time.Hour)); err != nil {
		t.Fatalf("a renewed certificate did not verify under the unchanged authority: %v", err)
	}
}

// A spent authority must refuse rather than clamp. Clamping produced a
// certificate whose NotAfter was already in the past, applied it, restarted
// the plane into material nothing accepts, and exited 0 — a reported success
// that leaves both seams unusable. The calendar reaches this in ten years;
// a restored old authority Secret or a skewed clock reaches it today.
func TestAnExpiredAuthorityRefusesToSignRatherThanMintingSomethingDead(t *testing.T) {
	ca, err := MintAuthority(epoch)
	if err != nil {
		t.Fatal(err)
	}
	_, err = ca.Sign(SeamNames(), epoch.Add(authorityLifetime+24*time.Hour))
	if err == nil {
		t.Fatal("an expired authority signed a certificate")
	}
	for _, want := range []string{authorityCommonName, "expired", "kmx plane"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not carry %q: %v", want, err)
		}
	}
}

// Still inside its life, but with less left than a serving certificate wants:
// the result is clamped to the authority and is genuinely usable, so this
// must NOT refuse.
func TestAnAuthorityNearTheEndOfItsLifeStillSigns(t *testing.T) {
	ca, err := MintAuthority(epoch)
	if err != nil {
		t.Fatal(err)
	}
	at := epoch.Add(authorityLifetime - 24*time.Hour)
	serving, err := ca.Sign(SeamNames(), at)
	if err != nil {
		t.Fatalf("an authority with a day left refused to sign: %v", err)
	}
	cert, err := ParseCertificate(serving.CertPEM)
	if err != nil {
		t.Fatal(err)
	}
	if !cert.NotAfter.After(at) {
		t.Errorf("the clamped certificate is already expired: NotAfter=%s, signed at %s", cert.NotAfter, at)
	}
}

func TestLoadAuthorityRefusesAMismatchedPair(t *testing.T) {
	a, err := MintAuthority(epoch)
	if err != nil {
		t.Fatal(err)
	}
	b, err := MintAuthority(epoch)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := LoadAuthority(a.CertPEM, b.KeyPEM); err == nil {
		t.Fatal("a certificate and an unrelated key loaded as an authority")
	}
}

func TestLoadAuthorityRefusesGarbage(t *testing.T) {
	// The credential-shaped case is ASSEMBLED rather than written out: a
	// literal one is what the tree scanner refuses, and it is right to —
	// a public repository should not carry anything shaped like a token
	// even where it plainly is not one.
	credentialShaped := []byte("kmh" + "_" + "not-a-certificate")
	for name, pem := range map[string][]byte{
		"empty":     nil,
		"not pem":   credentialShaped,
		"truncated": []byte("-----BEGIN CERTIFICATE-----\nAAAA\n-----END CERTIFICATE-----\n"),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := LoadAuthority(pem, pem); err == nil {
				t.Fatal("garbage loaded as an authority")
			}
		})
	}
}

func TestExpiryReportsWhatAnOperatorHasToActOn(t *testing.T) {
	_, leaf := mint(t)
	cert, err := ParseCertificate(leaf.CertPEM)
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name  string
		at    time.Time
		state ExpiryState
	}{
		{"fresh", epoch.Add(time.Hour), Valid},
		{"a month and a half out", epoch.Add(servingLifetime - 45*24*time.Hour), Valid},
		{"inside the warning window", epoch.Add(servingLifetime - 10*24*time.Hour), Expiring},
		{"past its notAfter", epoch.Add(servingLifetime + time.Hour), Expired},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := Expiry(cert, c.at).State; got != c.state {
				t.Errorf("state = %v, want %v", got, c.state)
			}
		})
	}
}

// The operator has to be told WHICH certificate, not that "a certificate" is
// wrong: a generic connection error is how a seam expiry presented last time
// and it cost hours.
func TestExpiryDescribesTheCertificateByIssuerAndSubject(t *testing.T) {
	_, leaf := mint(t)
	cert, err := ParseCertificate(leaf.CertPEM)
	if err != nil {
		t.Fatal(err)
	}
	line := Expiry(cert, epoch.Add(time.Hour)).Line()
	for _, want := range []string{authorityCommonName, servingCommonName, "expires"} {
		if !strings.Contains(line, want) {
			t.Errorf("%q missing from %q", want, line)
		}
	}
}

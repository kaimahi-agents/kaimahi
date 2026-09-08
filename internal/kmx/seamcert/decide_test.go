package seamcert

import (
	"crypto/x509"
	"strings"
	"testing"
	"time"
)

func certs(t *testing.T) (*x509.Certificate, *x509.Certificate) {
	t.Helper()
	ca, leaf := mint(t)
	authority, err := ParseCertificate(ca.CertPEM)
	if err != nil {
		t.Fatal(err)
	}
	serving, err := ParseCertificate(leaf.CertPEM)
	if err != nil {
		t.Fatal(err)
	}
	return serving, authority
}

func TestDecideKeepsAHealthyCertificateAndSaysWhy(t *testing.T) {
	serving, authority := certs(t)
	got := Decide(serving, authority, epoch.Add(time.Hour))
	if got.Sign {
		t.Fatalf("a healthy certificate was replaced: %s", got.Reason)
	}
	if !strings.Contains(got.Reason, "expires in") {
		t.Errorf("keeping a certificate should still say when it expires, got %q", got.Reason)
	}
}

func TestDecideSignsWhenThereIsNoCertificateYet(t *testing.T) {
	_, authority := certs(t)
	got := Decide(nil, authority, epoch)
	if !got.Sign {
		t.Fatal("a cluster with no certificate was left without one")
	}
	if !strings.Contains(got.Reason, "no serving certificate") {
		t.Errorf("reason = %q", got.Reason)
	}
}

func TestDecideSignsOnceTheCertificateEntersItsRenewalWindow(t *testing.T) {
	serving, authority := certs(t)
	got := Decide(serving, authority, epoch.Add(servingLifetime-10*24*time.Hour))
	if !got.Sign {
		t.Fatal("a certificate inside its renewal window was kept")
	}
	// The reason has to name the certificate, because this line is the
	// operator's only notice that an expiry was coming.
	if !strings.Contains(got.Reason, servingCommonName) {
		t.Errorf("the renewal reason does not name the certificate: %q", got.Reason)
	}
}

// Somebody replaced the authority — a restored backup, a deleted Secret. The
// old serving certificate still parses and has not expired, so nothing about
// it looks wrong; it simply no longer chains to what agents are told to
// trust, and every call would fail with an unknown-authority error.
func TestDecideSignsWhenTheAuthorityNoLongerMatches(t *testing.T) {
	serving, _ := certs(t)
	_, other := certs(t)
	got := Decide(serving, other, epoch.Add(time.Hour))
	if !got.Sign {
		t.Fatal("a certificate that does not chain to the current authority was kept")
	}
	if !strings.Contains(got.Reason, "not signed by the current authority") {
		t.Errorf("reason = %q", got.Reason)
	}
}

// The names a seam is reached by can grow between versions. A certificate
// signed before the addition keeps working for every OLD name, so nothing
// fails until something uses the new one — and then it fails as a hostname
// error far from the change that caused it.
func TestDecideSignsWhenTheCertificateNoLongerCoversEveryName(t *testing.T) {
	ca, _ := mint(t)
	short, err := ca.Sign([]string{"kaimahi-proxy.kaimahi"}, epoch)
	if err != nil {
		t.Fatal(err)
	}
	serving, err := ParseCertificate(short.CertPEM)
	if err != nil {
		t.Fatal(err)
	}
	authority, err := ParseCertificate(ca.CertPEM)
	if err != nil {
		t.Fatal(err)
	}
	got := Decide(serving, authority, epoch.Add(time.Hour))
	if !got.Sign {
		t.Fatal("a certificate missing a seam name was kept")
	}
	if !strings.Contains(got.Reason, "kaimahi-mcp-gateway.kaimahi") {
		t.Errorf("the reason does not name a name that is missing: %q", got.Reason)
	}
}

// An expiry must be reported as an expiry, not mistaken for a wrong
// authority — the two call for completely different investigations.
func TestDecideCallsAnExpiredCertificateExpired(t *testing.T) {
	serving, authority := certs(t)
	got := Decide(serving, authority, epoch.Add(servingLifetime+24*time.Hour))
	if !got.Sign {
		t.Fatal("an expired certificate was kept")
	}
	if !strings.Contains(got.Reason, "EXPIRED") {
		t.Errorf("reason = %q", got.Reason)
	}
}

// The case that reached a live cluster before this test existed: an authority
// minted TODAY, and a serving certificate it signed whose life is already
// over. Nothing is wrong with the issuance — the operator simply has not
// deployed in over a year.
//
// Judging "was this signed by the current authority" by building a chain got
// this wrong. A chain has to be verified AT a time, and every time inside the
// old certificate's window is before the authority existed, so the chain fails
// and the answer comes back as "somebody replaced the authority". That sends
// an operator looking for a Secret nobody touched. The signature is checked
// directly instead, which asks the question that was meant and has no clock
// in it.
func TestAnExpiredCertificateUnderAFreshAuthorityIsCalledExpired(t *testing.T) {
	authority, err := MintAuthority(epoch)
	if err != nil {
		t.Fatal(err)
	}
	// Signed 400 days before the authority was minted: its 398-day life ended
	// two days ago, and the authority has existed for none of it.
	old, err := authority.Sign(SeamNames(), epoch.Add(-400*24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	serving, err := ParseCertificate(old.CertPEM)
	if err != nil {
		t.Fatal(err)
	}
	ca, err := ParseCertificate(authority.CertPEM)
	if err != nil {
		t.Fatal(err)
	}
	got := Decide(serving, ca, epoch)
	if !got.Sign {
		t.Fatal("an expired certificate was kept")
	}
	if strings.Contains(got.Reason, "authority") {
		t.Errorf("an expired certificate was blamed on the authority that did sign it: %q", got.Reason)
	}
	if !strings.Contains(got.Reason, "EXPIRED") {
		t.Errorf("reason = %q, want it to say the certificate expired", got.Reason)
	}
}

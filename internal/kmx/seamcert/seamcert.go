// Package seamcert mints the certificate the governance plane serves its two
// data seams with, and reads back what an operator has to know about it.
//
// Why a certificate at all. The model seam carries the full text of what an
// agent was asked and what it answered, and the tool seam carries the full
// body of what a tool returned. Neither is written to any artifact the plane
// keeps: the ledger records identifiers, token counts and cost, and the tool
// audit records a capped summary of DECLARED argument fields — never a
// response. So the content on those two wires exists in no other record, and
// capturing it is the only way to obtain it.
//
// Why this package, and not cert-manager or a mesh. Both are an operational
// control plane, which is the thing this project has repeatedly declined to
// build and defend. What is needed here is much smaller and entirely
// decidable without a cluster: one authority, one serving certificate, and a
// clear answer to "when does this expire". That fits in a package with no
// dependencies and a test suite that runs in milliseconds.
//
// The shape, and the one property that makes rotation cheap: the authority
// OUTLIVES the certificate it signs. Renewal re-signs a serving certificate
// under the unchanged authority, so the CA an agent trusts never has to be
// redistributed and there is no window in which one side has rolled over and
// the other has not. The authority's private key is the price of that: it has
// to persist. It lives in one Secret in the plane's own namespace, it is
// never mounted into any pod — not even the proxy's, which needs only the
// serving key — and nothing but the command that mints and renews ever reads
// it.
package seamcert

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"slices"
	"strings"
	"time"
)

const (
	// authorityCommonName and servingCommonName are what an operator reads
	// back off the wire. They say which plane and which half, because
	// "certificate verify failed" against an unnamed certificate is the
	// error that costs the hours.
	authorityCommonName = "kaimahi-plane-ca"
	servingCommonName   = "kaimahi-plane-seams"

	// The authority outlives the certificates it signs by a wide margin, so
	// that routine renewal never touches the agent side. It is not
	// unbounded: a self-signed CA with no end date is a key nobody is ever
	// forced to think about again.
	authorityLifetime = 10 * 365 * 24 * time.Hour

	// One year and a bit, matching what public issuance settled on. Long
	// enough that renewal is not a chore, short enough that a cluster
	// nobody has touched in years does not keep serving on a key of
	// unknown provenance.
	servingLifetime = 398 * 24 * time.Hour

	// How long before NotAfter the operator starts being told. Wide enough
	// that a monthly glance at `kmx status` cannot miss it, and wide enough
	// that `kmx plane` renews well before anything breaks.
	renewalWindow = 30 * 24 * time.Hour
)

// Namespace and the two Service names the plane's data seams are published
// under. The certificate has to carry every form each Service is addressed
// by, because hostname verification compares the name in the URL — not the
// address it resolved to — and this repository writes both the short
// two-label form and the fully qualified one.
const (
	namespace       = "kaimahi"
	modelSeamName   = "kaimahi-proxy"
	toolSeamName    = "kaimahi-mcp-gateway"
	loopbackDNSName = "localhost"
)

// Authority is a certificate authority and the key that signs under it.
type Authority struct {
	CertPEM []byte
	KeyPEM  []byte

	cert *x509.Certificate
	key  *ecdsa.PrivateKey
}

// Serving is a certificate a seam listener presents, and its key.
type Serving struct {
	CertPEM []byte
	KeyPEM  []byte
}

// SeamNames returns every DNS name the two seams are reached by, in the
// forms this repository actually writes.
//
// All four forms of each Service, not just the fully qualified one: a
// RemoteMCPServer in this repository points at `kaimahi-mcp-gateway.kaimahi`
// while the governed ModelConfig presets point at
// `kaimahi-proxy.kaimahi.svc.cluster.local`. A certificate carrying only one
// of those fails the other with a hostname error that reads like a network
// fault.
func SeamNames() []string {
	var names []string
	for _, service := range []string{modelSeamName, toolSeamName} {
		names = append(names,
			service,
			service+"."+namespace,
			service+"."+namespace+".svc",
			service+"."+namespace+".svc.cluster.local",
		)
	}
	// The plane dials its own seams over loopback — the liveness probe asks
	// each data listener whether it is answering, and the approval notifier
	// posts through its own gateway. Those are clients like any other and
	// they verify like any other; without these names they would be the one
	// place tempted to skip verification.
	return append(names, loopbackDNSName)
}

// MintAuthority creates a fresh authority. The key it returns is the only
// copy; there is no recovery from losing it beyond minting another authority
// and redistributing it.
func MintAuthority(now time.Time) (Authority, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return Authority{}, fmt.Errorf("generating the authority key: %w", err)
	}
	serial, err := serialNumber()
	if err != nil {
		return Authority{}, err
	}
	template := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: authorityCommonName},
		NotBefore:             notBefore(now),
		NotAfter:              now.Add(authorityLifetime),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
		// The authority signs one kind of thing and only ever will.
		MaxPathLen:     0,
		MaxPathLenZero: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		return Authority{}, fmt.Errorf("signing the authority certificate: %w", err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return Authority{}, fmt.Errorf("reading back the authority certificate: %w", err)
	}
	keyPEM, err := encodeKey(key)
	if err != nil {
		return Authority{}, err
	}
	return Authority{
		CertPEM: encodeCert(der),
		KeyPEM:  keyPEM,
		cert:    cert,
		key:     key,
	}, nil
}

// LoadAuthority reads an authority back out of the PEM it was stored as, and
// refuses a pair that does not belong together.
//
// The mismatch check is not politeness. A certificate and an unrelated key
// load individually without complaint, and the failure would surface much
// later as a TLS handshake the client cannot explain — signed by a key whose
// public half is not the one in the certificate it presented.
func LoadAuthority(certPEM, keyPEM []byte) (Authority, error) {
	cert, err := ParseCertificate(certPEM)
	if err != nil {
		return Authority{}, fmt.Errorf("reading the authority certificate: %w", err)
	}
	if !cert.IsCA {
		return Authority{}, errors.New("reading the authority certificate: it is not a certificate authority")
	}
	block, _ := pem.Decode(keyPEM)
	if block == nil {
		return Authority{}, errors.New("reading the authority key: not PEM")
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return Authority{}, fmt.Errorf("reading the authority key: %w", err)
	}
	key, ok := parsed.(*ecdsa.PrivateKey)
	if !ok {
		return Authority{}, fmt.Errorf("reading the authority key: want an ECDSA key, got %T", parsed)
	}
	pub, ok := cert.PublicKey.(*ecdsa.PublicKey)
	if !ok || !pub.Equal(key.Public()) {
		return Authority{}, errors.New("the authority key does not belong to the authority certificate")
	}
	return Authority{CertPEM: certPEM, KeyPEM: keyPEM, cert: cert, key: key}, nil
}

// Sign issues a serving certificate for the given names, valid from now.
func (a Authority) Sign(names []string, now time.Time) (Serving, error) {
	if a.cert == nil || a.key == nil {
		return Serving{}, errors.New("this authority was never loaded or minted")
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return Serving{}, fmt.Errorf("generating the serving key: %w", err)
	}
	serial, err := serialNumber()
	if err != nil {
		return Serving{}, err
	}
	notAfter := now.Add(servingLifetime)
	// Never outlive the authority. A certificate valid past its issuer is
	// accepted by nobody and would present as an inexplicable failure on a
	// date far from any deploy.
	if notAfter.After(a.cert.NotAfter) {
		notAfter = a.cert.NotAfter
	}
	template := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: servingCommonName},
		NotBefore:             notBefore(now),
		NotAfter:              notAfter,
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		DNSNames:              names,
		IPAddresses:           []net.IP{net.IPv4(127, 0, 0, 1), net.IPv6loopback},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, a.cert, &key.PublicKey, a.key)
	if err != nil {
		return Serving{}, fmt.Errorf("signing the serving certificate: %w", err)
	}
	keyPEM, err := encodeKey(key)
	if err != nil {
		return Serving{}, err
	}
	return Serving{CertPEM: encodeCert(der), KeyPEM: keyPEM}, nil
}

// ParseCertificate decodes one PEM certificate.
func ParseCertificate(certPEM []byte) (*x509.Certificate, error) {
	block, _ := pem.Decode(bytes.TrimSpace(certPEM))
	if block == nil || block.Type != "CERTIFICATE" {
		return nil, errors.New("not a PEM certificate")
	}
	return x509.ParseCertificate(block.Bytes)
}

// ExpiryState is the three answers an operator can get, and they are three
// different actions: nothing, renew soon, the seams are already down.
type ExpiryState int

const (
	Valid ExpiryState = iota
	Expiring
	Expired
)

func (s ExpiryState) String() string {
	switch s {
	case Expiring:
		return "expiring"
	case Expired:
		return "expired"
	default:
		return "valid"
	}
}

// Report is what `kmx status` prints and what the renewal decision reads.
type Report struct {
	State    ExpiryState
	Issuer   string
	Subject  string
	NotAfter time.Time
	// In is how long is left. Negative once NotAfter has passed.
	In time.Duration
}

// Expiry classifies a certificate at a point in time.
func Expiry(cert *x509.Certificate, now time.Time) Report {
	r := Report{
		Issuer:   cert.Issuer.CommonName,
		Subject:  cert.Subject.CommonName,
		NotAfter: cert.NotAfter,
		In:       cert.NotAfter.Sub(now),
	}
	switch {
	case !now.Before(cert.NotAfter):
		r.State = Expired
	case r.In <= renewalWindow:
		r.State = Expiring
	default:
		r.State = Valid
	}
	return r
}

// NeedsRenewal is the one question `kmx plane` asks: should this run replace
// the serving certificate rather than keep it?
func (r Report) NeedsRenewal() bool { return r.State != Valid }

// Line names the certificate rather than describing a state. Whoever reads
// this is usually reading it because something would not connect, and the
// first thing they need is which certificate is involved and who issued it.
func (r Report) Line() string {
	days := int(r.In.Hours() / 24)
	switch r.State {
	case Expired:
		return fmt.Sprintf("%s (issued by %s) EXPIRED %d days ago — it expires %s",
			r.Subject, r.Issuer, -days, r.NotAfter.UTC().Format(time.RFC3339))
	case Expiring:
		return fmt.Sprintf("%s (issued by %s) expires in %d days, %s — renew with `kmx plane --step certificate`",
			r.Subject, r.Issuer, days, r.NotAfter.UTC().Format(time.RFC3339))
	default:
		return fmt.Sprintf("%s (issued by %s) expires in %d days, %s",
			r.Subject, r.Issuer, days, r.NotAfter.UTC().Format(time.RFC3339))
	}
}

// Renewal is the answer to the only question a deploy has to ask about an
// existing serving certificate: keep it, or sign another? Reason is carried
// because this is printed — an operator watching a deploy replace a
// certificate is owed the reason, and one watching it KEEP a certificate is
// owed that too.
type Renewal struct {
	Sign   bool
	Reason string
}

// Decide compares the serving certificate on the cluster against the
// authority and the names the seams are reached by. A nil serving
// certificate means there is none yet.
//
// Four things force a re-sign, and the last two are the ones that would
// otherwise rot silently: an authority that no longer matches (someone
// replaced it), and a set of names that has grown since the certificate was
// signed. A cluster upgraded to a version that reaches a seam by a new name
// would otherwise keep serving a certificate that cannot answer to it, and
// the failure would arrive as a hostname error nobody connected to a deploy.
func Decide(serving, authority *x509.Certificate, now time.Time) Renewal {
	if serving == nil {
		return Renewal{Sign: true, Reason: "no serving certificate yet"}
	}
	if err := serving.CheckSignatureFrom(authority); err != nil {
		return Renewal{Sign: true, Reason: "the serving certificate was not signed by the current authority"}
	}
	if missing := missingNames(serving); len(missing) > 0 {
		return Renewal{Sign: true, Reason: fmt.Sprintf(
			"the serving certificate does not cover %s", strings.Join(missing, ", "))}
	}
	if report := Expiry(serving, now); report.NeedsRenewal() {
		return Renewal{Sign: true, Reason: report.Line()}
	}
	return Renewal{Reason: Expiry(serving, now).Line()}
}

func missingNames(serving *x509.Certificate) []string {
	var missing []string
	for _, name := range SeamNames() {
		if !slices.Contains(serving.DNSNames, name) {
			missing = append(missing, name)
		}
	}
	return missing
}

// notBefore backdates slightly. Nothing here guarantees that the machine
// running kmx and the node running the proxy agree on the time, and a
// certificate that is not yet valid fails exactly like one that is wrong.
func notBefore(now time.Time) time.Time { return now.Add(-1 * time.Hour) }

func serialNumber() (*big.Int, error) {
	n, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, fmt.Errorf("generating a serial number: %w", err)
	}
	return n, nil
}

func encodeCert(der []byte) []byte {
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

func encodeKey(key *ecdsa.PrivateKey) ([]byte, error) {
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return nil, fmt.Errorf("encoding a private key: %w", err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}), nil
}

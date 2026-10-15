package agentsessions

import (
	"context"
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	v1 "github.com/aramase/agentsessions/api/genpb"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/secretshapes"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/test/bufconn"
)

func TestDialRejectsCredentialShapedHostnames(t *testing.T) {
	var bare, wrapped int
	for _, shape := range secretshapes.All() {
		if !validHost(shape.Example) {
			continue
		}
		for _, tc := range []struct{ name, host string }{
			{"bare", shape.Example},
			{"wrapped", "sessions." + shape.Example + ".invalid"},
		} {
			if secretshapes.Match(tc.host) == nil {
				continue
			}
			if !validHost(tc.host) {
				t.Fatal("credential fixture is not a valid DNS hostname")
			}
			if tc.name == "bare" {
				bare++
			} else {
				wrapped++
			}
			t.Run(shape.Name+"/"+tc.name, func(t *testing.T) {
				c, err := Dial(Options{Address: net.JoinHostPort(tc.host, "443")})
				if c != nil {
					_ = c.Close()
					t.Error("credential-shaped hostname produced a client")
				}
				assertSafeError(t, err, codes.InvalidArgument)
				if strings.Contains(err.Error(), shape.Example) || secretshapes.Match(err.Error()) != nil {
					t.Fatal("credential-shaped hostname escaped in diagnostic")
				}
			})
		}
	}
	if bare == 0 || wrapped == 0 {
		t.Fatal("no DNS-compatible bare/wrapped credential fixtures exercised")
	}
}

func TestDialCAOnLoopbackNeverFallsBackToPlaintext(t *testing.T) {
	_, ca := certificate(t, "", net.ParseIP("127.0.0.1"))
	s := validServer(t)
	c, err := Dial(Options{Address: serve(t, s, nil), CAFile: writeCA(t, ca)})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_, err = c.RunCase(ctx, caseRequest())
	if err == nil {
		t.Fatal("supplied CA allowed plaintext")
	}
	if creates, _ := s.counts(); creates != 0 {
		t.Fatal("mutation reached plaintext host with supplied CA")
	}
}

func TestDialRejectsWrongTrustRootAndExpiredCertificate(t *testing.T) {
	for _, expired := range []bool{false, true} {
		t.Run(map[bool]string{false: "wrong CA", true: "expired leaf"}[expired], func(t *testing.T) {
			cert, ca := certificate(t, "", net.ParseIP("127.0.0.1"))
			if expired {
				leaf, err := x509.ParseCertificate(cert.Certificate[0])
				if err != nil {
					t.Fatal(err)
				}
				root, err := x509.ParseCertificate(cert.Certificate[1])
				if err != nil {
					t.Fatal(err)
				}
				leaf.NotAfter = time.Now().Add(-time.Minute)
				key := cert.PrivateKey.(*ecdsa.PrivateKey)
				cert.Certificate[0], err = x509.CreateCertificate(rand.Reader, leaf, root, key.Public(), key)
				if err != nil {
					t.Fatal(err)
				}
			} else {
				_, ca = certificate(t, "", net.ParseIP("127.0.0.1"))
			}
			s := validServer(t)
			c, err := Dial(Options{Address: serve(t, s, &cert), CAFile: writeCA(t, ca)})
			if err != nil {
				t.Fatal(err)
			}
			defer c.Close()
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			_, err = c.RunCase(ctx, caseRequest())
			if err == nil {
				t.Fatal("invalid serving certificate accepted")
			}
			if creates, _ := s.counts(); creates != 0 {
				t.Fatal("mutation reached unverified host")
			}
		})
	}
}

// Transport credentials depend on the address literal and CAFile; a CA file
// also forces TLS on loopback, which a non-loopback literal requires anyway.
// The injected dialer replaces only the socket with an in-memory connection.
// 192.0.2.10 is an RFC 5737 documentation address: the dialer rejects any
// other target, and a zero dial count fails the test if it is bypassed.
func TestDialNonLoopbackLiteralRequiresVerifiedTLS(t *testing.T) {
	ip := net.ParseIP("192.0.2.10")
	address := net.JoinHostPort(ip.String(), "443")
	for _, secure := range []bool{false, true} {
		t.Run(map[bool]string{false: "plaintext refused", true: "CA verified"}[secure], func(t *testing.T) {
			listener := bufconn.Listen(1 << 20)
			var opts []grpc.ServerOption
			caFile := ""
			if secure {
				cert, ca := certificate(t, "", ip)
				opts = append(opts, grpc.Creds(credentials.NewTLS(&tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12})))
				caFile = writeCA(t, ca)
			}
			s := validServer(t)
			server := grpc.NewServer(opts...)
			v1.RegisterSessionsServer(server, s)
			done := make(chan struct{})
			go func() { defer close(done); _ = server.Serve(listener) }()
			t.Cleanup(func() { server.Stop(); <-done })
			var dials atomic.Int32
			c, err := dial(Options{Address: address, CAFile: caFile}, func(ctx context.Context, target string) (net.Conn, error) {
				dials.Add(1)
				if target != address {
					return nil, fmt.Errorf("unexpected dial target %q", target)
				}
				return listener.DialContext(ctx)
			})
			if err != nil {
				t.Fatal(err)
			}
			defer c.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			_, err = c.RunCase(ctx, caseRequest())
			if dials.Load() == 0 {
				t.Fatal("case did not dial the non-loopback literal")
			}
			if secure && err != nil {
				t.Fatal(err)
			}
			if !secure {
				if err == nil {
					t.Fatal("non-loopback literal used plaintext")
				}
				if creates, _ := s.counts(); creates != 0 {
					t.Fatal("mutation reached plaintext non-loopback host")
				}
			}
		})
	}
}

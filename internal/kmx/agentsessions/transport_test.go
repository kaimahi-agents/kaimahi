package agentsessions

import (
	"context"
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"net"
	"strings"
	"testing"
	"time"

	v1 "github.com/aramase/agentsessions/api/genpb"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/secretshapes"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
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

// A self-addressed non-loopback listener exercises literal-IP transport policy
// without contacting an external endpoint. Loopback-only machines skip this case.
func TestDialNonLoopbackLiteralRequiresVerifiedTLS(t *testing.T) {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		t.Fatal(err)
	}
	var ip net.IP
	for _, address := range addrs {
		network, ok := address.(*net.IPNet)
		if ok && network.IP.To4() != nil && network.IP.IsGlobalUnicast() && !network.IP.IsLoopback() {
			ip = network.IP
			break
		}
	}
	if ip == nil {
		t.Skip("no local non-loopback IPv4 interface")
	}
	for _, secure := range []bool{false, true} {
		t.Run(map[bool]string{false: "plaintext refused", true: "CA verified"}[secure], func(t *testing.T) {
			listener, err := net.Listen("tcp", net.JoinHostPort(ip.String(), "0"))
			if err != nil {
				t.Fatal("cannot listen on local test interface")
			}
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
			c, err := Dial(Options{Address: listener.Addr().String(), CAFile: caFile})
			if err != nil {
				t.Fatal(err)
			}
			defer c.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			_, err = c.RunCase(ctx, caseRequest())
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

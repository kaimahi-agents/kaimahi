package agentsessions

import (
	"crypto/tls"
	"crypto/x509"
	"net"
	"net/netip"
	"os"
	"strconv"
	"strings"

	sdk "github.com/aramase/agentsessions/client"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/secretshapes"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
)

// Dial accepts only host:port, never a URL or a gRPC resolver target. Connection
// establishment is lazy; RunCase verifies TLS before sending either mutation.
// Only literal loopback without a CA file permits plaintext.
func Dial(options Options) (*Client, error) {
	host, _, err := addressParts(options.Address)
	if err != nil {
		return nil, err
	}
	ip, _ := netip.ParseAddr(host)
	var transport credentials.TransportCredentials = insecure.NewCredentials()
	if !ip.Unmap().IsLoopback() || options.CAFile != "" {
		roots, err := x509.SystemCertPool()
		if err != nil {
			return nil, status.Error(codes.Internal, "agentsessions: system certificate roots unavailable")
		}
		if options.CAFile != "" {
			pem, err := os.ReadFile(options.CAFile)
			if err != nil {
				return nil, status.Error(codes.InvalidArgument, "agentsessions: cannot read sessions CA")
			}
			if !roots.AppendCertsFromPEM(pem) {
				return nil, status.Error(codes.InvalidArgument, "agentsessions: invalid sessions CA PEM")
			}
		}
		transport = credentials.NewTLS(&tls.Config{RootCAs: roots, ServerName: host, MinVersion: tls.VersionTLS12})
	}
	client, err := sdk.Dial("passthrough:///"+options.Address, sdk.WithDialOptions(
		grpc.WithTransportCredentials(transport),
		grpc.WithDisableRetry(),
		grpc.WithDisableServiceConfig(),
	))
	if err != nil {
		return nil, safeRPCError(err, "agentsessions: transport initialization failed")
	}
	return &Client{sdk: client}, nil
}

// NormalizeAddress compares operator and receipt destinations syntactically, without
// DNS lookup or treating aliases (including localhost) as the same destination.
func NormalizeAddress(address string) (string, error) {
	host, port, err := addressParts(address)
	if err != nil {
		return "", err
	}
	if ip, err := netip.ParseAddr(host); err == nil {
		host = ip.Unmap().String()
	} else {
		host = strings.ToLower(strings.TrimSuffix(host, "."))
	}
	return net.JoinHostPort(host, strconv.Itoa(port)), nil
}

func addressParts(address string) (string, int, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil || !validHost(host) || strings.Contains(address, "[") && !strings.Contains(host, ":") {
		return "", 0, status.Error(codes.InvalidArgument, "agentsessions: address must be host:port")
	}
	if secretshapes.Match(host) != nil {
		return "", 0, status.Error(codes.InvalidArgument, "agentsessions: credential-shaped host refused")
	}
	for _, c := range port {
		if c < '0' || c > '9' {
			return "", 0, status.Error(codes.InvalidArgument, "agentsessions: invalid address port")
		}
	}
	n, err := strconv.Atoi(port)
	if err != nil || n < 1 || n > 65535 {
		return "", 0, status.Error(codes.InvalidArgument, "agentsessions: invalid address port")
	}
	return host, n, nil
}

func validHost(host string) bool {
	if ip, err := netip.ParseAddr(host); err == nil {
		return ip.Zone() == ""
	}
	if len(host) == 0 || len(host) > 253 {
		return false
	}
	for _, label := range strings.Split(strings.TrimSuffix(host, "."), ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, c := range label {
			if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' {
				continue
			}
			return false
		}
	}
	return true
}

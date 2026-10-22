package runtime

import (
	"errors"
	"net"
	"net/url"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/secretshapes"
)

var (
	// ErrOpenAIEndpointInvalid reports a base that is not an absolute HTTP(S)
	// URL without credentials, query or fragment.
	ErrOpenAIEndpointInvalid = errors.New("endpoint must be an absolute HTTP(S) base without credentials, query, or fragment")
	// ErrOpenAIEndpointInsecure reports plaintext HTTP outside literal loopback.
	ErrOpenAIEndpointInsecure = errors.New("endpoint requires HTTPS except keyless HTTP at literal 127.0.0.1 or ::1")
)

// CanonicalOpenAIBaseURL returns the canonical OpenAI-compatible base used by
// both agent and judge requests: lowercase scheme/host, no default port, no
// trailing slash and a /v1 suffix. It performs no DNS lookup or network IO.
// Errors are ErrOpenAIEndpointInvalid or ErrOpenAIEndpointInsecure and never
// quote the input.
func CanonicalOpenAIBaseURL(raw string) (string, error) {
	if len(raw) > 4096 || !utf8.ValidString(raw) || secretshapes.Match(raw) != nil || strings.ContainsAny(raw, "?#") {
		return "", ErrOpenAIEndpointInvalid
	}
	u, err := url.Parse(raw)
	if err != nil || u.Opaque != "" || u.User != nil || u.Host == "" || u.Hostname() == "" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
		return "", ErrOpenAIEndpointInvalid
	}
	u.Scheme = strings.ToLower(u.Scheme)
	host := strings.ToLower(u.Hostname())
	if u.Scheme != "https" && !(u.Scheme == "http" && (host == "127.0.0.1" || host == "::1")) {
		return "", ErrOpenAIEndpointInsecure
	}
	if strings.HasSuffix(u.Host, ":") {
		return "", ErrOpenAIEndpointInvalid
	}
	port := u.Port()
	if port != "" {
		number, err := strconv.Atoi(port)
		if err != nil || number < 1 || number > 65535 {
			return "", ErrOpenAIEndpointInvalid
		}
	}
	if (u.Scheme == "https" && port == "443") || (u.Scheme == "http" && port == "80") {
		port = ""
	}
	u.Host = host
	if strings.Contains(host, ":") {
		u.Host = "[" + host + "]"
	}
	if port != "" {
		u.Host = net.JoinHostPort(host, port)
	}
	u.Path = strings.TrimRight(u.Path, "/")
	u.RawPath = strings.TrimRight(u.RawPath, "/")
	if !strings.HasSuffix(u.Path, "/v1") {
		u.Path += "/v1"
		if u.RawPath != "" {
			u.RawPath += "/v1"
		}
	}
	return u.String(), nil
}

package adapter

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
)

type secretRef struct {
	Name string `json:"name"`
	Key  string `json:"key"`
}

type route struct {
	URL     string               `json:"url"`
	Name    string               `json:"name"`
	Headers map[string]secretRef `json:"headers,omitempty"`
}

type config struct {
	Tools map[string]route `json:"tools"`
}

var (
	routeIDPattern    = regexp.MustCompile(`^shim-[a-f0-9]+$`)
	secretNamePattern = regexp.MustCompile(`^[a-z0-9]([a-z0-9.-]*[a-z0-9])?$`)
	secretKeyPattern  = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)
	headerPattern     = regexp.MustCompile("^[!#$%&'*+.^_`|~0-9A-Za-z-]+$")
	invalidJSON       = errors.New("invalid JSON object")
)

// New loads the immutable routing table. Secret files are read on each call,
// not at startup. The command supplies /config/routes.json and /secrets.
func New(configPath, secretsDir string) (*Adapter, error) {
	data, err := os.ReadFile(configPath)
	if err != nil {
		return nil, errors.New("route configuration unavailable")
	}
	if err := validateObject(data); err != nil {
		return nil, errors.New("invalid route configuration")
	}
	var cfg config
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err := d.Decode(&cfg); err != nil || cfg.Tools == nil {
		return nil, errors.New("invalid route configuration")
	}
	for id, r := range cfg.Tools {
		u, err := url.Parse(r.URL)
		if !routeIDPattern.MatchString(id) || strings.TrimSpace(r.Name) == "" || err != nil ||
			(u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil ||
			u.RawQuery != "" || u.ForceQuery || strings.Contains(r.URL, "#") {
			return nil, errors.New("invalid route destination or name")
		}
		seen := make(map[string]bool)
		for header, ref := range r.Headers {
			canonical := http.CanonicalHeaderKey(header)
			if !headerPattern.MatchString(header) || reservedHeader(canonical) || seen[canonical] ||
				!secretNamePattern.MatchString(ref.Name) || !secretKeyPattern.MatchString(ref.Key) || ref.Key == "." || strings.HasPrefix(ref.Key, "..") {
				return nil, errors.New("invalid route secret reference")
			}
			seen[canonical] = true
		}
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil              // Routes are cluster-local; do not leak credentials to ambient proxies.
	transport.DisableKeepAlives = true // No automatic replay on a reused connection.
	transport.ForceAttemptHTTP2 = false
	transport.MaxResponseHeaderBytes = 32 << 10
	return &Adapter{routes: cfg.Tools, secretsDir: secretsDir, transport: transport}, nil
}

func reservedHeader(name string) bool {
	if strings.HasPrefix(name, "Mcp-") {
		return true
	}
	switch name {
	case "Host", "Connection", "Content-Length", "Content-Type", "Accept", "Accept-Encoding", "Transfer-Encoding", "Trailer", "Te", "Upgrade", "Keep-Alive", "Proxy-Authorization", "Proxy-Connection", "Idempotency-Key", "X-Idempotency-Key":
		return true
	}
	return false
}

// validateObject preserves argument numbers and raw JSON while rejecting
// duplicate keys at every depth (including escaped-equivalent keys).
func validateObject(data []byte) error {
	if !json.Valid(data) {
		return invalidJSON
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.UseNumber()
	token, err := d.Token()
	if err != nil || token != json.Delim('{') {
		return invalidJSON
	}
	return consumeContainer(d, '{')
}

func consumeContainer(d *json.Decoder, kind json.Delim) error {
	seen := make(map[string]bool)
	for d.More() {
		if kind == '{' {
			key, err := d.Token()
			if err != nil {
				return invalidJSON
			}
			name := key.(string) // json.Valid already checked object syntax.
			if seen[name] {
				return invalidJSON
			}
			seen[name] = true
		}
		value, err := d.Token()
		if err != nil {
			return invalidJSON
		}
		if delim, ok := value.(json.Delim); ok {
			if err := consumeContainer(d, delim); err != nil {
				return err
			}
		}
	}
	_, err := d.Token()
	return err
}

func (a *Adapter) headers(r route) (http.Header, error) {
	headers := make(http.Header)
	if len(r.Headers) == 0 {
		return headers, nil
	}
	root, err := os.OpenRoot(a.secretsDir)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	for name, ref := range r.Headers {
		// Root permits Kubernetes projected-volume symlinks within /secrets,
		// but cannot follow a symlink outside it.
		file, err := root.Open(ref.Name + "/" + ref.Key)
		if err != nil {
			return nil, err
		}
		value, err := io.ReadAll(io.LimitReader(file, (64<<10)+1))
		file.Close()
		if err != nil || len(value) == 0 || len(value) > 64<<10 || strings.TrimSpace(string(value)) != string(value) {
			return nil, errors.New("invalid secret file")
		}
		for _, b := range value {
			if b < 32 || b == 127 {
				return nil, errors.New("invalid secret header value")
			}
		}
		headers.Set(name, string(value)) // Deliberately no Bearer prefix or trimming.
	}
	return headers, nil
}

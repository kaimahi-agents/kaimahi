package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var hostProxyAddress = regexp.MustCompile(`^http://127\.0\.0\.1:([0-9]+)/?$`)
var hostProxyResult = regexp.MustCompile(`^/(result|claim)/[0-9a-f]{32}$`)

// ProxyHostRequest forwards one already-signed request to the trusted child broker.
// It preserves signed bytes and holds no credentials or signing authority.
func ProxyHostRequest(ctx context.Context, address string, in io.Reader, out io.Writer) error {
	match := hostProxyAddress.FindStringSubmatch(address)
	if match == nil || in == nil || out == nil {
		return errHostBridge
	}
	port, err := strconv.Atoi(match[1])
	if err != nil || port < 1 || port > 65535 {
		return errHostBridge
	}
	raw, err := io.ReadAll(io.LimitReader(in, 96*1024+1))
	if err != nil || len(raw) > 96*1024 {
		return errHostBridge
	}
	var envelope struct {
		Method    string `json:"method"`
		Path      string `json:"path"`
		Time      string `json:"time"`
		Nonce     string `json:"nonce"`
		Signature string `json:"signature"`
		Body      []byte `json:"body"`
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&envelope) != nil || decoder.Decode(new(any)) != io.EOF || len(envelope.Body) > 64*1024 {
		return errHostBridge
	}
	if !(envelope.Method == http.MethodGet && envelope.Path == "/next" && len(envelope.Body) == 0 ||
		envelope.Method == http.MethodPost && hostProxyResult.MatchString(envelope.Path)) {
		return errHostBridge
	}
	request, err := http.NewRequestWithContext(ctx, envelope.Method, strings.TrimSuffix(address, "/")+envelope.Path, bytes.NewReader(envelope.Body))
	if err != nil {
		return errHostBridge
	}
	request.Header.Set("X-Kmx-Time", envelope.Time)
	request.Header.Set("X-Kmx-Nonce", envelope.Nonce)
	request.Header.Set("X-Kmx-Signature", envelope.Signature)
	request.Header.Set("Content-Type", "application/json")
	// No ambient proxy, automatic decompression, connection reuse or redirects:
	// this is one opaque HTTP exchange, never a retry or alternate route.
	transport := &http.Transport{Proxy: nil, DisableCompression: true, DisableKeepAlives: true, MaxResponseHeaderBytes: 4096}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return errHostBridge }}
	response, err := client.Do(request)
	if err != nil {
		return errHostBridge
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 128*1024+1))
	if err != nil || len(body) > 128*1024 {
		return errHostBridge
	}
	result := struct {
		Status    int    `json:"status"`
		Signature string `json:"signature"`
		Body      []byte `json:"body"`
	}{response.StatusCode, response.Header.Get("X-Kmx-Signature"), body}
	if json.NewEncoder(out).Encode(result) != nil {
		return errHostBridge
	}
	return nil
}

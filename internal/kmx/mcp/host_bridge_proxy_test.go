package mcp

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func proxyEnvelope(method, path string, body []byte) []byte {
	raw, _ := json.Marshal(map[string]any{"method": method, "path": path, "time": "123", "nonce": "nonce", "signature": "request-signature", "body": body})
	return raw
}

func TestProxyHostRequestRoundTrip(t *testing.T) {
	for _, operation := range []string{"next", "result", "claim"} {
		t.Run(operation, func(t *testing.T) {
			method, path, body := "GET", "/next", []byte{}
			if operation != "next" {
				method, path = "POST", "/"+operation+"/"+strings.Repeat("a", 32)
			}
			if operation == "result" {
				body = []byte(" {\r\n \"answer\":\"exact\" }\n")
			}
			response := []byte{0, 255, '\r', '\n', ' '}
			key := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{7}, ed25519.SeedSize))
			public := key.Public().(ed25519.PublicKey)
			requestSignature := base64.StdEncoding.EncodeToString(ed25519.Sign(key, []byte(method+"\n"+path+"\n123\nnonce\n"+string(body))))
			responseSignature := base64.StdEncoding.EncodeToString(ed25519.Sign(key, []byte("409\nnonce\n"+string(response))))
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				got, _ := io.ReadAll(r.Body)
				if r.Method != method || r.RequestURI != path || !bytes.Equal(got, body) {
					t.Errorf("changed signed request: %s %s %q", r.Method, r.RequestURI, got)
				}
				for name, want := range map[string]string{"X-Kmx-Time": "123", "X-Kmx-Nonce": "nonce", "X-Kmx-Signature": requestSignature, "Content-Type": "application/json"} {
					if r.Header.Get(name) != want {
						t.Errorf("%s=%q", name, r.Header.Get(name))
					}
				}
				signature, _ := base64.StdEncoding.DecodeString(r.Header.Get("X-Kmx-Signature"))
				canonical := r.Method + "\n" + r.URL.Path + "\n" + r.Header.Get("X-Kmx-Time") + "\n" + r.Header.Get("X-Kmx-Nonce") + "\n" + string(got)
				if !ed25519.Verify(public, []byte(canonical), signature) {
					t.Error("request signature no longer verifies")
				}
				w.Header().Set("X-Kmx-Signature", responseSignature)
				w.Header().Set("Content-Encoding", "gzip") // Opaque bytes must not be decompressed.
				w.WriteHeader(409)
				_, _ = w.Write(response)
			}))
			defer server.Close()
			var out bytes.Buffer
			envelope := bytes.ReplaceAll(proxyEnvelope(method, path, body), []byte("request-signature"), []byte(requestSignature))
			if err := ProxyHostRequest(t.Context(), server.URL+"/", bytes.NewReader(envelope), &out); err != nil {
				t.Fatal(err)
			}
			var got struct {
				Status    int
				Signature string
				Body      []byte
			}
			if err := json.Unmarshal(out.Bytes(), &got); err != nil {
				t.Fatal(err)
			}
			if got.Status != 409 || got.Signature != responseSignature || !bytes.Equal(got.Body, response) || calls.Load() != 1 {
				t.Fatalf("reply=%+v calls=%d", got, calls.Load())
			}
			signature, _ := base64.StdEncoding.DecodeString(got.Signature)
			if !ed25519.Verify(public, []byte("409\nnonce\n"+string(got.Body)), signature) {
				t.Fatal("response signature no longer verifies")
			}
		})
	}
}

func TestProxyHostRequestRefusesBeforeHTTP(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1) }))
	defer server.Close()
	valid := proxyEnvelope("GET", "/next", nil)
	tests := []struct {
		name, address string
		input         []byte
	}{}
	for _, address := range []string{"", "http://localhost:123", "https://127.0.0.1:123", "http://127.0.0.2:123", "http://[::1]:123", "http://10.0.0.1:123", "http://127.0.0.1:0", "http://127.0.0.1:65536", "http://127.0.0.1:+12", "http://127.0.0.1:abc", server.URL + "/path", server.URL + "?", server.URL + "#", strings.Replace(server.URL, "http://", "http://user@", 1)} {
		tests = append(tests, struct {
			name, address string
			input         []byte
		}{address, address, valid})
	}
	inputs := map[string][]byte{
		"bad-json": []byte("{"), "null": []byte("null"), "array": []byte("[]"),
		"extra":             append(append([]byte{}, valid...), []byte("{}")...),
		"unknown":           []byte(`{"method":"GET","path":"/next","extra":true}`),
		"typed-header":      []byte(`{"method":"GET","path":"/next","time":123}`),
		"bad-base64":        []byte(`{"method":"POST","path":"/result/` + strings.Repeat("a", 32) + `","body":"!"}`),
		"oversize-envelope": bytes.Repeat([]byte(" "), 96*1024+1),
		"oversize-body":     proxyEnvelope("POST", "/result/"+strings.Repeat("a", 32), bytes.Repeat([]byte("a"), 64*1024+1)),
		"get-body":          proxyEnvelope("GET", "/next", []byte("x")),
	}
	for _, path := range []string{"/", "/next?x", "/next#x", "/next/", "/result/" + strings.Repeat("A", 32), "/result/" + strings.Repeat("a", 31), "/result/" + strings.Repeat("a", 33), "/result/../next", "/%6eext", "//next"} {
		method := "GET"
		if strings.HasPrefix(path, "/result") {
			method = "POST"
		}
		inputs[path] = proxyEnvelope(method, path, nil)
	}
	for _, suffix := range []string{strings.Repeat("A", 32), strings.Repeat("a", 31), strings.Repeat("a", 33), strings.Repeat("a", 32) + "?x", "../next"} {
		inputs["claim-"+suffix] = proxyEnvelope("POST", "/claim/"+suffix, nil)
	}
	inputs["get-claim"] = proxyEnvelope("GET", "/claim/"+strings.Repeat("a", 32), nil)
	inputs["get-result"] = proxyEnvelope("GET", "/result/"+strings.Repeat("a", 32), nil)
	inputs["post-next"] = proxyEnvelope("POST", "/next", nil)
	inputs["put"] = proxyEnvelope("PUT", "/result/"+strings.Repeat("a", 32), nil)
	for _, name := range []string{"time", "nonce", "signature"} {
		var envelope map[string]any
		_ = json.Unmarshal(valid, &envelope)
		envelope[name] = "value\r\nInjected: yes"
		inputs[name+"-injection"], _ = json.Marshal(envelope)
	}
	for name, input := range inputs {
		tests = append(tests, struct {
			name, address string
			input         []byte
		}{name, server.URL, input})
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var out bytes.Buffer
			err := ProxyHostRequest(t.Context(), tc.address, bytes.NewReader(tc.input), &out)
			if err != errHostBridge || out.Len() != 0 {
				t.Fatalf("err=%v output=%q", err, out.String())
			}
		})
	}
	if calls.Load() != 0 {
		t.Fatalf("invalid requests reached HTTP: %d", calls.Load())
	}
}

func TestProxyHostRequestResponseBoundsAndRedirect(t *testing.T) {
	for _, mode := range []string{"redirect", "headers", "body", "body-limit"} {
		t.Run(mode, func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				switch mode {
				case "redirect":
					w.Header().Set("Location", "/next")
					w.WriteHeader(302)
				case "headers":
					w.Header().Set("X-Large", strings.Repeat("x", 8192))
				case "body":
					_, _ = io.WriteString(w, strings.Repeat("x", 128*1024+1))
				case "body-limit":
					_, _ = io.WriteString(w, strings.Repeat("x", 128*1024))
				}
			}))
			defer server.Close()
			var out bytes.Buffer
			err := ProxyHostRequest(t.Context(), server.URL, bytes.NewReader(proxyEnvelope("GET", "/next", nil)), &out)
			if mode == "body-limit" {
				if err != nil {
					t.Fatal(err)
				}
				var got struct{ Body []byte }
				if json.Unmarshal(out.Bytes(), &got) != nil || len(got.Body) != 128*1024 {
					t.Fatal("body boundary changed")
				}
			} else if err != errHostBridge || out.Len() != 0 {
				t.Fatalf("err=%v output size=%d", err, out.Len())
			}
			if calls.Load() != 1 {
				t.Fatalf("retried request: %d", calls.Load())
			}
		})
	}
}

func TestProxyHostRequestCancellation(t *testing.T) {
	entered, closed := make(chan struct{}), make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(entered)
		<-r.Context().Done()
		close(closed)
	}))
	defer server.Close()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- ProxyHostRequest(ctx, server.URL, bytes.NewReader(proxyEnvelope("GET", "/next", nil)), io.Discard)
	}()
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("request not started")
	}
	cancel()
	select {
	case err := <-done:
		if err != errHostBridge {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("cancel did not return")
	}
	select {
	case <-closed:
	case <-time.After(2 * time.Second):
		t.Fatal("request not closed")
	}
}

func TestProxyHostRequestBodyLimit(t *testing.T) {
	body := bytes.Repeat([]byte("x"), 64*1024)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, _ := io.ReadAll(r.Body)
		if !bytes.Equal(got, body) {
			t.Error("body boundary changed")
		}
	}))
	defer server.Close()
	envelope := proxyEnvelope("POST", "/result/"+strings.Repeat("a", 32), body)
	envelope = append(envelope, bytes.Repeat([]byte(" "), 96*1024-len(envelope))...)
	if err := ProxyHostRequest(t.Context(), server.URL, bytes.NewReader(envelope), io.Discard); err != nil {
		t.Fatal(err)
	}
}

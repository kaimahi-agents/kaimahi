package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMCPHostBridgeRequestConfigFree(t *testing.T) {
	body := []byte(" { \"signed\": true }\n")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, _ := io.ReadAll(r.Body)
		if r.Method != "POST" || r.URL.Path != "/result/"+strings.Repeat("a", 32) || !bytes.Equal(got, body) || r.Header.Get("X-Kmx-Time") != "123" || r.Header.Get("X-Kmx-Nonce") != "nonce" || r.Header.Get("X-Kmx-Signature") != "sig" {
			t.Error("request changed")
		}
		w.Header().Set("X-Kmx-Signature", "reply")
		w.WriteHeader(202)
		_, _ = w.Write(body)
	}))
	defer server.Close()
	var out, diag bytes.Buffer
	deps, loads := testDependencies(&out, &diag)
	read, write, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer read.Close()
	defer write.Close()
	raw := `{"method":"POST","path":"/result/` + strings.Repeat("a", 32) + `","time":"123","nonce":"nonce","signature":"sig","body":"` + base64.StdEncoding.EncodeToString(body) + `"}`
	if _, err := io.WriteString(write, raw); err != nil {
		t.Fatal(err)
	}
	_ = write.Close()
	deps.stdin = read
	home := filepath.Join(t.TempDir(), "must-not-exist")
	t.Setenv("KMX_HOME", home)
	if err := execute([]string{"mcp", "--host-bridge-request", server.URL, "--root", filepath.Join(home, "missing")}, deps); err != nil {
		t.Fatal(err)
	}
	var response struct {
		Status    int
		Signature string
		Body      []byte
	}
	if json.Unmarshal(out.Bytes(), &response) != nil || response.Status != 202 || response.Signature != "reply" || !bytes.Equal(response.Body, body) || *loads != 0 {
		t.Fatalf("response=%+v loads=%d output=%q", response, *loads, out.String())
	}
	if _, err := os.Stat(home); !os.IsNotExist(err) {
		t.Fatalf("created state: %v", err)
	}
}

func TestMCPHostBridgeRequestRefusalsAndHiddenFlag(t *testing.T) {
	for _, args := range [][]string{
		{"mcp", "--host-bridge-request", ""},
		{"mcp", "--host-bridge-request", "http://untrusted:1"},
		{"mcp", "--host-bridge-request", "http://127.0.0.1:1", "--host-bridge-public-key", ""},
		{"mcp", "--host-bridge-request", "http://127.0.0.1:1", "--unknown-proxy-flag"},
	} {
		var out, diag bytes.Buffer
		deps, loads := testDependencies(&out, &diag)
		err := execute(args, deps)
		if err == nil || *loads != 0 || out.Len() != 0 {
			t.Fatalf("args=%v err=%v loads=%d output=%q", args, err, *loads, out.String())
		}
		if !strings.Contains(args[len(args)-1], "unknown") && err.Error() != "host tools unavailable" {
			t.Fatalf("unsafe error: %v", err)
		}
	}
	var out, diag bytes.Buffer
	deps, loads := testDependencies(&out, &diag)
	if err := execute([]string{"mcp", "--help"}, deps); err != nil || *loads != 0 || strings.Contains(out.String(), "host-bridge-request") {
		t.Fatalf("help err=%v loads=%d", err, *loads)
	}
}

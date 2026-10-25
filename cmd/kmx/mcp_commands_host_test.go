package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"os"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	kmxmcp "github.com/kaimahi-agents/kaimahi/internal/kmx/mcp"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestMCPCommandHostRuntimeAndBridgeLifetime(t *testing.T) {
	for _, bound := range []bool{false, true} {
		name := "unbound"
		if bound {
			name = "bound"
		}
		t.Run(name, func(t *testing.T) {
			sr, cw, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			defer sr.Close()
			defer cw.Close()
			cr, sw, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			defer cr.Close()
			defer sw.Close()
			var ignored, diag bytes.Buffer
			deps, loads := testDependencies(&ignored, &diag)
			deps.stdin, deps.stdout = sr, sw
			operationRoot := t.TempDir()
			args := []string{"mcp", "--root", operationRoot, "--provider", "ollama", "--endpoint", "http://localhost:11434"}
			if bound {
				public, _, err := ed25519.GenerateKey(rand.Reader)
				if err != nil {
					t.Fatal(err)
				}
				der, _ := x509.MarshalPKIXPublicKey(public)
				args = append(args, "--host-bridge-public-key", base64.StdEncoding.EncodeToString(der))
			}
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			command := newRootCommand(&commandState{deps: deps})
			command.SetArgs(args)
			done := make(chan error, 1)
			go func() { done <- command.ExecuteContext(ctx) }()
			client := sdk.NewClient(&sdk.Implementation{Name: "runtime-test", Version: "1"}, nil)
			cs, err := client.Connect(ctx, &sdk.IOTransport{Reader: cr, Writer: cw}, nil)
			if err != nil {
				select {
				case startup := <-done:
					t.Fatalf("startup=%v; connect=%v", startup, err)
				default:
					t.Fatal(err)
				}
			}
			defer cs.Close()
			resource, err := cs.ReadResource(ctx, &sdk.ReadResourceParams{URI: "kmx://host-runtime"})
			if err != nil {
				t.Fatal(err)
			}
			var descriptor kmxmcp.HostRuntime
			if json.Unmarshal([]byte(resource.Contents[0].Text), &descriptor) != nil {
				t.Fatal("runtime JSON")
			}
			if descriptor.Kind != "kmx.host-runtime" || !reflect.DeepEqual(descriptor.Args, args[:7]) || *loads != 0 || strings.Contains(resource.Contents[0].Text, "host-bridge-public-key") {
				t.Fatalf("descriptor=%+v loads=%d", descriptor, *loads)
			}
			if (descriptor.Broker != nil) != bound {
				t.Fatal("flag did not isolate bridge authority")
			}
			if bound {
				res, err := http.Get(descriptor.Broker.URL + "/next")
				if err != nil {
					t.Fatal(err)
				}
				res.Body.Close()
				if res.StatusCode != 401 {
					t.Fatal("public resource granted unsigned authority")
				}
			}
			_ = cw.Close() // EOF owns the child broker even without caller cancellation.
			select {
			case err := <-done:
				if err != nil {
					t.Fatal(err)
				}
			case <-ctx.Done():
				t.Fatal("stdio EOF did not stop server")
			}
			if bound {
				res, err := http.Get(descriptor.Broker.URL + "/next")
				if err == nil {
					res.Body.Close()
					t.Fatal("listener survived EOF")
				}
			}
		})
	}
}
func TestMCPWithoutBridgeStillStartsWhenRuntimeIdentityIsUnavailable(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows prevents deleting the current directory")
	}
	root, cwd := t.TempDir(), t.TempDir()
	t.Chdir(cwd)
	if err := os.Remove(cwd); err != nil {
		t.Fatal(err)
	}
	var out, diag bytes.Buffer
	deps, loads := testDependencies(&out, &diag)
	stdin, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	defer stdin.Close()
	deps.stdin = stdin
	if err := execute([]string{"mcp", "--root", root}, deps); err != nil || *loads != 0 {
		t.Fatalf("optional runtime discovery must not break ordinary MCP: %v loads=%d", err, *loads)
	}
}

func TestMCPHostBridgeInvalidKeyIsConstantAndHidden(t *testing.T) {
	var out, diag bytes.Buffer
	deps, loads := testDependencies(&out, &diag)
	for _, key := range []string{"untrusted-value", ""} {
		err := execute([]string{"mcp", "--host-bridge-public-key", key}, deps)
		if err == nil || err.Error() != "host tools unavailable" || *loads != 0 || strings.Contains(out.String()+diag.String(), "untrusted-value") {
			t.Fatalf("unsafe startup refusal: %v", err)
		}
	}
	out.Reset()
	diag.Reset()
	if err := execute([]string{"mcp", "--help"}, deps); err != nil || strings.Contains(out.String(), "host-bridge-public-key") {
		t.Fatal("runtime public-key flag must be hidden")
	}
}

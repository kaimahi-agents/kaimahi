package toolchain

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"sync/atomic"
	"testing"
	"time"
)

func TestEnsureCancellationBeforePublication(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	payload := []byte("fixture binary")
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.URL.Path == "/checksum" {
			fmt.Fprintf(w, "%x", sha256.Sum256(payload))
			return
		}
		w.Write(payload)
		w.(http.Flusher).Flush()
		cancel()
		select {
		case <-r.Context().Done():
		case <-time.After(300 * time.Millisecond):
		}
	}))
	defer server.Close()
	spec := Spec{Name: "fixture", Version: "1", URL: server.URL + "/payload", ChecksumURL: server.URL + "/checksum"}
	opt := Options{CacheDir: t.TempDir(), Context: ctx}
	if _, err := Ensure(spec, opt); !errors.Is(err, context.Canceled) {
		t.Fatalf("download ignored cancellation: %v", err)
	}
	if requests.Load() != 1 {
		t.Fatal("sent checksum after cancellation", requests.Load())
	}
	if _, err := os.Stat(spec.CachePath(opt.CacheDir)); !os.IsNotExist(err) {
		t.Fatal("published cancelled download", err)
	}
}

func TestProvisionCancellationBeforePATH(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Provision([]string{"kind", "kubectl"}, t.TempDir(), Options{CacheDir: t.TempDir(), Context: ctx}); !errors.Is(err, context.Canceled) {
		t.Fatal("advanced tool provisioning after cancellation", err)
	}
}

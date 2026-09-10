package app

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"syscall"
	"testing"
	"time"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/config"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/run"
)

// Run the real public entry point in a child so its SIGTERM handler is tested
// without sending signals to the test runner or accessing any real cluster.
func TestOrkaSignalHelper(t *testing.T) {
	if os.Getenv("KMX_ORKA_SIGNAL_HELPER") != "1" {
		return
	}
	a := &App{Cfg: &config.Config{KubeContext: "kind-test", ContextSource: config.SourceFlag}, Run: &run.Runner{}, Out: io.Discard, Err: io.Discard}
	opt := CreateOptions{Name: "sample", Namespace: "orka-system", ProviderType: "openai", Model: "local", Secret: "model-key", Out: filepath.Join(os.Getenv("KMX_ORKA_TEST_DIR"), "bundle.yaml"), Task: "Say hello", ResultServiceAccount: "reader", ResultPort: os.Getenv("KMX_ORKA_SIGNAL_PORT")}
	if err := a.CreateAgent(opt); err == nil {
		os.Exit(3)
	}
	os.Exit(0)
}

func TestOrkaSIGTERMCleansForwardAndStopsLaterWrites(t *testing.T) {
	_, opt, _, _, dir := orkaCreateFixture(t, "stale-ready")
	orkaResultServer(t, &opt, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(404)
		fmt.Fprint(w, `{"error":{"code":404,"message":"task not found"}}`)
	})
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, executable, "-test.run=^TestOrkaSignalHelper$")
	cmd.Env = append(os.Environ(), "KMX_ORKA_SIGNAL_HELPER=1", "KMX_ORKA_SIGNAL_PORT="+opt.ResultPort)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	defer func() { _ = cmd.Process.Kill() }()
	// Provider creation proves the access probe passed and the live forward is
	// owned by the command. Its stale condition prevents Agent/Task creation.
	for {
		if _, err := os.Stat(filepath.Join(dir, "sample-providers.core.orka.ai.json")); err == nil {
			break
		}
		select {
		case err := <-done:
			t.Fatalf("child exited before Provider wait: %v", err)
		case <-ctx.Done():
			t.Fatal("child did not reach Provider wait")
		case <-time.After(20 * time.Millisecond):
		}
	}
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatalf("SIGTERM did not return through cleanup: %v", err)
	}
	assertOrkaForwardExited(t, dir)
	for _, call := range orkaCalls(t, dir) {
		if call.Document != nil && call.Document["kind"] != "Provider" && !slices.Contains(call.Args, "--dry-run=server") {
			t.Fatal("later resource created after cancelled Provider wait")
		}
	}
}

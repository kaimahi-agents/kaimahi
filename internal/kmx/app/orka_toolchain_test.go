package app

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/config"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/run"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/toolchain"
)

func TestOrkaManagedToolchainHelper(t *testing.T) {
	mode := os.Getenv("KMX_ORKA_CACHE_HELPER")
	if mode == "" {
		return
	}
	// A cache miss must fail locally, never contact a download publisher.
	toolchainBase = "http://127.0.0.1:1"
	var out, diagnostics bytes.Buffer
	a := &App{Cfg: &config.Config{KubeContext: "kind-test", ContextSource: config.SourceFlag}, Run: &run.Runner{}, Out: &out, Err: &diagnostics}
	opt := CreateOptions{Name: "sample", Namespace: "orka-system", ProviderType: "openai", Model: "local", Secret: "model-key", Out: filepath.Join(os.Getenv("KMX_ORKA_TEST_DIR"), "bundle.yaml")}
	opt.DryRun = mode == "dry-run"
	if mode == "offline" {
		opt.Out = "-"
	}
	err := a.CreateAgent(opt)
	if mode == "off" {
		if err == nil || !strings.Contains(err.Error(), "kubectl is not on PATH") {
			t.Fatalf("disabled toolchain did not report missing dependency: %v", err)
		}
		return
	}
	if err != nil {
		t.Fatal(err)
	}
	if mode == "offline" {
		if len(a.provisioned) != 0 || !strings.Contains(out.String(), "kind: Provider") {
			t.Fatal("offline create provisioned a tool or omitted the bundle")
		}
		return
	}
	if len(a.provisioned) != 1 || a.provisioned[0].Name != "kubectl" || a.provisioned[0].Source != toolchain.FromCache {
		t.Fatalf("fresh process did not use verified cache: %+v", a.provisioned)
	}
	if _, err := os.Stat(opt.Out); err != nil {
		t.Fatal("successful online/dry-run must write its local review artifact")
	}
}

func TestOrkaCreateUsesManagedKubectlInFreshProcess(t *testing.T) {
	for _, mode := range []string{"online", "dry-run", "off", "offline"} {
		t.Run(mode, func(t *testing.T) {
			_, _, _, _, dir := orkaCreateFixture(t, "")
			t.Setenv("KMX_HOME", t.TempDir())
			t.Setenv("KMX_TOOLCHAIN", "")
			if mode == "off" {
				t.Setenv("KMX_TOOLCHAIN", "off")
			}
			cache, err := config.CacheDir()
			if err != nil {
				t.Fatal(err)
			}
			goos, goarch := toolchain.Platform()
			spec, _ := toolchain.Pinned("kubectl", goos, goarch)
			body, err := os.ReadFile(filepath.Join(dir, "kubectl"))
			if err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(cache, 0700); err != nil {
				t.Fatal(err)
			}
			path := spec.CachePath(cache)
			if err := os.WriteFile(path, body, 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path+".sha256", []byte(fmt.Sprintf("%x", sha256.Sum256(body))), 0600); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", t.TempDir())
			t.Setenv("KMX_ORKA_CACHE_HELPER", mode)
			executable, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			cmd := exec.CommandContext(t.Context(), executable, "-test.run=^TestOrkaManagedToolchainHelper$")
			if output, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("fresh %s process: %v\n%s", mode, err, output)
			}
			if mode == "off" || mode == "offline" {
				if len(orkaCalls(t, dir)) != 0 {
					t.Fatal("non-executing path invoked kubectl")
				}
			}
		})
	}
}

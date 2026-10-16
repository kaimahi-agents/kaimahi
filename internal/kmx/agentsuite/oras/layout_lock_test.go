package oras

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"oras.land/oras-go/v2/content/oci"
)

func TestPreparedLayoutConcurrentProcessesPreserveEverySuccessfulReference(t *testing.T) {
	ctx := context.Background()
	source := filepath.Join(t.TempDir(), "suite")
	copyDirectory(t, filepath.Join("..", "..", "..", "..", "testdata", "agentsuite", "minimal"), source)
	prepared := filepath.Join(t.TempDir(), "prepared")
	original, err := Push(ctx, source, prepared, "prepared")
	if err != nil {
		t.Fatal(err)
	}
	descriptor, err := json.Marshal(original.Descriptor)
	if err != nil {
		t.Fatal(err)
	}
	for round := 0; round < 8; round++ {
		target := filepath.Join(t.TempDir(), "published")
		if _, err := PushLayout(ctx, prepared, target, "seed", original.Descriptor); err != nil {
			t.Fatal(err)
		}
		processCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
		commands := make([]*exec.Cmd, 2)
		for i, ref := range []string{"alpha", "beta"} {
			cmd := exec.CommandContext(processCtx, os.Args[0], "-test.run=^TestLayoutPublicationProcessHelper$")
			cmd.Env = append(os.Environ(), "KMX_LAYOUT_HELPER=1", "KMX_LAYOUT_SOURCE="+prepared, "KMX_LAYOUT_TARGET="+target, "KMX_LAYOUT_REFERENCE="+ref, "KMX_LAYOUT_DESCRIPTOR="+string(descriptor))
			commands[i] = cmd
			if err := cmd.Start(); err != nil {
				cancel()
				t.Fatal(err)
			}
		}
		succeeded := make([]bool, 2)
		for i, cmd := range commands {
			err := cmd.Wait()
			if err != nil {
				var exit *exec.ExitError
				if errors.As(err, &exit) && exit.ExitCode() == 3 {
					continue
				}
				cancel()
				t.Fatalf("publication process failed unexpectedly: %v", err)
			}
			succeeded[i] = true
		}
		for i, success := range succeeded {
			if !success {
				continue
			}
			store, err := oci.New(target)
			if err != nil {
				cancel()
				t.Fatal(err)
			}
			ref := []string{"alpha", "beta"}[i]
			got, err := store.Resolve(ctx, ref)
			if err != nil || got.Digest != original.Descriptor.Digest {
				cancel()
				t.Fatalf("successful publication lost %s: %v", ref, err)
			}
		}
		cancel()
	}
}

func TestBothLayoutPublicationModesRefuseCompetingWriterBeforeStoreOpen(t *testing.T) {
	ctx := context.Background()
	source := filepath.Join(t.TempDir(), "suite")
	copyDirectory(t, filepath.Join("..", "..", "..", "..", "testdata", "agentsuite", "minimal"), source)
	prepared := filepath.Join(t.TempDir(), "prepared")
	original, err := Push(ctx, source, prepared, "prepared")
	if err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "published")
	if _, err := PushLayout(ctx, prepared, target, "seed", original.Descriptor); err != nil {
		t.Fatal(err)
	}
	release, err := lockLayoutPublication(ctx, target)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := release(); err != nil {
			t.Error(err)
		}
	}()
	if err := os.WriteFile(filepath.Join(target, "index.json"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	for _, publish := range []func() error{
		func() error { _, err := PushLayout(ctx, prepared, target, "new", original.Descriptor); return err },
		func() error { _, err := Push(ctx, source, target, "legacy"); return err },
	} {
		if err := publish(); err == nil || !strings.Contains(err.Error(), "another local layout publisher") {
			t.Fatalf("writer must refuse before reading unfinished index: %v", err)
		}
	}
}

func TestLayoutPublicationProcessHelper(t *testing.T) {
	if os.Getenv("KMX_LAYOUT_HELPER") != "1" {
		return
	}
	var descriptor ocispec.Descriptor
	if err := json.Unmarshal([]byte(os.Getenv("KMX_LAYOUT_DESCRIPTOR")), &descriptor); err != nil {
		os.Exit(2)
	}
	_, err := PushLayout(context.Background(), os.Getenv("KMX_LAYOUT_SOURCE"), os.Getenv("KMX_LAYOUT_TARGET"), os.Getenv("KMX_LAYOUT_REFERENCE"), descriptor)
	if err != nil {
		if strings.Contains(err.Error(), "another local layout publisher") {
			os.Exit(3)
		}
		os.Exit(2)
	}
	os.Exit(0)
}

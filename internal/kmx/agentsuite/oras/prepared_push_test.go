package oras

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"oras.land/oras-go/v2/content"
)

func TestPreparedPushPreservesEvaluatedDescriptorAfterSourceEdits(t *testing.T) {
	ctx := context.Background()
	source := filepath.Join(t.TempDir(), "suite")
	copyDirectory(t, filepath.Join("..", "..", "..", "..", "testdata", "agentsuite", "minimal"), source)
	prepared := filepath.Join(t.TempDir(), "prepared")
	original, err := Push(ctx, source, prepared, "prepared")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "unrelated.txt"), []byte("not evaluated"), 0600); err != nil {
		t.Fatal(err)
	}

	target := filepath.Join(t.TempDir(), "published")
	pushed, err := PushLayout(ctx, prepared, target, "v1", original.Descriptor)
	if err != nil {
		t.Fatal(err)
	}
	if !content.Equal(pushed.Descriptor, original.Descriptor) {
		t.Fatal("local publication changed prepared identity")
	}
	pulled, err := Pull(ctx, target, "v1", filepath.Join(t.TempDir(), "pulled"))
	if err != nil {
		t.Fatal(err)
	}
	if !content.Equal(pulled.Descriptor, original.Descriptor) {
		t.Fatal("pull changed prepared identity")
	}
	if _, err := os.Stat(filepath.Join(pulled.Path, "unrelated.txt")); !os.IsNotExist(err) {
		t.Fatal("publication repacked authored content")
	}
}

func TestPreparedRegistryPushPreservesDescriptorAndTagPolicy(t *testing.T) {
	ctx := context.Background()
	server, _ := newTestRegistryServer("", "")
	defer server.Close()
	t.Setenv("DOCKER_CONFIG", t.TempDir())
	source := filepath.Join(t.TempDir(), "suite")
	copyDirectory(t, filepath.Join("..", "..", "..", "..", "testdata", "agentsuite", "minimal"), source)
	prepared := filepath.Join(t.TempDir(), "prepared")
	original, err := Push(ctx, source, prepared, "prepared")
	if err != nil {
		t.Fatal(err)
	}
	ref := strings.TrimPrefix(server.URL, "http://") + "/team/suite:v1"
	pushed, err := PushLayoutRegistry(ctx, prepared, ref, true, false, original.Descriptor, PushOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !content.Equal(pushed.Descriptor, original.Descriptor) {
		t.Fatal("registry publication changed prepared identity")
	}
	if _, err := PushLayoutRegistry(ctx, prepared, ref, true, false, original.Descriptor, PushOptions{}); err != nil {
		t.Fatal("same-digest push not idempotent:", err)
	}
	pulled, err := PullRegistry(ctx, ref, filepath.Join(t.TempDir(), "pulled"), true)
	if err != nil {
		t.Fatal(err)
	}
	if !content.Equal(pulled.Descriptor, original.Descriptor) {
		t.Fatal("registry pull changed prepared identity")
	}
	if err := os.WriteFile(filepath.Join(source, "extra.txt"), []byte("new source"), 0600); err != nil {
		t.Fatal(err)
	}
	changed := filepath.Join(t.TempDir(), "changed")
	candidate, err := Push(ctx, source, changed, "prepared")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := PushLayoutRegistry(ctx, changed, ref, true, false, candidate.Descriptor, PushOptions{}); err == nil || !strings.Contains(err.Error(), "--force") {
		t.Fatalf("replacement error = %v", err)
	}
	if _, err := PushLayoutRegistry(ctx, changed, ref, true, true, candidate.Descriptor, PushOptions{}); err != nil {
		t.Fatal(err)
	}
}

func TestPreparedRegistryPushSharesProvenancePolicy(t *testing.T) {
	for _, api := range []bool{false, true} {
		t.Run(fmt.Sprintf("referrers API %t", api), func(t *testing.T) {
			ctx := context.Background()
			source, repository, registry := pushProvenanceFixture(t, api)
			prepared := filepath.Join(t.TempDir(), "prepared")
			original, err := Push(ctx, source, prepared, "prepared")
			if err != nil {
				t.Fatal(err)
			}
			ref := repository + ":v1"
			optedOut, err := PushLayoutRegistry(ctx, prepared, ref, true, false, original.Descriptor, PushOptions{})
			if err != nil || optedOut.Provenance != nil {
				t.Fatalf("opt-out: %+v, %v", optedOut, err)
			}
			options := PushOptions{Provenance: &ProvenanceOptions{BuilderVersion: "v9.9.9", Source: testSource(), Require: true}}
			repaired, err := PushLayoutRegistry(ctx, prepared, ref, true, false, original.Descriptor, options)
			if err != nil || repaired.Provenance == nil || repaired.Provenance.SourceCommit != testCommit || !content.Equal(repaired.Descriptor, original.Descriptor) {
				t.Fatalf("repair: %+v, %v", repaired, err)
			}
			pulled, err := PullRegistry(ctx, ref, filepath.Join(t.TempDir(), "pulled"), true)
			if err != nil || len(pulled.Provenance) != 1 || *repaired.Provenance != pulled.Provenance[0] {
				t.Fatalf("pull: %+v, %v", pulled, err)
			}
			uploads, puts := registry.counts()
			if _, err := PushLayoutRegistry(ctx, prepared, ref, true, false, original.Descriptor, options); err != nil {
				t.Fatal(err)
			}
			if u, p := registry.counts(); u != uploads || p != puts {
				t.Fatal("same-digest provenance push wrote again")
			}
			registry.rejectManifest = func(data []byte) bool { return bytes.Contains(data, []byte(InTotoMediaType)) }
			if _, err := PushLayoutRegistry(ctx, prepared, repository+":required", true, false, original.Descriptor, options); err == nil || !strings.Contains(err.Error(), "tag was not changed") {
				t.Fatalf("required attachment failure: %v", err)
			}
			if _, err := PullRegistry(ctx, repository+":required", filepath.Join(t.TempDir(), "absent"), true); err == nil {
				t.Fatal("required attachment failure moved tag")
			}
			options.Provenance.Require = false
			optional, err := PushLayoutRegistry(ctx, prepared, repository+":optional", true, false, original.Descriptor, options)
			if err != nil || optional.Provenance != nil || len(optional.Warnings) != 1 {
				t.Fatalf("optional attachment failure: %+v, %v", optional, err)
			}
		})
	}
}

func TestPreparedPushRefusesUnexpectedDescriptorBeforeDestinationWrites(t *testing.T) {
	ctx := context.Background()
	source := filepath.Join(t.TempDir(), "suite")
	copyDirectory(t, filepath.Join("..", "..", "..", "..", "testdata", "agentsuite", "minimal"), source)
	prepared := filepath.Join(t.TempDir(), "prepared")
	original, err := Push(ctx, source, prepared, "prepared")
	if err != nil {
		t.Fatal(err)
	}
	wrong := original.Descriptor
	wrong.Size++
	target := filepath.Join(t.TempDir(), "published")
	if _, err := PushLayout(ctx, prepared, target, "v1", wrong); err == nil || !strings.Contains(err.Error(), "descriptor") {
		t.Fatalf("unexpected descriptor error = %v", err)
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatal("mismatching source created destination")
	}
}

func TestPreparedPushRefusesInvalidSourceBeforeDestinationWrites(t *testing.T) {
	root := t.TempDir()
	if _, err := PushLayout(context.Background(), filepath.Join(root, "missing"), filepath.Join(root, "destination"), "v1", ocispec.Descriptor{}); err == nil {
		t.Fatal("missing source accepted")
	}
	if _, err := os.Stat(filepath.Join(root, "destination")); !os.IsNotExist(err) {
		t.Fatal("invalid source created destination")
	}
}

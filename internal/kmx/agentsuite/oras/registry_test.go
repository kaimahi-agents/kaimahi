package oras

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"oras.land/oras-go/v2/content"
	"oras.land/oras-go/v2/content/memory"
	"oras.land/oras-go/v2/registry/remote/auth"
	"oras.land/oras-go/v2/registry/remote/credentials"
)

func TestRegistryRepositoryUsesDockerCompatibleCredentials(t *testing.T) {
	ctx := context.Background()
	configRoot := t.TempDir()
	if err := os.WriteFile(
		filepath.Join(configRoot, "config.json"),
		[]byte(`{"auths":{"registry.example.com":{"auth":"YWdlbnQ6c2VjcmV0"}}}`),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DOCKER_CONFIG", configRoot)
	want := auth.Credential{Username: "agent", Password: "secret"}

	repository, reference, err := newRegistryRepository(
		"registry.example.com/team/suite:v1",
		false,
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	if repository.Reference.Registry != "registry.example.com" ||
		repository.Reference.Repository != "team/suite" ||
		reference != "v1" ||
		repository.PlainHTTP {
		t.Fatalf("repository = %+v, reference = %q", repository, reference)
	}
	client, ok := repository.Client.(*auth.Client)
	if !ok {
		t.Fatalf("registry client = %T, want *auth.Client", repository.Client)
	}
	got, err := client.Credential(ctx, "registry.example.com")
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("credential = %+v, want %+v", got, want)
	}
}

func TestRegistryReferenceRequiresRepositoryAndTagOrDigest(t *testing.T) {
	for _, value := range []string{"team:v1", "registry.example.com/team"} {
		if _, _, err := newRegistryRepository(value, false, credentials.NewMemoryStore()); err == nil {
			t.Fatalf("newRegistryRepository(%q) succeeded", value)
		}
	}
}

func TestTargetPushPullPreservesDescriptorAndExtracts(t *testing.T) {
	ctx := context.Background()
	source := filepath.Join(t.TempDir(), "suite")
	copyDirectory(t, filepath.Join("..", "testdata", "minimal"), source)
	packedRoot, packedStore, packed, err := packDirectory(ctx, source)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { removeAll(t, packedRoot) })

	target := memory.New()
	if _, err := NewPusher(target).Push(ctx, packedStore, packed.Descriptor, "v1"); err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(t.TempDir(), "suite")
	pulled, err := pullToDirectory(ctx, target, "v1", output)
	if err != nil {
		t.Fatal(err)
	}
	if !content.Equal(pulled.Descriptor, packed.Descriptor) {
		t.Fatalf("pulled descriptor = %+v, want %+v", pulled.Descriptor, packed.Descriptor)
	}
	manifest, err := os.ReadFile(filepath.Join(output, "agentsuite.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(manifest) == 0 || pulled.Report.Name != "minimal" {
		t.Fatalf("manifest bytes = %d, report = %+v", len(manifest), pulled.Report)
	}
}

func TestRegistryPushPullOverHTTP(t *testing.T) {
	ctx := context.Background()
	server, registry := newTestRegistryServer("agent", "secret")
	t.Cleanup(server.Close)
	registryHost := strings.TrimPrefix(server.URL, "http://")

	configRoot := t.TempDir()
	if err := os.WriteFile(
		filepath.Join(configRoot, "config.json"),
		[]byte(`{"auths":{"`+registryHost+`":{"auth":"YWdlbnQ6c2VjcmV0"}}}`),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DOCKER_CONFIG", configRoot)

	source := filepath.Join(t.TempDir(), "source")
	copyDirectory(t, filepath.Join("..", "testdata", "minimal"), source)
	layout := filepath.Join(t.TempDir(), "layout")
	local, err := Push(ctx, source, layout, "v1")
	if err != nil {
		t.Fatal(err)
	}

	reference := registryHost + "/team/suite:v1"
	if _, err := PushRegistry(ctx, source, reference, false, false); err == nil {
		t.Fatal("PushRegistry() to an HTTP registry without plain HTTP opt-in succeeded")
	}
	pushed, err := PushRegistry(ctx, source, reference, true, false)
	if err != nil {
		t.Fatal(err)
	}
	if !content.Equal(pushed.Descriptor, local.Descriptor) {
		t.Fatalf("registry descriptor = %+v, want local layout descriptor %+v", pushed.Descriptor, local.Descriptor)
	}

	if _, err := PullRegistry(ctx, reference, filepath.Join(t.TempDir(), "https-output"), false); err == nil {
		t.Fatal("PullRegistry() from an HTTP registry without plain HTTP opt-in succeeded")
	}
	output := filepath.Join(t.TempDir(), "output")
	pulled, err := PullRegistry(ctx, reference, output, true)
	if err != nil {
		t.Fatal(err)
	}
	if !content.Equal(pulled.Descriptor, pushed.Descriptor) {
		t.Fatalf("pulled descriptor = %+v, want %+v", pulled.Descriptor, pushed.Descriptor)
	}
	localOutput := filepath.Join(t.TempDir(), "local-output")
	if _, err := Pull(ctx, layout, "v1", localOutput); err != nil {
		t.Fatal(err)
	}
	assertDirectoryFilesEqual(t, localOutput, output)

	challenges, authenticated := registry.authenticationCounts()
	if challenges == 0 || authenticated == 0 {
		t.Fatalf("authentication challenges = %d, authenticated requests = %d", challenges, authenticated)
	}
}

func removeAll(t *testing.T, path string) {
	t.Helper()
	if err := os.RemoveAll(path); err != nil {
		t.Error(err)
	}
}

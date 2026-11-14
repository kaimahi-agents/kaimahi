package app

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	kmxsuite "github.com/kaimahi-agents/kaimahi/internal/kmx/agentsuite"
)

func TestBuildSuiteAtomicallyReplacesExistingOutput(t *testing.T) {
	output := filepath.Join(t.TempDir(), "image.oci.tar")
	if err := os.WriteFile(output, []byte("existing"), 0o600); err != nil {
		t.Fatal(err)
	}
	archive, _, _, _ := buildSuiteArchive(t, false)
	called := false
	builder := sandboxBuilderFunc(func(_ context.Context, _ kmxsuite.SandboxPlan, dst io.Writer) (kmxsuite.BuildResult, error) {
		called = true
		_, err := dst.Write(archive)
		return kmxsuite.BuildResult{}, err
	})
	if _, err := (&App{}).BuildSuite(context.Background(), minimalSuitePath(t), output, kmxsuite.BuildSelection{}, builder); err != nil {
		t.Fatalf("BuildSuite() error = %v", err)
	}
	if !called {
		t.Fatal("builder was not called")
	}
	content, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != string(archive) {
		t.Fatalf("output = %q", content)
	}
}

func TestBuildSuitePreservesExistingOutputAfterBuilderFailure(t *testing.T) {
	parent := t.TempDir()
	output := filepath.Join(parent, "image.oci.tar")
	if err := os.WriteFile(output, []byte("existing"), 0o600); err != nil {
		t.Fatal(err)
	}
	builder := sandboxBuilderFunc(func(_ context.Context, _ kmxsuite.SandboxPlan, dst io.Writer) (kmxsuite.BuildResult, error) {
		_, _ = dst.Write([]byte("partial"))
		return kmxsuite.BuildResult{}, errors.New("build failed")
	})
	_, err := (&App{}).BuildSuite(context.Background(), minimalSuitePath(t), output, kmxsuite.BuildSelection{}, builder)
	if err == nil || !strings.Contains(err.Error(), "build failed") {
		t.Fatalf("BuildSuite() error = %v", err)
	}
	content, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "existing" {
		t.Fatalf("output changed after failure: %q", content)
	}
	staged, err := filepath.Glob(filepath.Join(parent, ".image.oci.tar-*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(staged) != 0 {
		t.Fatalf("staged outputs remain after failure: %v", staged)
	}
}

func TestBuildSuiteRejectsNonDirectoryOutputParent(t *testing.T) {
	parent := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(parent, []byte("file"), 0o600); err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(parent, "image.oci.tar")
	builder := sandboxBuilderFunc(func(context.Context, kmxsuite.SandboxPlan, io.Writer) (kmxsuite.BuildResult, error) {
		return kmxsuite.BuildResult{}, nil
	})
	_, err := (&App{}).BuildSuite(context.Background(), minimalSuitePath(t), output, kmxsuite.BuildSelection{}, builder)
	if err == nil {
		t.Fatal("BuildSuite() succeeded")
	}
}

func TestBuildSuiteRejectsDirectoryOutput(t *testing.T) {
	output := filepath.Join(t.TempDir(), "image.oci.tar")
	if err := os.Mkdir(output, 0o755); err != nil {
		t.Fatal(err)
	}
	builder := sandboxBuilderFunc(func(context.Context, kmxsuite.SandboxPlan, io.Writer) (kmxsuite.BuildResult, error) {
		return kmxsuite.BuildResult{}, nil
	})
	_, err := (&App{}).BuildSuite(context.Background(), minimalSuitePath(t), output, kmxsuite.BuildSelection{}, builder)
	if err == nil || !strings.Contains(err.Error(), "is a directory") {
		t.Fatalf("BuildSuite() error = %v", err)
	}
}

func minimalSuitePath(t *testing.T) string {
	t.Helper()
	// Git does not preserve read/write bits; checkout umask is not fixture policy.
	// Copy the unchanged public bytes into a test-owned tree with known modes.
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	copyTree(t, filepath.Join("..", "..", "..", "testdata", "agentsuite", "minimal"), root)
	return root
}

type sandboxBuilderFunc func(context.Context, kmxsuite.SandboxPlan, io.Writer) (kmxsuite.BuildResult, error)

func (fn sandboxBuilderFunc) Build(
	ctx context.Context,
	plan kmxsuite.SandboxPlan,
	dst io.Writer,
) (kmxsuite.BuildResult, error) {
	return fn(ctx, plan, dst)
}

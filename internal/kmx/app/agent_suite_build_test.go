package app

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentsuite"
)

func TestBuildSuiteAtomicallyReplacesExistingOutput(t *testing.T) {
	output := filepath.Join(t.TempDir(), "image.oci.tar")
	if err := os.WriteFile(output, []byte("existing"), 0o600); err != nil {
		t.Fatal(err)
	}
	called := false
	builder := sandboxBuilderFunc(func(_ context.Context, _ agentsuite.SandboxPlan, dst io.Writer) (agentsuite.BuildResult, error) {
		called = true
		_, err := dst.Write([]byte("replacement"))
		return agentsuite.BuildResult{}, err
	})
	if _, err := (&App{}).BuildSuite(context.Background(), minimalSuitePath(), output, agentsuite.BuildSelection{}, builder); err != nil {
		t.Fatalf("BuildSuite() error = %v", err)
	}
	if !called {
		t.Fatal("builder was not called")
	}
	content, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "replacement" {
		t.Fatalf("output = %q", content)
	}
}

func TestBuildSuitePreservesExistingOutputAfterBuilderFailure(t *testing.T) {
	parent := t.TempDir()
	output := filepath.Join(parent, "image.oci.tar")
	if err := os.WriteFile(output, []byte("existing"), 0o600); err != nil {
		t.Fatal(err)
	}
	builder := sandboxBuilderFunc(func(_ context.Context, _ agentsuite.SandboxPlan, dst io.Writer) (agentsuite.BuildResult, error) {
		_, _ = dst.Write([]byte("partial"))
		return agentsuite.BuildResult{}, errors.New("build failed")
	})
	_, err := (&App{}).BuildSuite(context.Background(), minimalSuitePath(), output, agentsuite.BuildSelection{}, builder)
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
	builder := sandboxBuilderFunc(func(context.Context, agentsuite.SandboxPlan, io.Writer) (agentsuite.BuildResult, error) {
		return agentsuite.BuildResult{}, nil
	})
	_, err := (&App{}).BuildSuite(context.Background(), minimalSuitePath(), output, agentsuite.BuildSelection{}, builder)
	if err == nil {
		t.Fatal("BuildSuite() succeeded")
	}
}

func TestBuildSuiteRejectsDirectoryOutput(t *testing.T) {
	output := filepath.Join(t.TempDir(), "image.oci.tar")
	if err := os.Mkdir(output, 0o755); err != nil {
		t.Fatal(err)
	}
	builder := sandboxBuilderFunc(func(context.Context, agentsuite.SandboxPlan, io.Writer) (agentsuite.BuildResult, error) {
		return agentsuite.BuildResult{}, nil
	})
	_, err := (&App{}).BuildSuite(context.Background(), minimalSuitePath(), output, agentsuite.BuildSelection{}, builder)
	if err == nil || !strings.Contains(err.Error(), "is a directory") {
		t.Fatalf("BuildSuite() error = %v", err)
	}
}

func minimalSuitePath() string {
	return filepath.Join("..", "agentsuite", "testdata", "minimal")
}

type sandboxBuilderFunc func(context.Context, agentsuite.SandboxPlan, io.Writer) (agentsuite.BuildResult, error)

func (fn sandboxBuilderFunc) Build(
	ctx context.Context,
	plan agentsuite.SandboxPlan,
	dst io.Writer,
) (agentsuite.BuildResult, error) {
	return fn(ctx, plan, dst)
}

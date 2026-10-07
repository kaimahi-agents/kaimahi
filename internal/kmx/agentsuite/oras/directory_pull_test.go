package oras

import (
	"archive/tar"
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestPullExtractsReferencedAgentSuite(t *testing.T) {
	source := filepath.Join("..", "testdata", "minimal")
	layout := filepath.Join(t.TempDir(), "layout")
	reference := "agentsuites/minimal:v1"
	pushed, err := Push(context.Background(), source, layout, reference)
	if err != nil {
		t.Fatal(err)
	}

	output := filepath.Join(t.TempDir(), "suite")
	pulled, err := Pull(context.Background(), layout, reference, output)
	if err != nil {
		t.Fatal(err)
	}
	if pulled.Path != output ||
		pulled.Reference != reference ||
		pulled.Descriptor.Digest != pushed.Descriptor.Digest ||
		pulled.Report.Name != "minimal" {
		t.Fatalf("pull result = %+v, pushed root = %+v", pulled, pushed.Descriptor)
	}
	for _, name := range []string{"oci-layout", "index.json", "blobs"} {
		if _, err := os.Stat(filepath.Join(output, name)); !os.IsNotExist(err) {
			t.Fatalf("pull output contains OCI layout entry %s: %v", name, err)
		}
	}
	assertDirectoryFilesEqual(t, source, output)
	if runtime.GOOS != "windows" {
		info, err := os.Stat(output)
		if err != nil {
			t.Fatal(err)
		}
		if mode := info.Mode().Perm(); mode != 0o755 {
			t.Fatalf("pull output mode = %#o, want 0755", mode)
		}
	}
}

func TestPullRefusesExistingOutput(t *testing.T) {
	source := filepath.Join("..", "testdata", "minimal")
	layout := filepath.Join(t.TempDir(), "layout")
	reference := "agentsuites/minimal:v1"
	if _, err := Push(context.Background(), source, layout, reference); err != nil {
		t.Fatal(err)
	}
	output := t.TempDir()
	keep := filepath.Join(output, "keep")
	if err := os.WriteFile(keep, []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Pull(context.Background(), layout, reference, output); err == nil {
		t.Fatal("pull to existing output succeeded")
	}
	data, err := os.ReadFile(keep)
	if err != nil || string(data) != "keep" {
		t.Fatalf("existing output changed: data=%q err=%v", data, err)
	}
}

func TestPullFailureLeavesNoOutput(t *testing.T) {
	source := filepath.Join("..", "testdata", "minimal")
	layout := filepath.Join(t.TempDir(), "layout")
	if _, err := Push(context.Background(), source, layout, "agentsuites/minimal:v1"); err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(t.TempDir(), "suite")
	if _, err := Pull(context.Background(), layout, "agentsuites/missing:v1", output); err == nil {
		t.Fatal("missing reference pulled successfully")
	}
	if _, err := os.Stat(output); !os.IsNotExist(err) {
		t.Fatalf("failed pull left output behind: %v", err)
	}
}

func TestExtractionPathRejectsTraversal(t *testing.T) {
	for _, name := range []string{"../escape", "/absolute", `windows\path`} {
		if _, err := extractionPath(&tar.Header{Name: name, Typeflag: tar.TypeReg}); err == nil {
			t.Fatalf("extractionPath(%q) succeeded", name)
		}
	}
}

func assertDirectoryFilesEqual(t *testing.T, wantRoot, gotRoot string) {
	t.Helper()
	if err := filepath.WalkDir(wantRoot, func(wantPath string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(wantRoot, wantPath)
		if err != nil || relative == "." {
			return err
		}
		gotPath := filepath.Join(gotRoot, relative)
		gotInfo, err := os.Lstat(gotPath)
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if !gotInfo.IsDir() {
				t.Fatalf("%s is not a directory", relative)
			}
			return nil
		}
		wantBytes, err := os.ReadFile(wantPath)
		if err != nil {
			return err
		}
		gotBytes, err := os.ReadFile(gotPath)
		if err != nil {
			return err
		}
		if string(wantBytes) != string(gotBytes) {
			t.Fatalf("%s differs after pull", relative)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

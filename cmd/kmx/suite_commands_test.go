package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentsuite"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"oras.land/oras-go/v2/content/oci"
)

func TestSuiteValidateMinimalLayout(t *testing.T) {
	fixture := copySuiteFixture(t, filepath.Join("..", "..", "internal", "kmx", "agentsuite", "testdata", "minimal"))
	for _, tc := range []struct {
		name string
		args []string
		json bool
	}{
		{name: "text", args: []string{"suite", "validate", fixture}},
		{name: "json", args: []string{"suite", "validate", fixture, "--output", "json"}, json: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out, diagnostics bytes.Buffer
			deps, loads := testDependencies(&out, &diagnostics)
			if err := execute(tc.args, deps); err != nil {
				t.Fatalf("execute(%v) error = %v\n%s", tc.args, err, diagnostics.String())
			}
			if *loads != 0 {
				t.Fatalf("offline suite validation loaded operational config %d time(s)", *loads)
			}
			if !tc.json {
				want := "AgentSuite minimal: conformant (agents=1 toolProviders=0 compositions=1 toolProviderCompositions=0 capabilities=none)\n"
				if out.String() != want {
					t.Fatalf("text output = %q, want %q", out.String(), want)
				}
				return
			}
			var report agentsuite.Report
			if err := json.Unmarshal(out.Bytes(), &report); err != nil {
				t.Fatalf("decode JSON output: %v\n%s", err, out.String())
			}
			if report.Name != "minimal" || report.Agents != 1 || report.ToolProviders != 0 || report.Compositions != 1 {
				t.Fatalf("unexpected JSON report: %+v", report)
			}
			if strings.TrimSpace(diagnostics.String()) != "" {
				t.Fatalf("unexpected diagnostics: %s", diagnostics.String())
			}
		})
	}
}

func TestSuitePushMinimalDirectory(t *testing.T) {
	fixture := copySuiteFixture(t, filepath.Join("..", "..", "internal", "kmx", "agentsuite", "testdata", "minimal"))
	outputRoot := t.TempDir()
	firstOutput := filepath.Join(outputRoot, "first")
	secondOutput := filepath.Join(outputRoot, "second")

	var out, diagnostics bytes.Buffer
	deps, loads := testDependencies(&out, &diagnostics)
	if err := execute([]string{
		"suite", "push", fixture,
		"--to-layout", firstOutput,
		"agentsuites/minimal:v1",
	}, deps); err != nil {
		t.Fatalf("suite push error = %v\n%s", err, diagnostics.String())
	}
	if *loads != 0 {
		t.Fatalf("offline suite push loaded operational config %d time(s)", *loads)
	}
	firstReport, err := agentsuite.ValidatePath(firstOutput)
	if err != nil {
		t.Fatalf("validate pushed layout: %v", err)
	}
	if firstReport.Name != "minimal" {
		t.Fatalf("pushed suite = %q, want minimal", firstReport.Name)
	}
	if !strings.Contains(out.String(), "Pushed AgentSuite minimal") ||
		!strings.Contains(out.String(), "Created local OCI layout") ||
		!strings.Contains(out.String(), "agentsuites/minimal:v1") ||
		!strings.Contains(out.String(), "sha256:") {
		t.Fatalf("push output = %q", out.String())
	}

	out.Reset()
	if err := execute([]string{
		"suite", "push", fixture,
		"--to-layout", secondOutput,
		"agentsuites/minimal:v1",
	}, deps); err != nil {
		t.Fatalf("second suite push error = %v\n%s", err, diagnostics.String())
	}
	firstRoot := readLayoutRoot(t, firstOutput)
	secondRoot := readLayoutRoot(t, secondOutput)
	if firstRoot.MediaType != secondRoot.MediaType ||
		firstRoot.Digest != secondRoot.Digest ||
		firstRoot.Size != secondRoot.Size {
		t.Fatalf("pushed roots differ: %+v != %+v", firstRoot, secondRoot)
	}
}

func TestSuitePushAddsReferencesToExistingLayout(t *testing.T) {
	fixture := copySuiteFixture(t, filepath.Join("..", "..", "internal", "kmx", "agentsuite", "testdata", "minimal"))
	target := filepath.Join(t.TempDir(), "layout")
	var out, diagnostics bytes.Buffer
	deps, _ := testDependencies(&out, &diagnostics)
	for i, reference := range []string{"agentsuites/alpha:v1", "agentsuites/beta:v1"} {
		out.Reset()
		if err := execute([]string{
			"suite", "push", fixture,
			"--to-layout", target,
			reference,
		}, deps); err != nil {
			t.Fatalf("push %s: %v\n%s", reference, err, diagnostics.String())
		}
		if i == 0 && !strings.Contains(out.String(), "Created local OCI layout") {
			t.Fatalf("new-layout output = %q", out.String())
		}
		if i == 1 && !strings.Contains(out.String(), "Updated existing local OCI layout") {
			t.Fatalf("existing-layout output = %q", out.String())
		}
		if i == 1 && !strings.Contains(out.String(), "unrelated references were preserved") {
			t.Fatalf("existing-layout preservation output = %q", out.String())
		}
	}
	layout, err := oci.New(target)
	if err != nil {
		t.Fatal(err)
	}
	for _, reference := range []string{"agentsuites/alpha:v1", "agentsuites/beta:v1"} {
		if _, err := layout.Resolve(context.Background(), reference); err != nil {
			t.Fatalf("resolve %s: %v", reference, err)
		}
	}
	index := readLayoutIndex(t, target)
	if len(index.Manifests) != 2 {
		t.Fatalf("layout has %d references, want 2", len(index.Manifests))
	}
}

func TestSuitePushRefusesNestedTarget(t *testing.T) {
	fixture := copySuiteFixture(t, filepath.Join("..", "..", "internal", "kmx", "agentsuite", "testdata", "minimal"))
	target := filepath.Join(fixture, "layout")
	var out, diagnostics bytes.Buffer
	deps, _ := testDependencies(&out, &diagnostics)
	if err := execute([]string{
		"suite", "push", fixture,
		"--to-layout", target,
		"agentsuites/minimal:v1",
	}, deps); err == nil {
		t.Fatal("suite push succeeded")
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatalf("nested target exists after refusal: %v", err)
	}
}

func TestSuitePushFailureLeavesNoNewTarget(t *testing.T) {
	source := t.TempDir()
	target := filepath.Join(t.TempDir(), "layout")
	var out, diagnostics bytes.Buffer
	deps, loads := testDependencies(&out, &diagnostics)
	if err := execute([]string{
		"suite", "push", source,
		"--to-layout", target,
		"agentsuites/invalid:v1",
	}, deps); err == nil {
		t.Fatal("invalid AgentSuite pushed successfully")
	}
	if *loads != 0 {
		t.Fatalf("failed offline suite push loaded operational config %d time(s)", *loads)
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatalf("failed push left target behind: %v", err)
	}
}

func TestSuitePullExtractsDirectory(t *testing.T) {
	fixture := copySuiteFixture(t, filepath.Join("..", "..", "internal", "kmx", "agentsuite", "testdata", "minimal"))
	layout := filepath.Join(t.TempDir(), "layout")
	output := filepath.Join(t.TempDir(), "suite")
	reference := "agentsuites/minimal:v1"
	var out, diagnostics bytes.Buffer
	deps, loads := testDependencies(&out, &diagnostics)
	if err := execute([]string{
		"suite", "push", fixture,
		"--to-layout", layout,
		reference,
	}, deps); err != nil {
		t.Fatalf("suite push error = %v\n%s", err, diagnostics.String())
	}

	out.Reset()
	if err := execute([]string{
		"suite", "pull", reference,
		"--from-layout", layout,
		"--output", output,
	}, deps); err != nil {
		t.Fatalf("suite pull error = %v\n%s", err, diagnostics.String())
	}
	if *loads != 0 {
		t.Fatalf("offline suite push/pull loaded operational config %d time(s)", *loads)
	}
	if !strings.Contains(out.String(), "Pulled and extracted AgentSuite minimal") ||
		!strings.Contains(out.String(), reference) ||
		!strings.Contains(out.String(), output) {
		t.Fatalf("pull output = %q", out.String())
	}
	report, err := agentsuite.ValidatePath(output)
	if err != nil {
		t.Fatalf("validate extracted suite: %v", err)
	}
	if report.Name != "minimal" {
		t.Fatalf("pulled suite = %q, want minimal", report.Name)
	}
	for _, name := range []string{"oci-layout", "index.json", "blobs"} {
		if _, err := os.Stat(filepath.Join(output, name)); !os.IsNotExist(err) {
			t.Fatalf("pull output contains OCI layout entry %s: %v", name, err)
		}
	}
	assertDirectoryContentsEqual(t, fixture, output)
}

func TestSuiteTransferHelpIsLocalOCILayoutOnly(t *testing.T) {
	for _, test := range []struct {
		args    []string
		example string
	}{
		{
			args:    []string{"suite", "push", "--help"},
			example: "kmx suite push ./suite --to-layout ./layout agentsuites/team:v1",
		},
		{
			args:    []string{"suite", "pull", "--help"},
			example: "kmx suite pull agentsuites/team:v1 --from-layout ./layout --output ./suite",
		},
	} {
		var out, diagnostics bytes.Buffer
		deps, loads := testDependencies(&out, &diagnostics)
		if err := execute(test.args, deps); err != nil {
			t.Fatalf("execute(%v): %v\n%s", test.args, err, diagnostics.String())
		}
		if *loads != 0 {
			t.Fatalf("help loaded operational config %d time(s)", *loads)
		}
		for _, want := range []string{"local OCI image layout", "filesystem directory", "Registry", test.example} {
			if !strings.Contains(out.String(), want) {
				t.Fatalf("help for %v does not contain %q:\n%s", test.args, want, out.String())
			}
		}
	}
}

func TestSuiteTransferFlagsMatchIssue(t *testing.T) {
	group := newSuiteCommand()
	tests := []struct {
		command string
		flags   []string
	}{
		{command: "push", flags: []string{"to-layout"}},
		{command: "pull", flags: []string{"from-layout", "output"}},
	}
	for _, test := range tests {
		command, _, err := group.Find([]string{test.command})
		if err != nil {
			t.Fatal(err)
		}
		for _, name := range test.flags {
			flag := command.Flags().Lookup(name)
			if flag == nil {
				t.Fatalf("suite %s is missing --%s", test.command, name)
			}
			if flag.Shorthand != "" {
				t.Fatalf("suite %s --%s has undocumented shorthand -%s", test.command, name, flag.Shorthand)
			}
		}
		for _, name := range []string{"reference", "registry", "plain-http", "username", "password"} {
			if command.Flags().Lookup(name) != nil {
				t.Fatalf("suite %s unexpectedly exposes --%s", test.command, name)
			}
		}
	}
}

func readLayoutRoot(t *testing.T, root string) ocispec.Descriptor {
	t.Helper()
	index := readLayoutIndex(t, root)
	if len(index.Manifests) != 1 {
		t.Fatalf("layout has %d manifests, want 1", len(index.Manifests))
	}
	return index.Manifests[0]
}

func readLayoutIndex(t *testing.T, root string) ocispec.Index {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, "index.json"))
	if err != nil {
		t.Fatal(err)
	}
	var index ocispec.Index
	if err := json.Unmarshal(data, &index); err != nil {
		t.Fatal(err)
	}
	return index
}

func assertDirectoryContentsEqual(t *testing.T, wantRoot, gotRoot string) {
	t.Helper()
	if err := filepath.WalkDir(wantRoot, func(wantPath string, entry fs.DirEntry, walkErr error) error {
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

func copySuiteFixture(t *testing.T, source string) string {
	t.Helper()
	destination := t.TempDir()
	if err := filepath.WalkDir(source, func(name string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(source, name)
		if err != nil || relative == "." {
			return err
		}
		target := filepath.Join(destination, relative)
		if entry.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		data, err := os.ReadFile(name)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, 0o644)
	}); err != nil {
		t.Fatal(err)
	}
	return destination
}

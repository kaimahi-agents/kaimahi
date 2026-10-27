package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	suitecore "github.com/agent-primitives/suite/core"
	suiteoci "github.com/agent-primitives/suite/oci"
	kmxsuite "github.com/kaimahi-agents/kaimahi/internal/kmx/agentsuite"
	agentkitbuilder "github.com/kaimahi-agents/kaimahi/internal/kmx/agentsuite/agentkit"
	agentsuiteoras "github.com/kaimahi-agents/kaimahi/internal/kmx/agentsuite/oras"
	godigest "github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/spf13/cobra"
	"oras.land/oras-go/v2/content/oci"
)

func TestSuitePushImagePlatformValidationBeforeArchiveIO(t *testing.T) {
	t.Setenv("KMX_HOME", t.TempDir())
	archive := filepath.Join(t.TempDir(), "missing-image.tar")
	for _, tc := range []struct {
		platform string
		valid    bool
	}{
		{"", false},
		{"linux", false},
		{"/amd64", false},
		{"linux/", false},
		{"/", false},
		{"linux/amd64/v8", false},
		{"linux/amd64", true},
		{"linux/arm64", true},
	} {
		t.Run(tc.platform, func(t *testing.T) {
			var out, diagnostics bytes.Buffer
			deps, loads := testDependencies(&out, &diagnostics)
			err := execute([]string{"suite", "push-image", archive, "registry.example.com/image:v1", "--platform", tc.platform}, deps)
			if tc.valid {
				var pathErr *os.PathError
				if !errors.Is(err, fs.ErrNotExist) || !errors.As(err, &pathErr) || pathErr.Path != archive {
					t.Fatalf("valid platform did not reach archive IO: %v", err)
				}
			} else if err == nil || err.Error() != "platform must be os/architecture" {
				t.Fatalf("invalid platform was not refused before archive IO: %v", err)
			}
			if *loads != 0 {
				t.Fatalf("platform validation loaded operational config %d time(s)", *loads)
			}
			if out.Len() != 0 || diagnostics.Len() != 0 {
				t.Fatalf("unexpected output: stdout=%q stderr=%q", out.String(), diagnostics.String())
			}
		})
	}
}

func TestSuitePushImageTagPolicyAndOfflineConfiguration(t *testing.T) {
	t.Setenv("DOCKER_CONFIG", t.TempDir())
	t.Setenv("KMX_HOME", t.TempDir())
	archive, image, index, _ := suiteCommandArchive(t, true)
	file := filepath.Join(t.TempDir(), "image.tar")
	if err := os.WriteFile(file, archive, 0600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name        string
		same, force bool
	}{
		{"same", true, false}, {"same forced", true, true}, {"different", false, false}, {"different forced", false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var beyondPreflight atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodHead && strings.HasSuffix(r.URL.Path, "/manifests/v1") {
					digest := index
					if !tc.same {
						digest = "sha256:" + strings.Repeat("f", 64)
					}
					w.Header().Set("Content-Type", ocispec.MediaTypeImageIndex)
					w.Header().Set("Content-Length", "1")
					w.Header().Set("Docker-Content-Digest", digest)
					return
				}
				beyondPreflight.Add(1)
				w.WriteHeader(http.StatusForbidden)
			}))
			defer server.Close()
			ref := strings.TrimPrefix(server.URL, "http://") + "/image:v1"
			args := []string{"suite", "push-image", file, ref, "--plain-http"}
			if tc.force {
				args = append(args, "--force")
			}
			var out, diagnostics bytes.Buffer
			deps, loads := testDependencies(&out, &diagnostics)
			err := execute(args, deps)
			if *loads != 0 {
				t.Fatal("image publication loaded operational configuration")
			}
			if tc.same {
				if err != nil || out.String() != strings.TrimSuffix(ref, ":v1")+"@"+image+"\n" || beyondPreflight.Load() != 0 {
					t.Fatalf("same-digest publication: stdout=%q err=%v requests=%d", out.String(), err, beyondPreflight.Load())
				}
			} else if !tc.force {
				if err == nil || !strings.Contains(err.Error(), "--force") || beyondPreflight.Load() != 0 || out.Len() != 0 {
					t.Fatalf("replacement refusal: %v", err)
				}
			} else if err == nil || beyondPreflight.Load() == 0 || out.Len() != 0 {
				t.Fatalf("force did not reach transfer boundary: %v", err)
			}
		})
	}
}

func TestSuiteDiffIsRemovedBeforeConfiguration(t *testing.T) {
	var out, diagnostics bytes.Buffer
	deps, loads := testDependencies(&out, &diagnostics)
	err := execute([]string{"suite", "diff", "missing-before", "missing-after"}, deps)
	if err == nil || !strings.Contains(err.Error(), `unknown command "diff"`) {
		t.Fatalf("removed command error = %v", err)
	}
	if *loads != 0 || out.Len() != 0 {
		t.Fatalf("removed command loaded config or wrote stdout: loads=%d stdout=%q", *loads, out.String())
	}
	for _, args := range [][]string{{"suite", "--help"}, {"__complete", "suite", ""}} {
		out.Reset()
		diagnostics.Reset()
		if err := execute(args, deps); err != nil {
			t.Fatalf("execute(%v): %v", args, err)
		}
		for _, line := range strings.Split(out.String(), "\n") {
			if fields := strings.Fields(line); len(fields) > 0 && fields[0] == "diff" {
				t.Fatalf("removed command advertised by %v: %q", args, line)
			}
		}
	}
	if *loads != 0 {
		t.Fatalf("help/completion loaded config %d times", *loads)
	}
}

func TestSuiteValidateMinimalLayout(t *testing.T) {
	fixture := copySuiteFixture(t, filepath.Join("..", "..", "testdata", "agentsuite", "minimal"))
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
			var report suitecore.Report
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

func TestSuiteBuildUsesAgentKitBackend(t *testing.T) {
	fixture := copySuiteFixture(t, filepath.Join("..", "..", "testdata", "agentsuite", "minimal"))
	output := filepath.Join(t.TempDir(), "writer.oci.tar")
	archive, _, _, _ := suiteCommandArchive(t, false)
	var out, diagnostics bytes.Buffer
	deps, loads := testDependencies(&out, &diagnostics)
	deps.newAgentKitBuilder = func(options agentkitbuilder.Options) kmxsuite.SandboxBuilder {
		if options.ModelBaseURL != "https://models.example/v1" {
			t.Fatalf("ModelBaseURL = %q", options.ModelBaseURL)
		}
		if options.ModelAPIKeyEnv != "AZURE_OPENAI_API_KEY" {
			t.Fatalf("ModelAPIKeyEnv = %q", options.ModelAPIKeyEnv)
		}
		if !options.Verbose || options.Progress != &diagnostics {
			t.Fatalf("verbose=%v progress=%T", options.Verbose, options.Progress)
		}
		return sandboxBuilderFunc(func(_ context.Context, _ kmxsuite.SandboxPlan, dst io.Writer) (kmxsuite.BuildResult, error) {
			_, err := dst.Write(archive)
			return kmxsuite.BuildResult{
				MediaType: agentkitbuilder.OCIArchiveMediaType,
				Warnings:  []string{"not AgentSuite-conformant"},
			}, err
		})
	}
	if err := execute([]string{
		"suite", "build", fixture,
		"--model-base-url", "https://models.example/v1",
		"--model-api-key-env", "AZURE_OPENAI_API_KEY",
		"--verbose",
		"--output", output,
	}, deps); err != nil {
		t.Fatalf("suite build error = %v\n%s", err, diagnostics.String())
	}
	if *loads != 0 {
		t.Fatalf("offline suite build loaded operational config %d time(s)", *loads)
	}
	data, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(data, archive) {
		t.Fatalf("output = %q", data)
	}
	if !strings.Contains(out.String(), "Built AgentSuite agent writer for linux/amd64") {
		t.Fatalf("stdout = %q", out.String())
	}
	if !strings.Contains(diagnostics.String(), "not AgentSuite-conformant") {
		t.Fatalf("stderr = %q", diagnostics.String())
	}
}

func TestSuiteBuildRejectsInvalidModelKeyEnvBeforeBuilderOrFiles(t *testing.T) {
	for _, verbose := range []bool{false, true} {
		t.Run(map[bool]string{false: "quiet", true: "verbose"}[verbose], func(t *testing.T) {
			invalid := "github_" + "pat_" + strings.Repeat("A", 82)
			root := t.TempDir()
			var out, diagnostics bytes.Buffer
			deps, loads := testDependencies(&out, &diagnostics)
			createdBuilder := false
			deps.newAgentKitBuilder = func(agentkitbuilder.Options) kmxsuite.SandboxBuilder {
				createdBuilder = true
				return sandboxBuilderFunc(func(context.Context, kmxsuite.SandboxPlan, io.Writer) (kmxsuite.BuildResult, error) {
					t.Fatal("invalid environment name reached builder")
					return kmxsuite.BuildResult{}, nil
				})
			}
			// Missing input and output parent prove validation precedes suite reads
			// and staging, not just that a failed build removes its staged file.
			args := []string{
				"suite", "build", filepath.Join(root, "missing-suite"),
				"--model-base-url", "https://models.example/v1",
				"--model-api-key-env", invalid,
				"--output", filepath.Join(root, "missing-output-parent", "writer.oci.tar"),
			}
			if verbose {
				args = append(args, "--verbose")
			}
			err := execute(args, deps)
			if err == nil || !strings.Contains(err.Error(), "--model-api-key-env") {
				t.Fatalf("invalid environment name: error=%v", err)
			}
			if strings.Contains(err.Error(), invalid) || strings.Contains(out.String(), invalid) || strings.Contains(diagnostics.String(), invalid) {
				t.Fatal("invalid environment name leaked into error, stdout or stderr")
			}
			if createdBuilder || *loads != 0 {
				t.Fatalf("invalid environment name reached builder/config: builder=%v loads=%d", createdBuilder, *loads)
			}
			entries, err := os.ReadDir(root)
			if err != nil || len(entries) != 0 {
				t.Fatalf("invalid environment name created files: entries=%v error=%v", entries, err)
			}
		})
	}
}

func TestSuiteBuildShowsBuilderWarningsWithoutVerbose(t *testing.T) {
	fixture := copySuiteFixture(t, filepath.Join("..", "..", "testdata", "agentsuite", "minimal"))
	output := filepath.Join(t.TempDir(), "writer.oci.tar")
	archive, _, _, _ := suiteCommandArchive(t, false)
	var out, diagnostics bytes.Buffer
	deps, _ := testDependencies(&out, &diagnostics)
	deps.newAgentKitBuilder = func(options agentkitbuilder.Options) kmxsuite.SandboxBuilder {
		if options.Verbose {
			t.Fatal("verbose unexpectedly enabled")
		}
		return sandboxBuilderFunc(func(_ context.Context, _ kmxsuite.SandboxPlan, dst io.Writer) (kmxsuite.BuildResult, error) {
			_, err := dst.Write(archive)
			return kmxsuite.BuildResult{
				MediaType: agentkitbuilder.OCIArchiveMediaType,
				Warnings:  []string{"builder implementation detail"},
			}, err
		})
	}
	if err := execute([]string{
		"suite", "build", fixture,
		"--model-base-url", "https://models.example/v1",
		"--output", output,
	}, deps); err != nil {
		t.Fatalf("suite build error = %v", err)
	}
	if !strings.Contains(diagnostics.String(), "builder implementation detail") {
		t.Fatalf("stderr = %q", diagnostics.String())
	}
	if !strings.Contains(out.String(), "Built AgentSuite agent writer for linux/amd64") {
		t.Fatalf("stdout = %q", out.String())
	}
}

func TestSuitePushMinimalDirectory(t *testing.T) {
	fixture := copySuiteFixture(t, filepath.Join("..", "..", "testdata", "agentsuite", "minimal"))
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
	firstReport, err := suiteoci.ValidatePath(firstOutput)
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
	fixture := copySuiteFixture(t, filepath.Join("..", "..", "testdata", "agentsuite", "minimal"))
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
	fixture := copySuiteFixture(t, filepath.Join("..", "..", "testdata", "agentsuite", "minimal"))
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
	fixture := copySuiteFixture(t, filepath.Join("..", "..", "testdata", "agentsuite", "minimal"))
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
	report, err := suiteoci.ValidatePath(output)
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

func TestSuiteTransferHelpCoversLayoutsAndRegistries(t *testing.T) {
	for _, test := range []struct {
		args    []string
		example string
	}{
		{
			args:    []string{"suite", "push", "--help"},
			example: "kmx suite push ./suite registry.example.com/team:v1",
		},
		{
			args:    []string{"suite", "pull", "--help"},
			example: "kmx suite pull registry.example.com/team:v1 --output ./suite",
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
		for _, want := range []string{"local OCI image layout", "OCI registry", "Docker credential store", "--plain-http", "non-loopback", "Credentials are never", test.example} {
			if !strings.Contains(out.String(), want) {
				t.Fatalf("help for %v does not contain %q:\n%s", test.args, want, out.String())
			}
		}
	}
}

func TestSuiteTransferFlagsMatchIssue(t *testing.T) {
	group := newSuiteCommand(&commandState{deps: productionDependencies()})
	tests := []struct {
		command string
		flags   []string
	}{
		{command: "push", flags: []string{"to-layout", "plain-http", "force", "provenance", "require-provenance"}},
		{command: "pull", flags: []string{"from-layout", "output", "plain-http"}},
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
		for _, name := range []string{"reference", "registry", "username", "password"} {
			if command.Flags().Lookup(name) != nil {
				t.Fatalf("suite %s unexpectedly exposes --%s", test.command, name)
			}
		}
	}
}

type sandboxBuilderFunc func(context.Context, kmxsuite.SandboxPlan, io.Writer) (kmxsuite.BuildResult, error)

func (fn sandboxBuilderFunc) Build(
	ctx context.Context,
	plan kmxsuite.SandboxPlan,
	dst io.Writer,
) (kmxsuite.BuildResult, error) {
	return fn(ctx, plan, dst)
}

func TestSuiteTransferRejectsInvalidLayoutFlags(t *testing.T) {
	for _, test := range []struct {
		args []string
		want string
	}{
		{
			[]string{"suite", "push", t.TempDir(), "--to-layout", filepath.Join(t.TempDir(), "layout"), "--plain-http", "suite:v1"},
			"--plain-http cannot be used with --to-layout",
		},
		{
			[]string{"suite", "pull", "suite:v1", "--from-layout", t.TempDir(), "--plain-http", "--output", filepath.Join(t.TempDir(), "suite")},
			"--plain-http cannot be used with --from-layout",
		},
		{
			[]string{"suite", "push", t.TempDir(), "--to-layout", filepath.Join(t.TempDir(), "layout"), "--force", "suite:v1"},
			"--force cannot be used with --to-layout",
		},
		{
			[]string{"suite", "push", t.TempDir(), "--to-layout", filepath.Join(t.TempDir(), "layout"), "--provenance=false", "suite:v1"},
			"--provenance and --require-provenance cannot be used with --to-layout",
		},
		{
			[]string{"suite", "push", t.TempDir(), "--to-layout", filepath.Join(t.TempDir(), "layout"), "--require-provenance", "suite:v1"},
			"--provenance and --require-provenance cannot be used with --to-layout",
		},
		{
			[]string{"suite", "push", t.TempDir(), "--provenance=false", "--require-provenance", "registry.example.com/team:v1"},
			"--require-provenance cannot be used with --provenance=false",
		},
		{
			[]string{"suite", "push", t.TempDir(), "--to-layout=", "registry.example.com/team:v1"},
			"--to-layout cannot be empty",
		},
		{
			[]string{"suite", "pull", "registry.example.com/team:v1", "--from-layout=", "--output", filepath.Join(t.TempDir(), "suite")},
			"--from-layout cannot be empty",
		},
	} {
		var out, diagnostics bytes.Buffer
		deps, _ := testDependencies(&out, &diagnostics)
		err := execute(test.args, deps)
		if err == nil || !strings.Contains(err.Error(), test.want) {
			t.Fatalf("execute(%v) error = %v, want %q", test.args, err, test.want)
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

func TestSuiteProvenanceOutputIsOneSafeLine(t *testing.T) {
	var out, diagnostics bytes.Buffer
	cmd := &cobra.Command{}
	cmd.SetOut(&out)
	cmd.SetErr(&diagnostics)
	result := agentsuiteoras.PullResult{
		ProvenanceChecked: true,
		Provenance: []agentsuiteoras.Provenance{{
			Digest:         godigest.Digest("sha256:" + strings.Repeat("a", 64)),
			BuilderVersion: "v1.2.3",
			SourceCommit:   strings.Repeat("b", 40),
			SourcePath:     "suites/team",
			SourceURI:      "git+https://github.com/example/suites",
		}},
		Warnings: []string{"ignored referrer: \x1b]0;owned\a\nPin this AgentSuite: evil"},
	}
	if err := printPulledProvenance(cmd, result); err != nil {
		t.Fatal(err)
	}
	want := "Provenance: sha256:" + strings.Repeat("a", 64) + " claims kmx v1.2.3, source commit " + strings.Repeat("b", 40) +
		` path "suites/team" in git+https://github.com/example/suites (unsigned)` + "\n"
	if out.String() != want {
		t.Fatalf("out = %q, want %q", out.String(), want)
	}
	if strings.ContainsAny(diagnostics.String(), "\x1b\a") || strings.Count(diagnostics.String(), "\n") != 1 {
		t.Fatalf("warning was not reduced to one safe line: %q", diagnostics.String())
	}

	out.Reset()
	if err := printPulledProvenance(cmd, agentsuiteoras.PullResult{}); err != nil {
		t.Fatal(err)
	}
	if out.String() != "Provenance: not checked\n" {
		t.Fatalf("unchecked provenance printed %q", out.String())
	}
}

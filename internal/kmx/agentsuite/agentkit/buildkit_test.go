package agentkit

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"

	buildkitclient "github.com/moby/buildkit/client"
	"github.com/moby/buildkit/exporter/containerimage/exptypes"
	gatewayclient "github.com/moby/buildkit/frontend/gateway/client"
	specs "github.com/opencontainers/image-spec/specs-go/v1"
	agentkitconfig "github.com/sozercan/agentkit/pkg/agentkit/config"
	"github.com/sozercan/agentkit/pkg/agentkit/effective"
)

func TestBuildkitExporterWritesNamedOCIArchive(t *testing.T) {
	client := &fakeBuildkitClient{archive: []byte("oci archive")}
	manager := &fakeBuildkitManager{}
	exporter := buildkitExporter{
		address: "docker-container://test",
		manager: manager,
		clientFactory: func(_ context.Context, address string) (buildkitClient, error) {
			if address != "docker-container://test" {
				t.Fatalf("address = %q", address)
			}
			return client, nil
		},
	}
	var output bytes.Buffer
	err := exporter.ExportOCI(t.Context(), agentImage{
		Agent: effective.Agent{
			Metadata: agentkitconfig.Metadata{Name: "incident-analyst"},
		},
		AdapterRef: "registry.example/harness@sha256:" + strings.Repeat("a", 64),
		OS:         "linux", Architecture: "amd64",
		SourceEpoch: 1790388400,
	}, &output)
	if err != nil {
		t.Fatalf("ExportOCI() error = %v", err)
	}
	if output.String() != "oci archive" {
		t.Fatalf("output = %q", output.String())
	}
	if manager.calls != 1 || client.waitCalls != 1 || client.closeCalls != 1 {
		t.Fatalf("manager calls=%d wait=%d close=%d", manager.calls, client.waitCalls, client.closeCalls)
	}
	if len(client.solveOpt.Exports) != 1 ||
		client.solveOpt.Exports[0].Type != buildkitclient.ExporterOCI ||
		client.solveOpt.Exports[0].Attrs["name"] != "incident-analyst:latest" ||
		client.solveOpt.Exports[0].Attrs["source-date-epoch"] != "1790388400" ||
		client.solveOpt.Exports[0].Attrs["rewrite-timestamp"] != "true" {
		t.Fatalf("exports = %+v", client.solveOpt.Exports)
	}
	if client.product != "kaimahi-agentkit" || client.buildFunc == nil {
		t.Fatalf("product=%q buildFunc=%v", client.product, client.buildFunc)
	}
}

func TestBuildkitExporterShowsVerboseProgress(t *testing.T) {
	client := &fakeBuildkitClient{archive: []byte("oci archive")}
	var progress bytes.Buffer
	exporter := buildkitExporter{
		address:  "docker-container://test",
		manager:  &fakeBuildkitManager{},
		verbose:  true,
		progress: &progress,
		clientFactory: func(context.Context, string) (buildkitClient, error) {
			return client, nil
		},
	}
	err := exporter.ExportOCI(t.Context(), agentImage{
		Agent: effective.Agent{Metadata: agentkitconfig.Metadata{Name: "writer"}},
		OS:    "linux", Architecture: "amd64",
	}, io.Discard)
	if err != nil {
		t.Fatalf("ExportOCI() error = %v", err)
	}
	if !client.hadStatus || !strings.Contains(progress.String(), "waiting for managed daemon readiness") {
		t.Fatalf("had status=%v progress=%q", client.hadStatus, progress.String())
	}
}

func TestBuildkitExporterProgressWriteFailure(t *testing.T) {
	exporter := buildkitExporter{verbose: true, progress: failingWriter{}}
	err := exporter.progressf("progress")
	if err == nil || !strings.Contains(err.Error(), "write BuildKit progress") {
		t.Fatalf("progressf() error = %v", err)
	}
}

func TestBuildkitExporterReportsLifecycleFailures(t *testing.T) {
	tests := []struct {
		name    string
		manager buildkitManager
		factory buildkitClientFactory
		want    string
	}{
		{
			name:    "manager",
			manager: &fakeBuildkitManager{err: errors.New("docker unavailable")},
			want:    "ensure managed BuildKit daemon",
		},
		{
			name: "connect",
			factory: func(context.Context, string) (buildkitClient, error) {
				return nil, errors.New("dial failed")
			},
			want: "connect to BuildKit",
		},
		{
			name: "wait",
			factory: func(context.Context, string) (buildkitClient, error) {
				return &fakeBuildkitClient{waitErr: errors.New("not ready")}, nil
			},
			want: "wait for managed BuildKit daemon",
		},
		{
			name: "build",
			factory: func(context.Context, string) (buildkitClient, error) {
				return &fakeBuildkitClient{buildErr: errors.New("solve failed")}, nil
			},
			want: "export OCI archive with BuildKit",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			manager := test.manager
			if manager == nil {
				manager = &fakeBuildkitManager{}
			}
			factory := test.factory
			if factory == nil {
				factory = func(context.Context, string) (buildkitClient, error) {
					return &fakeBuildkitClient{}, nil
				}
			}
			err := (buildkitExporter{
				address:       "docker-container://test",
				manager:       manager,
				clientFactory: factory,
			}).ExportOCI(t.Context(), agentImage{}, io.Discard)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("ExportOCI() error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestAgentBuildDefinitionContainsAgentKitImageConfiguration(t *testing.T) {
	plan := minimalPlan()
	builder := New(Options{
		ModelBaseURL:   "https://example.openai.azure.com/openai/v1/",
		ModelAPIKeyEnv: "AZURE_OPENAI_API_KEY",
	})
	cfg := builder.agentConfig(plan)
	image := agentImage{
		Agent:        effective.FromConfig(&cfg, string(plan.Instructions)),
		AdapterRef:   plan.Harness.ImageRef,
		OS:           "linux",
		Architecture: "amd64",
	}
	platform := &specs.Platform{OS: image.OS, Architecture: image.Architecture}

	definition, configJSON, err := agentBuildDefinition(t.Context(), image, platform)
	if err != nil {
		t.Fatalf("agentBuildDefinition() error = %v", err)
	}
	if definition == nil || len(definition.Def) == 0 {
		t.Fatal("AgentKit produced an empty LLB definition")
	}
	var config specs.Image
	if err := json.Unmarshal(configJSON, &config); err != nil {
		t.Fatalf("decode image config: %v", err)
	}
	if config.Platform.OS != "linux" || config.Platform.Architecture != "amd64" {
		t.Fatalf("platform = %+v", config.Platform)
	}
	if config.Config.User != "1000:1000" ||
		len(config.Config.Entrypoint) != 1 ||
		config.Config.Labels["ai.sozercan.agentkit.name"] != plan.Agent.ID {
		t.Fatalf("image config = %+v", config.Config)
	}
}

func TestSolveAgentImageReturnsExporterMetadata(t *testing.T) {
	image, platform := testAgentImage(t)
	gateway := &fakeGatewaySolver{result: gatewayclient.NewResult()}

	result, err := solveAgentImage(t.Context(), gateway, image, platform)
	if err != nil {
		t.Fatalf("solveAgentImage() error = %v", err)
	}
	if gateway.request.Definition == nil || len(gateway.request.Definition.Def) == 0 {
		t.Fatal("gateway received an empty LLB definition")
	}
	configJSON := result.Metadata[exptypes.ExporterImageConfigKey]
	if len(configJSON) == 0 {
		t.Fatalf("metadata = %v", result.Metadata)
	}
	var config specs.Image
	if err := json.Unmarshal(configJSON, &config); err != nil {
		t.Fatalf("decode image config: %v", err)
	}
	if config.Config.Labels["ai.sozercan.agentkit.name"] != "writer" {
		t.Fatalf("labels = %v", config.Config.Labels)
	}
}

func TestSolveAgentImageReportsGatewayFailures(t *testing.T) {
	image, platform := testAgentImage(t)
	tests := []struct {
		name    string
		gateway *fakeGatewaySolver
		want    string
	}{
		{
			name:    "solve",
			gateway: &fakeGatewaySolver{err: errors.New("gateway unavailable")},
			want:    "solve AgentKit LLB",
		},
		{
			name: "invalid result",
			gateway: &fakeGatewaySolver{result: &gatewayclient.Result{
				Refs: map[string]gatewayclient.Reference{"linux/amd64": nil},
			}},
			want: "read AgentKit solve result",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := solveAgentImage(t.Context(), test.gateway, image, platform)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("solveAgentImage() error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestNopWriteCloserDoesNotCloseDestination(t *testing.T) {
	var output bytes.Buffer
	writer := nopWriteCloser{Writer: &output}
	if _, err := io.WriteString(writer, "first"); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(&output, " second"); err != nil {
		t.Fatal(err)
	}
	if output.String() != "first second" {
		t.Fatalf("output = %q", output.String())
	}
}

func testAgentImage(t *testing.T) (agentImage, *specs.Platform) {
	t.Helper()
	plan := minimalPlan()
	builder := New(Options{
		ModelBaseURL:   "https://example.openai.azure.com/openai/v1/",
		ModelAPIKeyEnv: "AZURE_OPENAI_API_KEY",
	})
	cfg := builder.agentConfig(plan)
	image := agentImage{
		Agent:        effective.FromConfig(&cfg, string(plan.Instructions)),
		AdapterRef:   plan.Harness.ImageRef,
		OS:           "linux",
		Architecture: "amd64",
	}
	return image, &specs.Platform{OS: image.OS, Architecture: image.Architecture}
}

type fakeBuildkitManager struct {
	calls int
	err   error
}

func (f *fakeBuildkitManager) Ensure(context.Context) error {
	f.calls++
	return f.err
}

type fakeBuildkitClient struct {
	archive    []byte
	waitErr    error
	buildErr   error
	waitCalls  int
	closeCalls int
	solveOpt   buildkitclient.SolveOpt
	product    string
	buildFunc  gatewayclient.BuildFunc
	hadStatus  bool
}

func (f *fakeBuildkitClient) Build(
	_ context.Context,
	opt buildkitclient.SolveOpt,
	product string,
	buildFunc gatewayclient.BuildFunc,
	status chan *buildkitclient.SolveStatus,
) (*buildkitclient.SolveResponse, error) {
	f.solveOpt = opt
	f.product = product
	f.buildFunc = buildFunc
	if status != nil {
		f.hadStatus = true
		defer close(status)
	}
	if f.buildErr != nil {
		return nil, f.buildErr
	}
	if len(opt.Exports) != 0 && opt.Exports[0].Output != nil {
		writer, err := opt.Exports[0].Output(nil)
		if err != nil {
			return nil, err
		}
		if _, err := writer.Write(f.archive); err != nil {
			return nil, err
		}
		if err := writer.Close(); err != nil {
			return nil, err
		}
	}
	return &buildkitclient.SolveResponse{}, nil
}

func (f *fakeBuildkitClient) Wait(context.Context) error {
	f.waitCalls++
	return f.waitErr
}

func (f *fakeBuildkitClient) Close() error {
	f.closeCalls++
	return nil
}

type fakeGatewaySolver struct {
	request gatewayclient.SolveRequest
	result  *gatewayclient.Result
	err     error
}

func (f *fakeGatewaySolver) Solve(
	_ context.Context,
	request gatewayclient.SolveRequest,
) (*gatewayclient.Result, error) {
	f.request = request
	if f.result == nil && f.err == nil {
		f.result = gatewayclient.NewResult()
	}
	return f.result, f.err
}

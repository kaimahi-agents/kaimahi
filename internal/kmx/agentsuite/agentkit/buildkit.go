package agentkit

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"time"

	buildkitclient "github.com/moby/buildkit/client"
	_ "github.com/moby/buildkit/client/connhelper/dockercontainer"
	"github.com/moby/buildkit/client/llb"
	"github.com/moby/buildkit/exporter/containerimage/exptypes"
	gatewayclient "github.com/moby/buildkit/frontend/gateway/client"
	"github.com/moby/buildkit/util/progress/progressui"
	specs "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/sozercan/agentkit/pkg/agentkit/effective"
	agentllb "github.com/sozercan/agentkit/pkg/agentkit2llb/agent"
)

type agentImage struct {
	Agent        effective.Agent
	AdapterRef   string
	OS           string
	Architecture string
	SourceEpoch  int64
}

type ociExporter interface {
	ExportOCI(context.Context, agentImage, io.Writer) error
}

type buildkitExporter struct {
	address       string
	manager       buildkitManager
	clientFactory buildkitClientFactory
	verbose       bool
	progress      io.Writer
}

type buildkitClient interface {
	Build(context.Context, buildkitclient.SolveOpt, string, gatewayclient.BuildFunc, chan *buildkitclient.SolveStatus) (*buildkitclient.SolveResponse, error)
	Wait(context.Context) error
	Close() error
}

type buildkitClientFactory func(context.Context, string) (buildkitClient, error)

type gatewaySolver interface {
	Solve(context.Context, gatewayclient.SolveRequest) (*gatewayclient.Result, error)
}

func (e buildkitExporter) ExportOCI(ctx context.Context, image agentImage, dst io.Writer) error {
	if e.manager != nil {
		if err := e.manager.Ensure(ctx); err != nil {
			return fmt.Errorf("ensure managed BuildKit daemon: %w", err)
		}
	}
	factory := e.clientFactory
	if factory == nil {
		factory = func(ctx context.Context, address string) (buildkitClient, error) {
			return buildkitclient.New(ctx, address)
		}
	}
	client, err := factory(ctx, e.address)
	if err != nil {
		return fmt.Errorf("connect to BuildKit: %w", err)
	}
	defer client.Close()
	if e.manager != nil {
		if err := e.progressf("BuildKit: waiting for managed daemon readiness\n"); err != nil {
			return err
		}
		readyCtx, cancel := context.WithTimeout(ctx, 45*time.Second)
		defer cancel()
		if err := client.Wait(readyCtx); err != nil {
			return fmt.Errorf("wait for managed BuildKit daemon: %w", err)
		}
	}

	platform := specs.Platform{OS: image.OS, Architecture: image.Architecture}
	var status chan *buildkitclient.SolveStatus
	var displayDone chan error
	if e.verbose && e.progress != nil {
		display, err := progressui.NewDisplay(e.progress, progressui.PlainMode)
		if err != nil {
			return fmt.Errorf("create BuildKit progress display: %w", err)
		}
		status = make(chan *buildkitclient.SolveStatus)
		displayDone = make(chan error, 1)
		go func() {
			_, err := display.UpdateFrom(ctx, status)
			displayDone <- err
		}()
	}
	_, err = client.Build(ctx, buildkitclient.SolveOpt{
		Exports: []buildkitclient.ExportEntry{{
			Type: buildkitclient.ExporterOCI,
			Attrs: map[string]string{
				"name":              image.Agent.Metadata.Name + ":latest",
				"source-date-epoch": strconv.FormatInt(image.SourceEpoch, 10),
				"rewrite-timestamp": "true",
			},
			Output: func(map[string]string) (io.WriteCloser, error) {
				return nopWriteCloser{Writer: dst}, nil
			},
		}},
	}, "kaimahi-agentkit", func(ctx context.Context, gateway gatewayclient.Client) (*gatewayclient.Result, error) {
		return solveAgentImage(ctx, gateway, image, &platform)
	}, status)
	if displayDone != nil {
		if displayErr := <-displayDone; err == nil && displayErr != nil {
			return fmt.Errorf("display BuildKit progress: %w", displayErr)
		}
	}
	if err != nil {
		return fmt.Errorf("export OCI archive with BuildKit: %w", err)
	}
	return nil
}

func (e buildkitExporter) progressf(format string, args ...any) error {
	if !e.verbose || e.progress == nil {
		return nil
	}
	if _, err := fmt.Fprintf(e.progress, format, args...); err != nil {
		return fmt.Errorf("write BuildKit progress: %w", err)
	}
	return nil
}

func solveAgentImage(
	ctx context.Context,
	gateway gatewaySolver,
	image agentImage,
	platform *specs.Platform,
) (*gatewayclient.Result, error) {
	definition, configJSON, err := agentBuildDefinition(ctx, image, platform)
	if err != nil {
		return nil, err
	}
	solved, err := gateway.Solve(ctx, gatewayclient.SolveRequest{Definition: definition.ToPB()})
	if err != nil {
		return nil, fmt.Errorf("solve AgentKit LLB: %w", err)
	}
	ref, err := solved.SingleRef()
	if err != nil {
		return nil, fmt.Errorf("read AgentKit solve result: %w", err)
	}
	result := gatewayclient.NewResult()
	result.SetRef(ref)
	result.AddMeta(exptypes.ExporterImageConfigKey, configJSON)
	return result, nil
}

func agentBuildDefinition(ctx context.Context, image agentImage, platform *specs.Platform) (*llb.Definition, []byte, error) {
	state, imageConfig, err := agentllb.Agentkit2LLB(image.Agent, image.AdapterRef, platform)
	if err != nil {
		return nil, nil, fmt.Errorf("convert AgentKit agent to LLB: %w", err)
	}
	definition, err := state.Marshal(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("marshal AgentKit LLB: %w", err)
	}
	configJSON, err := json.Marshal(imageConfig)
	if err != nil {
		return nil, nil, fmt.Errorf("marshal AgentKit image config: %w", err)
	}
	return definition, configJSON, nil
}

type nopWriteCloser struct {
	io.Writer
}

func (nopWriteCloser) Close() error {
	return nil
}

package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentsuite"
	suiteoras "github.com/kaimahi-agents/kaimahi/internal/kmx/agentsuite/oras"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/config"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/imagelift"
	agentruntime "github.com/kaimahi-agents/kaimahi/internal/kmx/runtime"
)

// LiftAgentImage installs one marked HTTP agent image into a prepared target.
func (a *App) LiftAgentImage(ctx context.Context, reference, environment string, planOnly bool) error {
	file, err := os.Open(environment)
	if err != nil {
		return err
	}
	data, readErr := io.ReadAll(io.LimitReader(file, (64<<10)+1))
	closeErr := file.Close()
	if err := errors.Join(readErr, closeErr); err != nil {
		return err
	}
	env, err := imagelift.DecodeEnvironment(data)
	if err != nil {
		return err
	}
	if a.Cfg.ContextSource == config.SourceFlag && a.Cfg.KubeContext != env.Context {
		return errors.New("--context differs from deployment environment")
	}
	worker := a.withRunContext(ctx)
	copyApp := *worker
	cfg := *a.Cfg
	cfg.KubeContext, cfg.ContextSource = env.Context, config.SourceFlag
	copyApp.Cfg = &cfg
	copyApp.guarded = false
	if len(env.Members) > 0 {
		return copyApp.liftSuiteImages(ctx, reference, env, planOnly)
	}
	if env.PlainHTTP && !strings.HasPrefix(env.Context, "kind-") {
		return errors.New("plain HTTP image transport is restricted to explicit local kind environments")
	}
	image, err := suiteoras.ResolveImageRegistryTransport(ctx, reference, env.Platform, env.PlainHTTP)
	if err != nil {
		return err
	}
	root, err := os.MkdirTemp("", "kmx-image-lift-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(root)
	pulled, err := suiteoras.PullRegistry(ctx, image.Deployment.SuiteReference, filepath.Join(root, "suite"), env.PlainHTTP)
	if err != nil {
		return err
	}
	if pulled.Descriptor.Digest.String() != image.Deployment.SuiteDigest {
		return errors.New("resolved source suite differs from image record")
	}
	if err := agentsuite.ValidateImageSource(pulled.Path, image.Deployment); err != nil {
		return err
	}
	resolved, err := agentsuite.ResolveDeploymentSuite(pulled.Path, env.Platform)
	if err != nil {
		return err
	}
	for _, member := range resolved.Members() {
		if member.Agent.ID == image.Deployment.Agent {
			env.Instructions = string(member.Instructions)
		}
	}
	plan, err := imagelift.Render(image.Reference, image.Deployment, env)
	if err != nil {
		return err
	}
	cluster := imageLiftCluster{app: &copyApp}
	var adapter imagelift.Adapter = imagelift.KubernetesHTTPAdapter{Cluster: cluster}
	copyApp.notef("Image lift: %s to context %s, namespace %s (plan %s)", image.Reference, env.Context, env.Namespace, plan.Digest)
	if planOnly {
		if err := adapter.Inspect(ctx, plan); err != nil {
			return err
		}
		// Literal configuration stays in memory; the public plan lists identities.
		return json.NewEncoder(a.Out).Encode(struct{ Name, Context, Namespace, Image, SuiteDigest, PlanDigest string }{plan.Name, plan.Context, plan.Namespace, plan.Image, plan.SuiteDigest, plan.Digest})
	}
	receipt, deployErr := adapter.Deploy(ctx, plan, func() error {
		return copyApp.guardWith("lift AgentSuite image", "kmx lift "+reference+" --environment "+environment, env.Namespace, true, false)
	})
	if err := json.NewEncoder(a.Out).Encode(receipt); err != nil {
		return errors.Join(deployErr, err)
	}
	return deployErr
}

func (a *App) liftSuiteImages(ctx context.Context, reference string, env imagelift.Environment, planOnly bool) error {
	prepared, err := a.PrepareSuiteLift(ctx, reference, env)
	if err != nil {
		return err
	}
	if planOnly {
		return json.NewEncoder(a.Out).Encode(prepared.Summary())
	}
	receipt, deployErr := prepared.Deploy(ctx, func(summary agentruntime.SuitePlanSummary) error {
		a.notef("Suite lift: %s, %d members, plan %s", summary.Suite, len(summary.Members), summary.PlanDigest)
		return a.guardWith("lift AgentSuite", "kmx lift <suite-reference> --environment <file>", env.Namespace, true, false)
	})
	return errors.Join(deployErr, json.NewEncoder(a.Out).Encode(receipt))
}

// PrepareSuiteLift is the typed registry-to-plan operation shared by CLI and TUI.
// The returned plan retains its exact target and rechecks observations on deploy.
func (a *App) PrepareSuiteLift(ctx context.Context, reference string, env imagelift.Environment) (*agentruntime.PreparedSuiteDeployment, error) {
	if err := env.Validate(); err != nil {
		return nil, err
	}
	worker := *a.withRunContext(ctx)
	cfg := *a.Cfg
	cfg.KubeContext, cfg.ContextSource = env.Context, config.SourceFlag
	worker.Cfg = &cfg
	return worker.prepareSuiteLift(ctx, reference, env)
}

func (a *App) prepareSuiteLift(ctx context.Context, reference string, env imagelift.Environment) (*agentruntime.PreparedSuiteDeployment, error) {
	if env.PlainHTTP && !strings.HasPrefix(env.Context, "kind-") {
		return nil, errors.New("plain HTTP image transport is restricted to explicit local kind environments")
	}
	root, err := os.MkdirTemp("", "kmx-suite-lift-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(root)
	pulled, err := suiteoras.PullRegistry(ctx, reference, filepath.Join(root, "suite"), env.PlainHTTP)
	if err != nil {
		return nil, err
	}
	suite, err := agentsuite.ResolveDeploymentSuite(pulled.Path, env.Platform)
	if err != nil {
		return nil, err
	}
	members := suite.Members()
	if len(members) != len(env.Members) {
		return nil, errors.New("environment must bind every suite member exactly once")
	}
	plans := map[string]imagelift.Plan{}
	bindings := map[string]agentruntime.SuiteMemberBinding{}
	for _, member := range members {
		binding, ok := env.Members[member.Agent.ID]
		if !ok {
			return nil, fmt.Errorf("missing member binding: %s", member.Agent.ID)
		}
		image, err := suiteoras.ResolveImageRegistryTransport(ctx, binding.Image, env.Platform, env.PlainHTTP)
		if err != nil {
			return nil, err
		}
		if image.Deployment.SuiteDigest != pulled.Descriptor.Digest.String() || image.Deployment.Agent != member.Agent.ID {
			return nil, errors.New("member image does not belong to selected suite and agent")
		}
		if err := agentsuite.ValidateImageSource(pulled.Path, image.Deployment); err != nil {
			return nil, err
		}
		child := env
		child.Members = nil
		child.Name = binding.Name
		child.Inputs = binding.Inputs
		child.Inference = binding.Inference
		child.Instructions = string(member.Instructions)
		plan, err := imagelift.Render(image.Reference, image.Deployment, child)
		if err != nil {
			return nil, err
		}
		plans[member.Agent.ID] = plan
		bindings[member.Agent.ID] = agentruntime.SuiteMemberBinding{Name: plan.Name, Image: plan.Image}
	}
	request := agentruntime.SuiteDeployRequest{Suite: suite, ArtifactDigest: pulled.Descriptor.Digest.String(), Instance: env.Name, Target: agentruntime.SuiteTarget{Context: env.Context, ClusterUID: env.ClusterUID, Namespace: env.Namespace, Runtime: agentruntime.ID(env.Adapter)}, Bindings: bindings, Reconcile: true}
	prepared, err := agentruntime.PrepareSuiteDeployment(ctx, request, imagelift.SuiteAdapter{Cluster: imageLiftCluster{app: a}, Plans: plans})
	if err != nil {
		return nil, err
	}
	if err := prepared.Inspect(ctx); err != nil {
		return nil, err
	}
	return prepared, nil
}

type imageLiftCluster struct{ app *App }

func (c imageLiftCluster) Call(ctx context.Context, input []byte, args ...string) ([]byte, error) {
	worker := c.app.withRunContext(ctx)
	cmd := worker.Run.Command("kubectl", worker.kubectl(args...)...)
	cmd.Stdin = bytes.NewReader(input)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		// API errors may echo submitted config; keep literal inputs out of errors.
		return nil, fmt.Errorf("kubectl operation failed: %w", err)
	}
	return []byte(strings.TrimSpace(stdout.String())), nil
}

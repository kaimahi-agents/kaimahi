package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentsuite"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/app"
	"github.com/spf13/cobra"
)

func addSuiteWorkspaceCommands(group *cobra.Command, state *commandState) {
	var instructions string
	var contextTokens, outputTokens int
	create := &cobra.Command{Use: "create <directory>", Short: "Create a model-independent HTTP AgentSuite source", Args: cobra.ExactArgs(1)}
	create.Flags().StringVar(&instructions, "instructions", "", "agent instructions")
	create.Flags().IntVar(&contextTokens, "context-tokens", 8192, "minimum inference context tokens")
	create.Flags().IntVar(&outputTokens, "output-tokens", 1024, "minimum inference maximum output tokens")
	_ = create.MarkFlagRequired("instructions")
	create.RunE = func(cmd *cobra.Command, args []string) error {
		return agentsuite.CreateHTTPSource(args[0], agentsuite.CreateRequest{Name: filepath.Base(args[0]), Instructions: instructions, Inference: agentsuite.InferenceRequirements{API: "openai-chat-completions-v1", ContextTokens: contextTokens, OutputTokens: outputTokens}})
	}
	var workspace, registry string
	var plain bool
	var buildOptions app.SuiteBuildOptions
	var attestations bool
	publish := &cobra.Command{Use: "publish <name>", Short: "Build and publish every workspace suite member, reusing matching images", Args: cobra.ExactArgs(1)}
	publish.Flags().StringVar(&workspace, "workspace", "suites", "source workspace directory")
	publish.Flags().StringVar(&registry, "registry", "", "container registry/repository prefix")
	publish.Flags().BoolVar(&plain, "plain-http", false, "anonymous local-development registry HTTP")
	publish.Flags().StringVar(&buildOptions.Builder, "builder", "", "Docker buildx builder")
	publish.Flags().BoolVar(&attestations, "attestations", true, "request SBOM and provenance")
	publish.Flags().BoolVar(&buildOptions.RequireAttestations, "require-attestations", false, "require SBOM and provenance")
	_ = publish.MarkFlagRequired("registry")
	publish.RunE = appRun(state, func(a *app.App) error {
		ctx, stop := signal.NotifyContext(publish.Context(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		buildOptions.DisableAttestations = !attestations
		value, err := a.PublishSuiteWorkspace(ctx, workspace, publish.Flags().Args()[0], registry, plain, buildOptions)
		if err != nil {
			return err
		}
		return json.NewEncoder(a.Out).Encode(value)
	})
	var liftWorkspace, environment string
	var planOnly bool
	deploy := &cobra.Command{Use: "deploy <name>", Short: "Plan and deploy a published workspace suite", Args: cobra.ExactArgs(1)}
	deploy.Flags().StringVar(&liftWorkspace, "workspace", "suites", "source workspace directory")
	deploy.Flags().StringVar(&environment, "environment", "", "deployment environment JSON")
	deploy.Flags().BoolVar(&planOnly, "plan", false, "inspect and print the typed plan without deployment")
	_ = deploy.MarkFlagRequired("environment")
	deploy.RunE = appRun(state, func(a *app.App) error {
		name := deploy.Flags().Args()[0]
		pub, err := app.ReadSuitePublication(liftWorkspace, name)
		if err != nil {
			return err
		}
		if err := app.CheckWorkspacePublication(liftWorkspace, name, pub); err != nil {
			return err
		}
		env, err := app.ReadSuiteEnvironment(environment)
		if err != nil {
			return err
		}
		env, err = app.BindSuitePublication(pub, env)
		if err != nil {
			return err
		}
		prepared, err := a.PrepareSuiteLift(deploy.Context(), pub.Suite, env)
		if err != nil {
			return err
		}
		if planOnly {
			return json.NewEncoder(a.Out).Encode(prepared.Summary())
		}
		if os.Getenv("KAIMAHI_CONFIRM") != env.Context {
			return fmt.Errorf("set KAIMAHI_CONFIRM=%s after reviewing --plan", env.Context)
		}
		receipt, err := a.ApplyWorkspaceLift(deploy.Context(), liftWorkspace, name, pub, env, prepared, prepared.Summary().PlanDigest)
		return errors.Join(err, json.NewEncoder(a.Out).Encode(receipt))
	})
	var runWorkspace, placement, member, prompt, runContext string
	run := &cobra.Command{Use: "run <name>", Short: "Invoke a recorded suite deployment through its authenticated image endpoint", Args: cobra.ExactArgs(1)}
	run.Flags().StringVar(&runWorkspace, "workspace", "suites", "source workspace directory")
	run.Flags().StringVar(&placement, "deployment", "", "environment instance name")
	run.Flags().StringVar(&member, "member", "", "agent member id")
	run.Flags().StringVar(&prompt, "prompt", "", "prompt to send to the deployed agent")
	run.Flags().StringVar(&runContext, "to-context", "", "disambiguate deployments by their recorded context")
	_ = run.MarkFlagRequired("deployment")
	_ = run.MarkFlagRequired("member")
	_ = run.MarkFlagRequired("prompt")
	run.RunE = appRun(state, func(a *app.App) error {
		records, err := app.ReadWorkspaceDeployments(runWorkspace, run.Flags().Args()[0])
		if err != nil {
			return err
		}
		var matches []app.WorkspaceDeployment
		for _, record := range records {
			if record.Environment.Name == placement && (runContext == "" || record.Environment.Context == runContext) {
				matches = append(matches, record)
			}
		}
		if len(matches) != 1 {
			return errors.New("deployment selection is absent or ambiguous; specify its name and --to-context")
		}
		answer, err := a.RunSuiteDeployment(run.Context(), matches[0], member, prompt)
		if err != nil {
			return err
		}
		_, err = fmt.Fprintln(a.Out, answer)
		return err
	})
	group.AddCommand(create, publish, deploy, run)
}

package main

import (
	"fmt"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"unicode"

	"github.com/spf13/cobra"
	"oras.land/oras-go/v2/registry"

	agentsuitecore "github.com/kaimahi-agents/kaimahi/internal/kmx/agentsuite"
	agentkitbuilder "github.com/kaimahi-agents/kaimahi/internal/kmx/agentsuite/agentkit"
	agentsuite "github.com/kaimahi-agents/kaimahi/internal/kmx/agentsuite/oras"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/app"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/version"
)

func newSuiteCommand(state *commandState) *cobra.Command {
	group := &cobra.Command{
		Use:   "suite",
		Short: "Work with portable AgentSuite artifacts",
		Args:  cobra.NoArgs,
		RunE:  func(cmd *cobra.Command, _ []string) error { return cmd.Help() },
	}
	var output string
	validate := &cobra.Command{
		Use:   "validate <path>",
		Short: "Validate a portable AgentSuite artifact",
		Args:  usageArgs(1, 1, "kmx suite validate <path> [-o text|json]"),
	}
	validate.Flags().StringVarP(&output, "output", "o", "text", "output: text|json")
	_ = validate.RegisterFlagCompletionFunc("output", staticCompletion([]string{"text", "json"}))
	validate.RunE = func(cmd *cobra.Command, args []string) error {
		a := &app.App{Out: cmd.OutOrStdout()}
		return a.ValidateSuite(args[0], output)
	}

	var (
		buildAgent               string
		buildPlatform            string
		buildOutput              string
		buildModelURL            string
		buildModelKeyEnv         string
		buildVerbose             bool
		buildBuilder             string
		buildAttestations        bool
		buildRequireAttestations bool
	)
	build := &cobra.Command{
		Use:   "build <directory>",
		Short: "Build one AgentSuite agent as an OCI image-layout tar",
		Long: "Build one AgentSuite agent as an OCI image-layout tar.\n\n" +
			"Docker with the buildx plugin and an OCI-export-capable builder are required. Select a builder with --builder (for example, a docker-container builder); kmx does not create or manage builders. The classic Docker image store does not support OCI export, and disabling attestations does not enable it.\n\n" +
			"SBOM and max-mode provenance attestations are requested by default. A confirmed unsupported-attestation rejection retries without either attestation and warns. Use --require-attestations to fail instead, or --attestations=false to opt out. Missing requested attestations in a successful archive warn; required attestations must be present and bind to the image.\n\n" +
			"The canonical image digest identifies the runnable image manifest. The separate index digest includes attestations when present and may vary per build. SBOM inspection reads directly from the local archive.\n\n" +
			"WARNING: --model-api-key-env accepts the environment variable name, never the API key value.\n\n" +
			"The current implementation treats the build profile's harness image as a monolithic AgentKit adapter and does not yet compose the runtime-base image, so its output is not AgentSuite-conformant.",
		Args: usageArgs(1, 1, "kmx suite build <directory> --agent <id> --platform <platform> --model-base-url <url> --output <file>"),
	}
	build.Flags().StringVar(&buildAgent, "agent", "", "agent id (optional only when the suite contains one agent)")
	build.Flags().StringVar(&buildPlatform, "platform", "", "exact platform (optional only when the agent has one composition)")
	build.Flags().StringVar(&buildOutput, "output", "", "new OCI image-layout tar path")
	build.Flags().StringVar(&buildModelURL, "model-base-url", "", "OpenAI-compatible model endpoint embedded by the experimental AgentKit adapter")
	build.Flags().StringVar(&buildModelKeyEnv, "model-api-key-env", "", "environment variable name containing the model API key; never pass the key value")
	build.Flags().BoolVar(&buildVerbose, "verbose", false, "show Docker buildx progress")
	build.Flags().StringVar(&buildBuilder, "builder", "", "Docker buildx builder name (otherwise preserve buildx selection)")
	build.Flags().BoolVar(&buildAttestations, "attestations", true, "request SBOM and max-mode provenance; use --attestations=false to opt out")
	build.Flags().BoolVar(&buildRequireAttestations, "require-attestations", false, "fail if the builder cannot attest or the archive lacks verified SBOM and provenance")
	_ = build.MarkFlagRequired("output")
	_ = build.MarkFlagRequired("model-base-url")
	_ = build.MarkFlagFilename("output")
	_ = build.RegisterFlagCompletionFunc("platform", staticCompletion([]string{"linux/amd64", "linux/arm64"}))
	build.RunE = func(cmd *cobra.Command, args []string) error {
		if err := agentkitbuilder.ValidateModelAPIKeyEnv(buildModelKeyEnv); err != nil {
			return err
		}
		if cmd.Flags().Changed("builder") && strings.TrimSpace(buildBuilder) == "" {
			return fmt.Errorf("--builder cannot be empty")
		}
		if buildRequireAttestations && !buildAttestations {
			return fmt.Errorf("--require-attestations cannot be used with --attestations=false")
		}
		builder := state.deps.newAgentKitBuilder(agentkitbuilder.Options{
			ModelBaseURL:        buildModelURL,
			ModelAPIKeyEnv:      buildModelKeyEnv,
			Verbose:             buildVerbose,
			Progress:            cmd.ErrOrStderr(),
			Builder:             buildBuilder,
			DisableAttestations: !buildAttestations,
			RequireAttestations: buildRequireAttestations,
		})
		a := &app.App{Out: cmd.OutOrStdout(), Err: cmd.ErrOrStderr()}
		ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		result, err := a.BuildSuite(ctx, args[0], buildOutput, agentsuitecore.BuildSelection{
			Agent: buildAgent, Platform: buildPlatform,
		}, builder)
		if err != nil {
			return err
		}
		for _, warning := range result.Warnings {
			if _, err := fmt.Fprintf(cmd.ErrOrStderr(), "Warning: %s\n", warning); err != nil {
				return err
			}
		}
		indexLabel := "Index digest (no attestations)"
		if result.HasAttestations {
			indexLabel = "Index digest (includes attestations; may vary per build)"
		}
		if _, err := fmt.Fprintf(cmd.OutOrStdout(), "Built AgentSuite agent %s for %s to %s (%s)\nImage digest: %s\n%s: %s\n",
			result.Agent, result.Platform, result.Path, result.MediaType, result.Digest, indexLabel, result.IndexDigest); err != nil {
			return err
		}
		if result.SBOMDigest != "" {
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "Read SBOM statement: tar -xOf %s blobs/sha256/%s\n", quoteShell(result.Path), strings.TrimPrefix(result.SBOMDigest, "sha256:"))
		} else {
			_, err = fmt.Fprintln(cmd.OutOrStdout(), "SBOM: not present in archive")
		}
		return err
	}

	var target string
	var pushPlainHTTP, pushForce, pushProvenance, pushRequireProvenance bool
	push := &cobra.Command{
		Use:   "push <directory> <reference>",
		Short: "Push an extracted AgentSuite to an OCI layout or registry",
		Long: "Push an extracted AgentSuite to an OCI registry, or use --to-layout for a local OCI image layout.\n\n" +
			"Registry authentication is read from the standard Docker credential store for HTTPS and loopback HTTP.\n" +
			"With --plain-http, non-loopback registries are unauthenticated; auth challenges are refused.\n" +
			"Credentials are never sent over HTTP to non-loopback registries or token endpoints.\n\n" +
			"An existing registry tag with the same digest is a no-op, except that provenance is attached if none is visible;\n" +
			"replacing a different digest requires --force. The tag preflight check is NOT ATOMIC against concurrent pushers.\n\n" +
			"A registry push attaches unsigned SLSA provenance as an OCI referrer of the suite manifest before moving the tag.\n" +
			"It records the kmx version and, when every packed file matches HEAD, the Git commit and path, so anyone can\n" +
			"check out, repack and compare digests. It is a claim, not a signature: anyone with push access can attach one.\n" +
			"Sign the suite digest where you push, for example in CI. An attach failure is a warning unless\n" +
			"--require-provenance is set. Registries without the referrers API also get a sha256-<digest> index tag.\n" +
			"Local layouts get none: a conformant AgentSuite layout holds exactly one manifest.",
		Example: "  kmx suite push ./suite registry.example.com/team:v1\n" +
			"  kmx suite push ./suite --to-layout ./layout agentsuites/team:v1",
		Args: usageArgs(2, 2, "kmx suite push <directory> [--to-layout <layout>] [--plain-http] [--force] [--provenance=false] [--require-provenance] <reference>"),
	}
	push.Flags().StringVar(&target, "to-layout", "", "local OCI image-layout target directory")
	push.Flags().BoolVar(&pushPlainHTTP, "plain-http", false, "use HTTP instead of HTTPS for a registry target")
	push.Flags().BoolVar(&pushForce, "force", false, "allow replacing a registry tag with a different digest (non-atomic preflight)")
	push.Flags().BoolVar(&pushProvenance, "provenance", true, "attach an unsigned provenance referrer to a registry push")
	push.Flags().BoolVar(&pushRequireProvenance, "require-provenance", false, "fail without moving the tag if provenance cannot be attached")
	_ = push.MarkFlagDirname("to-layout")
	push.RunE = func(cmd *cobra.Command, args []string) error {
		a := &app.App{Out: cmd.OutOrStdout()}
		var result agentsuite.PushResult
		var err error
		if pushRequireProvenance && !pushProvenance {
			return fmt.Errorf("--require-provenance cannot be used with --provenance=false")
		}
		provenance := app.SuitePushProvenance{Enabled: pushProvenance, Require: pushRequireProvenance}
		if pushProvenance {
			info, ok := state.deps.buildInfo()
			if builderVersion := version.Resolve(info, ok).Version; builderVersion != version.Unknown {
				provenance.BuilderVersion = builderVersion
			}
		}
		if cmd.Flags().Changed("to-layout") {
			if target == "" {
				return fmt.Errorf("--to-layout cannot be empty")
			}
			if pushPlainHTTP {
				return fmt.Errorf("--plain-http cannot be used with --to-layout")
			}
			if pushForce {
				return fmt.Errorf("--force cannot be used with --to-layout")
			}
			if cmd.Flags().Changed("provenance") || pushRequireProvenance {
				return fmt.Errorf("--provenance and --require-provenance cannot be used with --to-layout")
			}
			result, err = a.PushSuite(cmd.Context(), args[0], target, args[1])
		} else {
			result, err = a.PushSuiteRegistry(cmd.Context(), args[0], args[1], pushPlainHTTP, pushForce, provenance)
		}
		if err != nil {
			return err
		}
		if target == "" {
			if _, err := fmt.Fprintf(
				cmd.OutOrStdout(),
				"Pushed AgentSuite %s to %s (%s)\n",
				result.Report.Name,
				result.Reference,
				result.Descriptor.Digest,
			); err != nil {
				return err
			}
			return printPushedProvenance(cmd, result)
		}
		if result.Updated {
			if _, err := fmt.Fprintf(
				cmd.OutOrStdout(),
				"Updated existing local OCI layout %s; unrelated references were preserved\n",
				result.Path,
			); err != nil {
				return err
			}
		} else {
			if _, err := fmt.Fprintf(cmd.OutOrStdout(), "Created local OCI layout %s\n", result.Path); err != nil {
				return err
			}
		}
		if _, err := fmt.Fprintf(
			cmd.OutOrStdout(),
			"Pushed AgentSuite %s to %s as %s (%s)\n",
			result.Report.Name,
			result.Path,
			result.Reference,
			result.Descriptor.Digest,
		); err != nil {
			return err
		}
		return nil
	}

	var pullSource, pullOutput string
	var pullPlainHTTP bool
	pull := &cobra.Command{
		Use:   "pull <reference>",
		Short: "Pull and extract an AgentSuite from an OCI layout or registry",
		Long: "Pull and extract an AgentSuite from an OCI registry, or use --from-layout for a local OCI image layout.\n\n" +
			"Registry authentication is read from the standard Docker credential store for HTTPS and loopback HTTP.\n" +
			"With --plain-http, non-loopback registries are unauthenticated; auth challenges are refused.\n" +
			"Credentials are never sent over HTTP to non-loopback registries or token endpoints.\n\n" +
			"A registry pull also lists kmx provenance referrers that name the pulled digest. They are unsigned\n" +
			"claims, shown for information; anyone with push access can attach one, and none blocks a pull.",
		Example: "  kmx suite pull registry.example.com/team:v1 --output ./suite\n" +
			"  kmx suite pull agentsuites/team:v1 --from-layout ./layout --output ./suite",
		Args: usageArgs(1, 1, "kmx suite pull <reference> [--from-layout <layout>] [--plain-http] --output <directory>"),
	}
	pull.Flags().StringVar(&pullSource, "from-layout", "", "local OCI image-layout source directory")
	pull.Flags().StringVar(&pullOutput, "output", "", "extracted AgentSuite output directory")
	pull.Flags().BoolVar(&pullPlainHTTP, "plain-http", false, "use HTTP instead of HTTPS for a registry source")
	_ = pull.MarkFlagRequired("output")
	_ = pull.MarkFlagDirname("from-layout")
	_ = pull.MarkFlagDirname("output")
	pull.RunE = func(cmd *cobra.Command, args []string) error {
		a := &app.App{Out: cmd.OutOrStdout()}
		var result agentsuite.PullResult
		var err error
		source := args[0]
		if cmd.Flags().Changed("from-layout") {
			if pullSource == "" {
				return fmt.Errorf("--from-layout cannot be empty")
			}
			if pullPlainHTTP {
				return fmt.Errorf("--plain-http cannot be used with --from-layout")
			}
			result, err = a.PullSuite(cmd.Context(), pullSource, args[0], pullOutput)
			source = pullSource
		} else {
			result, err = a.PullSuiteRegistry(cmd.Context(), args[0], pullOutput, pullPlainHTTP)
		}
		if err != nil {
			return err
		}
		_, err = fmt.Fprintf(
			cmd.OutOrStdout(),
			"Pulled and extracted AgentSuite %s from %s as %s to %s (%s)\n",
			result.Report.Name,
			source,
			result.Reference,
			result.Path,
			result.Descriptor.Digest,
		)
		if err != nil {
			return err
		}
		if !cmd.Flags().Changed("from-layout") {
			if err := printPulledProvenance(cmd, result); err != nil {
				return err
			}
			parsed, err := registry.ParseReference(args[0])
			if err != nil {
				return err
			}
			if parsed.ValidateReferenceAsTag() == nil {
				_, err = fmt.Fprintf(cmd.OutOrStdout(), "Pin this AgentSuite: %s/%s@%s\n", parsed.Registry, parsed.Repository, result.Descriptor.Digest)
				return err
			}
		}
		return nil
	}
	group.AddCommand(build, pull, push, validate)
	return group
}

func printPushedProvenance(cmd *cobra.Command, result agentsuite.PushResult) error {
	for _, warning := range result.Warnings {
		if _, err := fmt.Fprintf(cmd.ErrOrStderr(), "Warning: %s\n", terminalLine(warning)); err != nil {
			return err
		}
	}
	if result.Provenance == nil {
		return nil
	}
	_, err := fmt.Fprintf(cmd.OutOrStdout(), "Provenance: %s claims %s (unsigned)\n", result.Provenance.Digest, provenanceClaims(*result.Provenance))
	return err
}

func printPulledProvenance(cmd *cobra.Command, result agentsuite.PullResult) error {
	for _, warning := range result.Warnings {
		if _, err := fmt.Fprintf(cmd.ErrOrStderr(), "Warning: %s\n", terminalLine(warning)); err != nil {
			return err
		}
	}
	if !result.ProvenanceChecked {
		_, err := fmt.Fprintln(cmd.OutOrStdout(), "Provenance: not checked")
		return err
	}
	if len(result.Provenance) == 0 {
		_, err := fmt.Fprintln(cmd.OutOrStdout(), "Provenance: none")
		return err
	}
	for _, provenance := range result.Provenance {
		if _, err := fmt.Fprintf(cmd.OutOrStdout(), "Provenance: %s claims %s (unsigned)\n", provenance.Digest, provenanceClaims(provenance)); err != nil {
			return err
		}
	}
	return nil
}

// provenanceClaims prints only fields verification already checked.
func provenanceClaims(provenance agentsuite.Provenance) string {
	claims := "kmx"
	if provenance.BuilderVersion != "" {
		claims += " " + provenance.BuilderVersion
	}
	if provenance.SourceCommit == "" {
		return claims + ", no source commit"
	}
	claims += ", source commit " + provenance.SourceCommit
	if provenance.SourcePath != "" {
		claims += " path " + strconv.Quote(provenance.SourcePath)
	}
	if provenance.SourceURI != "" {
		claims += " in " + provenance.SourceURI
	}
	return claims
}

// terminalLine makes registry error text a single safe line.
func terminalLine(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return ' '
		}
		return r
	}, s)
}

package main

import (
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/app"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/config"
)

func newEvalCleanupCommand(state *commandState, verb string) *cobra.Command {
	var destination, workspace, target, namespace string
	cmd := &cobra.Command{
		Use: "cleanup [full-run-id]", Short: "List candidate leftovers or confirm cleanup of one exact run",
		Long:              "Without a run ID, report candidates read-only. With one full 32-character lowercase hex run ID, preview target and exact root UIDs, then require context-naming confirmation even on local kind (KAIMAHI_CONFIRM=CONTEXT for automation). This may interrupt an active run; liveness is unknown. Only identity-safe Provider/Agent/Task roots are deleted with foreground GC. Unanchored descendants or incomplete inventories refuse. Runtime/model logs are not erased.\nRequires explicit --to or --to-context; no evalTarget/current/default fallback. Named targets copy only context, namespace and runtime declaration, ignoring inference and bindings. --workspace locates kmx.yaml only with --to; explicit context needs no workspace/source.",
		Example:           "  kmx test cleanup --to dev\n  kmx test cleanup <full-run-id> --to dev",
		PersistentPreRunE: validateContainerEngineFlag,
		Args: func(cmd *cobra.Command, args []string) error {
			if err := usageArgs(0, 1, "kmx "+verb+" cleanup [full-run-id] --to NAME | --to-context CONTEXT")(cmd, args); err != nil {
				return err
			}
			if len(args) == 1 && (len(args[0]) != 32 || strings.Trim(args[0], "0123456789abcdef") != "") {
				return fmt.Errorf("cleanup requires a full 32-character lowercase hex run ID")
			}
			return nil
		},
		PreRunE: func(cmd *cobra.Command, _ []string) error {
			if err := validateContainerEngineFlag(cmd, nil); err != nil {
				return err
			}
			if state.contextFlag != "" || cmd.Flags().Changed("context") {
				return fmt.Errorf("cleanup refuses --context; use --to-context")
			}
			if cmd.Flags().Changed("container-engine") {
				return fmt.Errorf("cleanup refuses irrelevant --container-engine")
			}
			for _, name := range []string{"to", "workspace", "to-context", "to-namespace"} {
				if cmd.Flags().Changed(name) && strings.TrimSpace(cmd.Flag(name).Value.String()) == "" {
					return fmt.Errorf("cleanup requires non-empty --%s", name)
				}
			}
			if destination == "" && target == "" {
				return fmt.Errorf("cleanup requires --to or --to-context")
			}
			if cmd.Flags().Changed("workspace") && destination == "" {
				return fmt.Errorf("cleanup --workspace requires named --to")
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			selectedContext, selectedNamespace, source := target, namespace, config.SourceFlag
			if destination != "" {
				_, doc, err := resolveDestinations(workspace)
				if err != nil {
					return err
				}
				saved, err := doc.Target(destination)
				if err != nil {
					return err
				}
				if saved.Runtime != "" && saved.Runtime != "orka" {
					return fmt.Errorf("candidate cleanup is Orka-only")
				}
				if !cmd.Flags().Changed("to-context") {
					selectedContext = saved.Context
					source = "kmx.yaml (--to " + destination + ")"
				}
				if !cmd.Flags().Changed("to-namespace") && saved.Namespace != "" {
					selectedNamespace = saved.Namespace
				}
			}
			if _, err := fmt.Fprintf(cmd.ErrOrStderr(), "Cleanup target: context=%q namespace=%q chosen-by=%q\n", selectedContext, selectedNamespace, source); err != nil {
				return err
			}
			selected := *state
			selected.contextFlag = selectedContext
			a, err := selected.operationApplication(cmd)
			if err != nil {
				return err
			}
			a.TestPresentation = verb == "test"
			a.Cfg.ContextSource = source
			// Never synthesize root --context in recovery advice for this route.
			parts := []string{quoteShell(state.executable()), verb, "cleanup"}
			if len(args) == 1 {
				parts = append(parts, args[0])
			}
			parts = append(parts, "--to-context", quoteShell(selectedContext), "--to-namespace", quoteShell(selectedNamespace))
			a.InvocationCommand = strings.Join(parts, " ")
			opt := app.SuiteCandidateCleanupOptions{Namespace: selectedNamespace}
			if len(args) == 1 {
				opt.RunID = args[0]
			}
			return a.CleanupSuiteCandidates(ctx, opt)
		},
	}
	registerDestinationFlag(cmd, &destination)
	cmd.Flags().StringVar(&workspace, "workspace", "", "config locator, only with named --to")
	cmd.Flags().StringVar(&target, "to-context", "", "explicit candidate cleanup context")
	cmd.Flags().StringVar(&namespace, "to-namespace", app.OrkaNamespace, "candidate cleanup namespace")
	_ = cmd.MarkFlagDirname("workspace")
	return cmd
}

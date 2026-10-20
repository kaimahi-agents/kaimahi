package main

import (
	"errors"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/app"
)

func newSuiteEvalVerifyCommand(state *commandState) *cobra.Command {
	var opt app.SuiteEvaluationVerifyOptions
	cmd := &cobra.Command{
		Use:   "verify --suite <layout-or-digest> --evidence <file> --expect <file>",
		Short: "Verify portable evaluation evidence offline as a CI gate",
		Long: "Verify unsigned evaluation evidence offline against a local single-manifest OCI layout and a complete, independently reviewed ExpectedEvaluation JSON file. No configuration, workspace, KMX state/cache, model, cluster or registry is accessed.\n\n" +
			"Use --suite DIR for layout input, or --suite sha256:<digest> --from-layout DIR to pin the local bytes explicitly. A digest alone cannot supply artifact bytes. No registry references or cache lookup are supported.\n\n" +
			"Commit a reviewed expected.json beside the suite in CI; never copy expectations from the evidence being checked or regenerate the policy to make a failing gate pass. All expected subject, execution, evaluator and judge policy fields are required.\n\n" +
			"Success exits zero and prints a text or JSON verified summary. Refusals exit one with a stable code and safe explanation on stderr; JSON is success-only. Verification establishes unsigned consistency, not authenticity, execution truth, freshness or deployment readiness. Private push/Deploy gates are unchanged.",
		Example: "  kmx eval verify --suite ./suite-layout --evidence ./evidence.json --expect ./expected.json\n" +
			"  kmx eval verify --suite sha256:<digest> --from-layout ./suite-layout --evidence ./evidence.json --expect ./expected.json -o json",
		Args: func(_ *cobra.Command, args []string) error {
			if len(args) != 0 {
				return errors.New("eval verify accepts no positional references")
			}
			return nil
		},
		PersistentPreRunE: func(cmd *cobra.Command, _ []string) error {
			if flag := cmd.Flag("context"); state.contextFlag != "" || flag != nil && flag.Changed {
				return errors.New("--context cannot be used with eval verify")
			}
			if flag := cmd.Flag("container-engine"); flag != nil && flag.Changed {
				return errors.New("eval verify does not use --container-engine")
			}
			for _, name := range []string{"suite", "from-layout", "evidence", "expect", "output"} {
				if flag := cmd.Flag(name); flag.Changed && strings.TrimSpace(flag.Value.String()) == "" {
					return fmt.Errorf("eval verify requires non-empty --%s", name)
				}
			}
			if opt.Output != "text" && opt.Output != "json" {
				return errors.New("eval verify --output must be text or json")
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, _ []string) error {
			a := &app.App{Out: cmd.OutOrStdout(), Err: cmd.ErrOrStderr()}
			return a.VerifySuiteEvaluation(cmd.Context(), opt)
		},
	}
	cmd.Flags().StringVar(&opt.Suite, "suite", "", "local single-manifest OCI layout, or sha256 digest with --from-layout")
	cmd.Flags().StringVar(&opt.FromLayout, "from-layout", "", "explicit local layout for digest-form --suite (no lookup)")
	cmd.Flags().StringVar(&opt.Evidence, "evidence", "", "detached portable evaluation evidence JSON file")
	cmd.Flags().StringVar(&opt.Expect, "expect", "", "reviewed complete ExpectedEvaluation JSON, independent of evidence")
	cmd.Flags().StringVarP(&opt.Output, "output", "o", "text", "report format: text|json (JSON success-only)")
	for _, name := range []string{"suite", "evidence", "expect"} {
		_ = cmd.MarkFlagRequired(name)
	}
	_ = cmd.MarkFlagDirname("suite")
	_ = cmd.MarkFlagDirname("from-layout")
	_ = cmd.MarkFlagFilename("evidence", "json")
	_ = cmd.MarkFlagFilename("expect", "json")
	_ = cmd.RegisterFlagCompletionFunc("output", staticCompletion([]string{"text", "json"}))
	return cmd
}

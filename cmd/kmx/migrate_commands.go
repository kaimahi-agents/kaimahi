package main

import (
	"strings"

	"github.com/spf13/cobra"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/app"
)

// newMigrateCommand is a top-level verb rather than a member of a family,
// and deliberately: `tools` and `models` are seams an operator onboards
// endpoints to, while this is one journey with one end state — an
// application that was talking to a model endpoint directly is talking to
// a governed seam instead, and nothing about it was rebuilt.
func newMigrateCommand(state *commandState) *cobra.Command {
	var opt app.MigrateOptions
	cmd := &cobra.Command{
		Use:   "migrate <deployment>",
		Short: "Put an application you did not write onto a governed model seam",
		Args: usageArgs(1, 1,
			"kmx migrate <deployment> --namespace <namespace> --model <provider>/<model>"),
	}
	cmd.Flags().StringVar(&opt.Namespace, "namespace", "", "namespace YOUR application runs in (required)")
	cmd.Flags().StringVar(&opt.Container, "container", "", "container that talks to a model (needed only when there are several)")
	cmd.Flags().StringVar(&opt.Model, "model", "", "model name the endpoint resolves, e.g. local/qwen2.5:3b (required)")
	cmd.Flags().StringVar(&opt.Upstream, "upstream", "",
		strings.Join(app.MigrateUpstreams, "|")+" — the second is Orka's default behaviour, for measuring it")
	cmd.Flags().StringVar(&opt.Credential, "credential", "", "governed credential to issue (default: the deployment's name)")
	cmd.Flags().StringVar(&opt.Secret, "secret", "", "Secret the credential is stored in, in your namespace")
	cmd.Flags().StringVar(&opt.OrkaNamespace, "orka-namespace", "", "namespace Orka runs in")
	cmd.Flags().StringVar(&opt.ServiceAccount, "service-account", "", "ServiceAccount the seam presents to Orka")
	cmd.Flags().StringVar(&opt.TokenDuration, "token-duration", "", "lifetime asked of the API server for that token")
	cmd.Flags().StringVar(&opt.BaseURLVar, "base-url-var", "", "your application's variable for the model base URL")
	cmd.Flags().StringVar(&opt.KeyVar, "key-var", "", "your application's variable for the model credential")
	cmd.Flags().StringVar(&opt.ModelVar, "model-var", "", "your application's variable for the model name")
	cmd.Flags().StringVar(&opt.Out, "out", "", "manifest output path")
	cmd.Flags().BoolVar(&opt.NoApply, "no-apply", false, "write and stop")
	cmd.Flags().BoolVar(&opt.DryRun, "dry-run", false, "server-side validation only")
	_ = cmd.RegisterFlagCompletionFunc("upstream", staticCompletion(app.MigrateUpstreams))
	cmd.RunE = appRun(state, func(a *app.App) error {
		opt.Deployment = cmd.Flags().Arg(0)
		return a.Migrate(opt)
	})
	return cmd
}

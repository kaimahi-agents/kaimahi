// Package app orchestrates kmx commands using resolved configuration,
// context guards, runtime adapters and external tools.
package app

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"

	kaimahi "github.com/kaimahi-agents/kaimahi"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentpreview"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/config"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/guard"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/orkaschema"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/run"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/toolchain"
)

type progressPresenter interface {
	Phase(string) string
	Success(string) string
	Failure(string) string
	Heading(string) string
	Warning(string) string
	Accent(string) string
	Muted(string) string
	Info(string) string
}

func (a *App) testCommandVerb() string {
	if a.TestPresentation {
		return "test"
	}
	return "eval"
}

// App carries the resolved configuration and the streams every command
// writes to.
type App struct {
	// FileRunner is the embedded preview dependency; nil refuses file execution.
	FileRunner      agentpreview.Runner
	chatClusterName string
	// kagentProfile is invocation-local; nil keeps the exact create profile.
	kagentProfile *kagentProfile
	// operationProgress reports real stage transitions to an owning deployment UI.
	operationProgress func(name, status string, err error)
	Cfg               *config.Config
	Run               *run.Runner
	Out               io.Writer
	// Executable is the CLI path that launched this App. Generated follow-up
	// commands retain it so a checkout binary cannot redirect to an older kmx
	// earlier on PATH. Empty keeps the installed-command spelling "kmx".
	Executable string
	// DeployPresentation selects current CLI stage labels only. Historical JSON,
	// plan identities and consent-bound data remain unchanged; zero preserves Lift.
	DeployPresentation bool
	// TestPresentation selects canonical test recovery advice only; zero keeps
	// the legacy eval spelling. Evidence and execution are unaffected.
	TestPresentation bool
	// InvocationCommand is the shell-quoted CLI invocation used for guard retry
	// advice. Interactive sub-operations leave it empty and supply their own.
	InvocationCommand string
	chatVerbose       bool
	chatInference     string
	copilotCLI        string
	copilotModel      string
	foundryClient     *foundryChatClient
	Err               io.Writer
	Stdin             *os.File
	// now is injectable so progress timing can be tested without sleeping.
	now func() time.Time
	// captureCommand replaces execution of an already-prepared kubectl capture
	// in tests only. Nil retains the real subprocess boundary.
	captureCommand func(context.Context, *exec.Cmd) error
	// compileOrkaSchema replaces installed-schema compilation in tests only.
	// Nil compiles each fresh observed CRD set without caching.
	compileOrkaSchema func(map[string][]byte) (*orkaschema.Validator, error)
	// releaseRecordPrepared configures the real private publisher in boundary tests.
	// Nil leaves persistence unchanged.
	releaseRecordPrepared func(*releaseLiftRecordWriter)
	// waitTiming overrides retry pacing and phase deadlines in tests only.
	// Nil preserves every production interval and timeout.
	waitTiming *waitTiming
	// progressUI replaces destination detection in tests only. Production uses
	// cliui.New against Err so styling follows the actual output stream.
	progressUI progressPresenter
	// localModels replaces provider discovery and terminal selection in tests.
	// Nil uses the bounded loopback detectors and real terminal streams.
	localModels *localModelEnvironment
	// selectedLocalModel is non-nil only when this invocation elected to reuse
	// a model that was already installed on the host.
	selectedLocalModel *localModel
	localModelsChecked bool
	localModelVerified bool

	// provisioned records the cluster tools this run had to fetch, so a
	// command that reports structured output can say what it put on the
	// machine.
	provisioned []toolchain.Tool

	// guarded records that the context guard has already run in this
	// process, so a multi-step command asks at most once — the same
	// once-per-invocation behaviour make gives the `guard` prerequisite.
	guarded     bool
	liftConsent *workspaceLiftConsent

	// orkaInstaller and orkaInstallerDigest point `kmx orka install` at a
	// test origin serving bytes a test can pin. Both are empty in every
	// real invocation, which is what keeps the shipped pin the only one:
	// no flag, environment variable or file can move it.
	orkaInstaller       string
	orkaInstallerDigest string
	// Set only while preparing the Orka result forward, so cancellation and
	// signals kill a forward even before its bound-port announcement.
	orkaForwardContext context.Context

	// quickstartResultPort moves quickstart's loopback result forward off the
	// fixed port so a test can own a listener. Empty in every real
	// invocation, where the fixed port is the only one.
	quickstartResultPort string
	// Only this invocation's successful opaque placement may authorize its answer.
	quickstartPrepared  *preparedQuickstartSuite
	quickstartPlacement *ReleaseLiftResult
}

// New builds an App around the process's own streams.
func New(cfg *config.Config) *App {
	r := run.Default()
	r.Env = cfg.KindEnv()
	r.Unset = cfg.KindUnset()
	return &App{Cfg: cfg, Run: r, Out: os.Stdout, Err: os.Stderr, Stdin: os.Stdin, TestPresentation: true}
}

// operationContext is shared by commands, network requests, and retry waits.
func (a *App) operationContext() context.Context {
	if a.Run != nil && a.Run.Context != nil {
		return a.Run.Context
	}
	return context.Background()
}

// withRunContext returns a copy of this App whose Runner is bound to ctx, so
// a caller that supplied one cancels the child commands this copy runs.
//
// A copy, rather than rebinding a.Run.Context in place: the Runner is shared
// by every caller holding this App, and one entry point's context must not
// silently become the deadline of a command someone else started. It is the
// same shallow App+Runner copy appAtAgentLocation already makes to aim a
// subset of reads somewhere else without moving the original.
func (a *App) withRunContext(ctx context.Context) *App {
	if a.Run == nil || a.Run.Context == ctx {
		return a
	}
	app := *a
	runner := *a.Run
	runner.Context = ctx
	app.Run = &runner
	return &app
}

func (a *App) installedOrkaSchema(crds map[string][]byte) (*orkaschema.Validator, error) {
	if a.compileOrkaSchema != nil {
		return a.compileOrkaSchema(crds)
	}
	return orkaschema.Installed(crds)
}

func (a *App) executeCapture(ctx context.Context, cmd *exec.Cmd) error {
	if a.captureCommand != nil {
		return a.captureCommand(ctx, cmd)
	}
	return cmd.Run()
}

// kubectl returns a kubectl argument list carrying the explicit --context.
//
// An explicit context prevents a command from following kubeconfig's mutable
// current-context, which tools such as `az aks get-credentials` can change.
// Reads and writes built with this helper stay on the selected target.
func (a *App) kubectl(args ...string) []string {
	return append([]string{"--context", a.Cfg.KubeContext}, args...)
}

func (a *App) kubectlRun(args ...string) error {
	return a.Run.Run("kubectl", a.kubectl(args...)...)
}

func (a *App) kubectlCapture(args ...string) (string, error) {
	return a.Run.Capture("kubectl", a.kubectl(args...)...)
}

func (a *App) kubectlQuiet(args ...string) bool {
	return a.Run.Quiet("kubectl", a.kubectl(args...)...)
}

// isNotFound reports whether a kubectl failure was "the object is not there",
// as opposed to "the cluster could not be reached" or "you may not read it".
//
// The distinction is what lets a read be TOLERANT without becoming blind. A
// fresh cluster legitimately has no namespace or Secret yet, but an
// unreachable API server or an RBAC denial must never be read as "absent".
func isNotFound(err error) bool {
	if err == nil {
		return false
	}
	message := err.Error()
	return strings.Contains(message, "NotFound") || strings.Contains(message, `" not found`)
}

// Command prepares a subprocess using the same explicit kubectl context and
// Runner environment as the other cluster reads and writes.
func (a *App) Command(args ...string) *exec.Cmd {
	prepared := a.Run.Command("kubectl", a.kubectl(args...)...)
	if a.orkaForwardContext == nil {
		return prepared
	}
	cmd := exec.CommandContext(a.orkaForwardContext, prepared.Path, prepared.Args[1:]...)
	cmd.Env = prepared.Env
	return cmd
}

// kubeconfig reads the merged kubeconfig, saying plainly when the reason it
// cannot is that kubectl is not installed. On a fresh machine the missing tool
// is the whole story, and "cannot read the kubeconfig — refusing to act blind"
// would otherwise read as something being wrong with the cluster. Every path
// that classifies a context goes through here, `kmx context show` included.
func (a *App) kubeconfig() (*guard.Kubeconfig, error) {
	if err := run.MustExist("kubectl", "to read the kubeconfig and reach the cluster",
		"https://kubernetes.io/docs/tasks/tools/"); err != nil {
		return nil, err
	}
	cfg, err := guard.LoadKubeconfig("kubectl")
	if err != nil {
		return nil, fmt.Errorf("kube-guard: %w", err)
	}
	return cfg, nil
}

// Guard prints where the action will land and refuses anything that is not a
// local kind cluster without explicit confirmation. It runs at most once per
// process.
func (a *App) Guard(action, command string) error {
	return a.guardWith(action, command, config.GuardNamespaceHint, false, false)
}

// GuardCreate is Guard for bring-up commands that create or repair their
// exact kind context before performing cluster work.
func (a *App) GuardCreate(action, command string) error {
	return a.guardWith(action, command, config.GuardNamespaceHint, false, true)
}

// GuardCreateIn is GuardCreate for a caller that knows exactly which
// namespaces it writes to, and so can say so instead of printing the
// generic list. The banner's claim is where the action lands; naming
// namespaces the command never touches weakens it, and on the Orka path it
// advertises a runtime that path does not install.
func (a *App) GuardCreateIn(action, command, namespaces string) error {
	return a.guardWith(action, command, namespaces, false, true)
}

// GuardKnown is Guard for an action that must not take the "about to be
// created" allowance: the kubeconfig has to actually describe the cluster,
// or the operator confirms it by name. `kmx local down` is the caller — see the
// reasoning there and on guard.Request.MustBeKnown.
func (a *App) GuardKnown(action, command string) error {
	return a.guardWith(action, command, config.GuardNamespaceHint, true, false)
}

func (a *App) guardWith(action, command, namespaces string, mustBeKnown, createsContext bool) error {
	if a.guarded {
		return nil
	}
	if a.InvocationCommand != "" {
		command = a.InvocationCommand
	}
	cfg, err := a.kubeconfig()
	if err != nil {
		return err
	}
	if err := guard.Check(cfg, guard.Request{
		Action:         action,
		Context:        a.Cfg.KubeContext,
		Source:         a.Cfg.ContextSource,
		Namespaces:     namespaces,
		Confirm:        a.Cfg.Confirm,
		Command:        command,
		MustBeKnown:    mustBeKnown,
		CreatesContext: createsContext,
	}, a.Err, a.Stdin); err != nil {
		return err
	}
	a.guarded = true
	return nil
}

// manifest returns one of the embedded k8s manifests.
func manifest(name string) ([]byte, error) {
	b, err := kaimahi.Manifests.ReadFile("k8s/" + name)
	if err != nil {
		return nil, fmt.Errorf("kmx was built without k8s/%s: %w", name, err)
	}
	return b, nil
}

// apply pipes an embedded manifest into `kubectl apply -f -`. kmx is
// installed with `go install` and run outside a clone, so there is no file on
// disk to point kubectl at; the manifests travel inside the binary.
func (a *App) apply(name string) error {
	body, err := manifest(name)
	if err != nil {
		return err
	}
	return a.applyBytes(name, body)
}

func (a *App) applyBytes(name string, body []byte) error {
	fmt.Fprintf(a.Err, "kubectl --context %s apply -f - # (embedded k8s/%s)\n", a.Cfg.KubeContext, name)
	quiet := *a.Run
	quiet.Echo = false
	return quiet.RunStdin(body, "kubectl", a.kubectl("apply", "-f", "-")...)
}

// notef prints an operator-facing note on stderr, where the Makefile's
// `echo ... >&2` notes go.
func (a *App) notef(format string, args ...any) {
	fmt.Fprintf(a.Err, format+"\n", args...)
}

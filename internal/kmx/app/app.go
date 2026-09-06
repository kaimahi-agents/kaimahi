// Package app implements kmx's commands.
//
// The Makefile's kind-path recipes are the specification for everything
// here: every wait, every fail-closed check and every message is carried
// across, and where a comment in the Makefile explains WHY a wait exists,
// that reasoning is repeated at the Go code that replaced it. The Makefile
// now delegates to this package, so there is one implementation and CI keeps
// proving the code a developer actually runs.
package app

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"time"

	kaimahi "github.com/kaimahi-agents/kaimahi"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/config"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/guard"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/run"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/toolchain"
)

// App carries the resolved configuration and the streams every command
// writes to.
type App struct {
	Cfg *config.Config
	Run *run.Runner
	Out io.Writer
	// chatJSON forces raw A2A JSON from `agent chat` on a terminal.
	chatJSON bool
	Err      io.Writer
	Stdin    *os.File
	// now is injectable so progress timing can be tested without sleeping.
	now func() time.Time

	// provisioned records the cluster tools this run had to fetch, so a
	// command that reports structured output can say what it put on the
	// machine.
	provisioned []toolchain.Tool

	// guarded records that the context guard has already run in this
	// process, so a multi-step command asks at most once — the same
	// once-per-invocation behaviour make gives the `guard` prerequisite.
	guarded bool
}

// New builds an App around the process's own streams.
func New(cfg *config.Config) *App {
	r := run.Default()
	r.Env = cfg.KindEnv()
	return &App{Cfg: cfg, Run: r, Out: os.Stdout, Err: os.Stderr, Stdin: os.Stdin}
}

// kubectl returns a kubectl argument list carrying the explicit --context.
//
// Every read and every write kmx makes goes through this. The Makefile's
// $(KUBECTL) does the same thing for the same reason: a bare kubectl follows
// `current-context`, which `az aks get-credentials` rewrites silently, so a
// command meant for kind can quietly aim at a managed cluster.
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

// Capture and Command make App an admin.Kube: the admin plumbing reaches the
// cluster through the SAME kubectl every other read and write here uses,
// carrying the same explicit --context. It cannot be aimed anywhere else.
func (a *App) Capture(args ...string) (string, error) { return a.kubectlCapture(args...) }

func (a *App) Command(args ...string) *exec.Cmd {
	return a.Run.Command("kubectl", a.kubectl(args...)...)
}

// kubeconfig reads the merged kubeconfig, saying plainly when the reason it
// cannot is that kubectl is not installed. On a fresh machine the missing tool
// is the whole story, and "cannot read the kubeconfig — refusing to act blind"
// would otherwise read as something being wrong with the cluster. Every path
// that classifies a context goes through here, `kmx ctx` included.
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
	if a.guarded {
		return nil
	}
	cfg, err := a.kubeconfig()
	if err != nil {
		return err
	}
	if err := guard.Check(cfg, guard.Request{
		Action:     action,
		Context:    a.Cfg.KubeContext,
		Source:     a.Cfg.ContextSource,
		Namespaces: config.GuardNamespaces,
		Confirm:    a.Cfg.Confirm,
		Command:    command,
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

package app

import (
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/config"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/toolchain"
)

type dependency struct {
	name, why, install string
	probe              []string
	// fetchable marks a tool kmx can download, pinned and checksum-verified,
	// when the machine does not have it (internal/kmx/toolchain).
	fetchable bool
}

var (
	depKubectl = dependency{"kubectl", "to read and write Kubernetes resources", "https://kubernetes.io/docs/tasks/tools/", []string{"version", "--client"}, true}
	depKind    = dependency{"kind", "to manage the local Kubernetes cluster", "https://kind.sigs.k8s.io/docs/user/quick-start/#installation", []string{"version"}, true}
	depHelm    = dependency{"helm", "to install kagent", "https://helm.sh/docs/intro/install/", []string{"version"}, true}
	depBash    = dependency{"bash", "to run the embedded scripts used by this lift phase", "https://www.gnu.org/software/bash/", []string{"--version"}, false}
	depPython3 = dependency{"python3", "to render JSON safely in the embedded boundary probe", "https://www.python.org/downloads/", []string{"--version"}, false}
	depCurl    = dependency{"curl", "to query Azure Managed Prometheus during verification", "https://curl.se/download.html", []string{"--version"}, false}
	// Go is deliberately NOT fetchable. It is a toolchain and a directory
	// tree rather than one binary, and only the commands that BUILD the
	// governance plane need it — `kmx plane`, and `kmx lift`, which builds
	// it for a managed cluster. The first answer does not (the fast path
	// is ungoverned by design). Fetching it is the obvious next
	// prerequisite to kill, and it is not killed yet.
	depGo = dependency{"go", "to fetch and build the governance plane", "https://go.dev/dl/", []string{"version"}, false}
)

func (a *App) engineDependency() dependency {
	install := "https://docs.docker.com/get-docker/"
	if a.Cfg.ContainerEngine == "podman" {
		install = "https://podman.io/docs/installation"
	}
	return dependency{a.Cfg.ContainerEngine, "as the selected kind container engine", install, []string{"info"}, false}
}

// preflight makes every declared dependency usable, and reports every
// remaining problem in one response. No command should use a dependency
// before this returns nil.
//
// "Makes usable" is the part that matters once the front door is `curl |
// sh` and one command. A missing kind, kubectl
// or Helm used to be the end of the run: four install pages, and a first
// agent that was four downloads away from someone who just wanted to see one
// answer. kmx already knew how to fetch ONE of the tools it shells out to —
// the pinned kagent CLI, checksum-verified into a cache directory — so the
// rest are fetched the same way, by the same rules, and what the operator
// already has on PATH still wins.
//
// The container engine stays a genuine prerequisite: it is a daemon and a
// privileged system package, not a binary that can be dropped into a cache.
func (a *App) preflight(dependencies ...dependency) error {
	if err := a.provision(dependencies); err != nil {
		return err
	}
	seen := map[string]bool{}
	var problems []string
	for _, dep := range dependencies {
		if seen[dep.name] {
			continue
		}
		seen[dep.name] = true
		path, err := exec.LookPath(dep.name)
		if err != nil {
			problems = append(problems, fmt.Sprintf("%s is not on PATH — kmx needs it %s.\n  install: %s", dep.name, dep.why, dep.install))
			continue
		}
		if len(dep.probe) == 0 {
			continue
		}
		if _, err := a.Run.Capture(path, dep.probe...); err != nil {
			problems = append(problems, fmt.Sprintf("%s was found at %s but is unusable: %v\n  install: %s", dep.name, path, err, dep.install))
		}
	}
	if len(problems) == 0 {
		return nil
	}
	noun := "dependency"
	if len(problems) != 1 {
		noun = "dependencies"
	}
	return fmt.Errorf("preflight: %d missing or unusable %s:\n\n- %s", len(problems), noun, strings.Join(problems, "\n- "))
}

// noFetch is the opt-out. Set KMX_TOOLCHAIN=off and a missing tool is once
// again an error naming its install page — for an operator who would rather
// their machine only ran binaries their own package manager put there.
// toolchainBase overrides the download origin. Only the tests set it: a unit
// test must never reach a real release server, and one that did would pass or
// fail on somebody else's uptime.
var toolchainBase string

func noFetch() bool { return strings.EqualFold(strings.TrimSpace(os.Getenv("KMX_TOOLCHAIN")), "off") }

// provision fetches the pinned copy of any declared dependency that kmx knows
// how to fetch and the machine does not already have, then puts it on PATH
// for the rest of this process.
//
// PATH, specifically, rather than absolute paths at every call site: every
// command in this package is echoed to the operator's terminal so it can be
// copied and run by hand (internal/kmx/run), and a line reading
// `/home/you/.config/kmx/bin/kubectl-1.37.0-linux-amd64 get pods` is not a
// line anybody copies. It also has to be the PROCESS environment rather than
// the Runner's: exec.Command resolves the binary against the parent's PATH,
// not against the child's Env.
func (a *App) provision(dependencies []dependency) error {
	if noFetch() {
		return nil
	}
	var wanted []string
	seen := map[string]bool{}
	for _, dep := range dependencies {
		if seen[dep.name] || !dep.fetchable {
			continue
		}
		seen[dep.name] = true
		if _, err := exec.LookPath(dep.name); err != nil {
			wanted = append(wanted, dep.name)
		}
	}
	if len(wanted) == 0 {
		return nil
	}
	cache, err := config.CacheDir()
	if err != nil {
		return err
	}
	linkDir, err := config.ToolchainDir()
	if err != nil {
		return err
	}
	tools, err := toolchain.Provision(wanted, linkDir, toolchain.Options{CacheDir: cache, Log: a.Err, BaseOverride: toolchainBase})
	if err != nil {
		return fmt.Errorf("%w\n  kmx could not fetch a tool it needs. Install it yourself and put it on PATH, or set KMX_TOOLCHAIN=off to be told rather than helped", err)
	}
	// A command can preflight more than once (a step, then a lane). Record
	// each tool once, or the structured output lists it twice.
	for _, tool := range tools {
		known := false
		for _, seen := range a.provisioned {
			if seen.Name == tool.Name {
				known = true
				break
			}
		}
		if !known {
			a.provisioned = append(a.provisioned, tool)
		}
	}
	return os.Setenv("PATH", toolchain.PrependPath(os.Getenv("PATH"), linkDir))
}

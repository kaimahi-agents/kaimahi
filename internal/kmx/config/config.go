// Package config resolves kmx's settings.
//
// Every knob keeps the name this repository already uses — KIND_CLUSTER,
// KUBE_CTX, CONTAINER_ENGINE, MODEL, CHAT_PORT and KAIMAHI_CONFIRM from
// the Makefile, so delegating targets pass nothing: an
// operator's `KIND_CLUSTER=mine make up` and their `KIND_CLUSTER=mine kmx up`
// are the same run. Where the Makefile has a default, that default is
// repeated here verbatim; the two are pinned together by a test.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Pinned versions and defaults. These are the Makefile's, and
// TestDefaultsMatchTheMakefile refuses to let them drift.
const (
	DefaultKindCluster = "kaimahi-p1"
	DefaultModel       = "qwen2.5:3b"
	DefaultChatPort    = "auto"
	DefaultTask        = "Hello! Who are you and where are you running?"
	// GuardNamespaces names the common fixed namespaces. Some operations also
	// write to a caller-selected workload namespace; the generic banner must
	// not present this list as a complete destination set. Known Orka-path
	// destinations still get their exact list from app.GuardCreateIn.
	GuardNamespaces        = "ollama, orka-system"
	GuardNamespaceHint     = GuardNamespaces + " (common, not exhaustive; see action for other namespaces)"
	DefaultContainerEngine = "docker"
)

// Where a kube context came from. These are printed, so they read as
// answers to "who chose this cluster?" rather than as identifiers.
//
// SourceDefault is the one that is not an answer. It means nothing named a
// cluster and kmx fell back to a name it made up, and the guard treats it
// differently for exactly that reason: every other source is somebody's
// decision, and this one is nobody's.
const (
	SourceFlag        = "--context"
	SourceKubeCtx     = "KUBE_CTX"
	SourceSelected    = "kmx ctx"
	SourceKindCluster = "KIND_CLUSTER"
	SourceDefault     = "default"
)

// Config is the resolved run configuration.
type Config struct {
	KindCluster     string
	KubeContext     string
	ContainerEngine string
	Model           string
	// ModelExplicit distinguishes an operator's MODEL choice from the default,
	// so interactive discovery never replaces a value automation supplied.
	ModelExplicit bool
	ChatPort      string
	Confirm       string
	// ContextSource records where KubeContext came from, for the banner.
	ContextSource string
}

func env(name, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(name)); v != "" {
		return v
	}
	return fallback
}

// Load resolves configuration from the environment and the selected-context
// file, with an optional --context override taking precedence over both.
//
// Context resolution order, most explicit first:
//  1. --context on the command line
//  2. KUBE_CTX in the environment (what the Makefile exports)
//  3. the context selected by `kmx ctx <name>`
//  4. kind-<KIND_CLUSTER>, when KIND_CLUSTER is in the environment
//  5. kind-kaimahi-p1, the bare default — which nobody chose
//
// The fallback is deliberately a kind-* name: the guard admits an absent
// kind-* context as "about to be created", so a fresh machine works with no
// setup, and any other unset-and-wrong case is refused as a typo.
//
// 4 and 5 produce the same name on a machine with no KIND_CLUSTER, and they
// are still different facts. 4 is a cluster the operator named; 5 is one kmx
// invented because nothing else was available. Labelling both `KIND_CLUSTER`
// made the invention indistinguishable from a choice, which is what let a
// command land on a cluster nobody had picked without saying so.
//
// Note what is NOT consulted: the kubeconfig's `current-context`. kmx pins an
// explicit context on every call for the reason kubectl() states — a bare
// kubectl follows current-context, and `az aks get-credentials` rewrites that
// silently, so a command meant for kind can quietly aim at a managed cluster.
// Following it here would swap an invented target for one another tool
// picked. The guard names it instead, and refuses.
func Load(contextFlag string) (*Config, error) {
	return LoadWithOverrides(contextFlag, "")
}

// LoadWithOverrides resolves configuration with CLI values taking precedence
// over environment values. Applying the engine here, rather than after Load,
// lets a valid --container-engine recover from a stale or invalid
// CONTAINER_ENGINE value exactly as an ordinary CLI override should.
func LoadWithOverrides(contextFlag, containerEngineFlag string) (*Config, error) {
	model, modelExplicit := os.LookupEnv("MODEL")
	model = strings.TrimSpace(model)
	if model == "" {
		model, modelExplicit = DefaultModel, false
	}
	c := &Config{
		KindCluster:     env("KIND_CLUSTER", DefaultKindCluster),
		ContainerEngine: env("CONTAINER_ENGINE", DefaultContainerEngine),
		Model:           model,
		ModelExplicit:   modelExplicit,
		ChatPort:        env("CHAT_PORT", DefaultChatPort),
		Confirm:         os.Getenv("KAIMAHI_CONFIRM"),
	}
	engine := c.ContainerEngine
	if containerEngineFlag != "" {
		engine = containerEngineFlag
	}
	if err := c.SetContainerEngine(engine); err != nil {
		if containerEngineFlag != "" {
			return nil, fmt.Errorf("unknown --container-engine %q — expected docker or podman", containerEngineFlag)
		}
		return nil, fmt.Errorf("unknown CONTAINER_ENGINE %q — expected 'docker' or 'podman'", engine)
	}

	switch {
	case strings.TrimSpace(contextFlag) != "":
		c.KubeContext, c.ContextSource = strings.TrimSpace(contextFlag), SourceFlag
	case strings.TrimSpace(os.Getenv("KUBE_CTX")) != "":
		c.KubeContext, c.ContextSource = strings.TrimSpace(os.Getenv("KUBE_CTX")), SourceKubeCtx
	default:
		if selected, err := ReadSelectedContext(); err != nil {
			return nil, err
		} else if selected != "" {
			c.KubeContext, c.ContextSource = selected, SourceSelected
		} else if strings.TrimSpace(os.Getenv("KIND_CLUSTER")) != "" {
			c.KubeContext, c.ContextSource = "kind-"+c.KindCluster, SourceKindCluster
		} else {
			c.KubeContext, c.ContextSource = "kind-"+c.KindCluster, SourceDefault
		}
	}
	return c, nil
}

// SetContainerEngine validates and applies one resolved engine choice. Both
// the environment and CLI paths use it instead of maintaining separate lists.
func (c *Config) SetContainerEngine(engine string) error {
	engine = strings.TrimSpace(engine)
	switch engine {
	case "docker", "podman":
		c.ContainerEngine = engine
		return nil
	default:
		return fmt.Errorf("unknown container engine %q — expected docker or podman", engine)
	}
}

// KindEnv returns the environment kind needs for the selected engine. kind
// talks to podman only when KIND_EXPERIMENTAL_PROVIDER says so, so the two
// are set together and can never disagree — a cluster created under one
// engine is invisible to the other, which otherwise reads as "kind is
// broken".
func (c *Config) KindEnv() []string {
	if c.ContainerEngine == "podman" {
		return []string{"KIND_EXPERIMENTAL_PROVIDER=podman"}
	}
	return nil
}

// KindUnset removes inherited provider selection before KindEnv applies the
// chosen engine. Docker is kind's default only when the variable is absent;
// Podman needs exactly its own value. Clearing first avoids either explicit
// choice inheriting a contradictory shell setting; Runner additions win over
// removals, so Podman is then added back deliberately.
func (c *Config) KindUnset() []string {
	return []string{"KIND_EXPERIMENTAL_PROVIDER"}
}

// StateDir is where kmx keeps its local state and the binaries kmx had to
// fetch. Without an override it follows os.UserConfigDir: the XDG config home
// on Linux and Library/Application Support on macOS. KMX_HOME is the explicit
// cross-platform override for tests and isolated invocations; app packages
// should use this function rather than interpreting OS-specific variables.
func StateDir() (string, error) {
	if home := strings.TrimSpace(os.Getenv("KMX_HOME")); home != "" {
		return home, nil
	}
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("cannot locate a config directory (set KMX_HOME): %w", err)
	}
	return filepath.Join(dir, "kmx"), nil
}

func contextFile() (string, error) {
	dir, err := StateDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "context"), nil
}

// CacheDir is where a fetched, checksum-verified binary is cached, so a kmx
// installed with `go install` (no clone, no bin/) still has somewhere to put
// it.
func CacheDir() (string, error) {
	dir, err := StateDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "bin"), nil
}

// ToolchainDir is where kmx puts a plain-named symlink for each cluster tool
// it had to fetch itself (kind, kubectl). It is prepended to PATH for the
// life of one command.
//
// It cannot be CacheDir: entries there carry their version in the name, so a
// pin bump can never be served the previous binary, and nothing looks for a
// command under `kind-0.33.0-linux-amd64`.
func ToolchainDir() (string, error) {
	dir, err := StateDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "path"), nil
}

// LiftRecordDir is where a managed-cluster run records what it created in
// somebody's subscription.
//
// It is deliberately kmx's own state directory rather than anywhere in a
// checkout. The record carries resource ids, which carry a subscription id,
// and this repository refuses to hold one; a file written into a working tree
// is a file that eventually gets committed. It also has to outlive the run: a
// lift that finishes on Monday is torn down on Tuesday by a different
// process, and the only safe way to remove what it made is to read back the
// ids it recorded when it made them.
func LiftRecordDir() (string, error) {
	dir, err := StateDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "lift"), nil
}

// ReadSelectedContext returns the context chosen by `kmx ctx`, or "".
func ReadSelectedContext() (string, error) {
	path, err := contextFile()
	if err != nil {
		return "", err
	}
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("cannot read the selected context at %s: %w", path, err)
	}
	return strings.TrimSpace(string(b)), nil
}

// WriteSelectedContext records the context `kmx ctx` selected.
func WriteSelectedContext(context string) (string, error) {
	path, err := contextFile()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", err
	}
	if err := os.WriteFile(path, []byte(context+"\n"), 0o600); err != nil {
		return "", err
	}
	return path, nil
}

// Package config resolves kmx's settings.
//
// Every knob keeps the name this repository already uses — KIND_CLUSTER,
// KUBE_CTX, CONTAINER_ENGINE, KAGENT_VERSION, MODEL, CHAT_PORT, CRED and
// KAIMAHI_CONFIRM from the Makefile, ADMIN_PORT from
// scripts/plane-admin.sh — so that the delegating make targets need to pass
// nothing: an operator's `KIND_CLUSTER=mine make up` and their
// `KIND_CLUSTER=mine kmx up` are the same run. Where the Makefile has a
// default, that default is repeated here verbatim; the two are pinned
// together by a test.
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
	DefaultKindCluster   = "kaimahi-p1"
	DefaultKagentVersion = "0.9.12"
	DefaultModel         = "qwen2.5:3b"
	DefaultChatPort      = "auto"
	// DefaultAdminPort is the local side of the plane's admin port-forward —
	// scripts/plane-admin.sh's ADMIN_PORT, so a stale forward left by either
	// implementation is noticed by the other rather than talked through.
	DefaultAdminPort = "19091"
	// DefaultOpsPort is the local side of the metrics forward —
	// scripts/plane-metrics.sh's OPS_PORT, for the same reason ADMIN_PORT
	// is shared: a stale forward from either implementation is noticed.
	DefaultOpsPort     = "19092"
	DefaultAgent       = "hello-world"
	DefaultTask        = "Hello! Who are you and where are you running?"
	DefaultNamespace   = "kagent"
	KeylessModelConfig = "hello-world-model"
	// DefaultCredential is the Makefile's CRED: the credential `govern`
	// issues and the ledger is read for by default.
	DefaultCredential = "hello-world"
	// DefaultToolsCredential is the Makefile's CRED_TOOLS: the credential
	// the MCP gateway admits the tools agent by.
	DefaultToolsCredential = "hello-tools"
	// DefaultTools is the Makefile's TOOLS — the gateway allowlist
	// `kmx tools govern` sets, and the agent's tool selection.
	DefaultTools = "k8s_get_resources"
	// DefaultToolsAgent is the agent `kmx tools govern` puts behind the
	// gateway, and DefaultToolsSecret the agent-side Secret its kmh_ token
	// is stored in (the Makefile's GOVERNED_SECRET for that target).
	DefaultToolsAgent  = "hello-tools"
	DefaultToolsSecret = "kaimahi-tools-token"
	// DefaultToolServer is the committed governed seam — the one
	// RemoteMCPServer kmx carries and applies (k8s/kaimahi-tools.yaml).
	// `kmx tools govern --server` names another, scaffolded by
	// `kmx tools add`.
	DefaultToolServer = "kaimahi-tools"
	// GovernedSecret is the agent-side Secret the issued token is stored in
	// (the Makefile's GOVERNED_SECRET default), in the kagent namespace.
	GovernedSecret         = "kaimahi-governed-token"
	GovernedModelConfig    = "governed-ollama"
	GuardNamespaces        = "kagent, kaimahi, ollama"
	DefaultContainerEngine = "docker"
)

// Config is the resolved run configuration.
type Config struct {
	KindCluster     string
	KubeContext     string
	ContainerEngine string
	KagentVersion   string
	Model           string
	ChatPort        string
	AdminPort       string
	OpsPort         string
	Credential      string
	// ToolsCredential is the Makefile's CRED_TOOLS.
	ToolsCredential string
	Confirm         string
	// KagentBin, when set, is an existing kagent binary to use instead of
	// the cached download. The Makefile points it at bin/kagent so a
	// checkout keeps one copy.
	KagentBin string
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
//  4. kind-<KIND_CLUSTER>, the Makefile's own default
//
// The fallback is deliberately a kind-* name: the guard admits an absent
// kind-* context as "about to be created", so a fresh machine works with no
// setup, and any other unset-and-wrong case is refused as a typo.
func Load(contextFlag string) (*Config, error) {
	c := &Config{
		KindCluster:     env("KIND_CLUSTER", DefaultKindCluster),
		ContainerEngine: env("CONTAINER_ENGINE", DefaultContainerEngine),
		KagentVersion:   env("KAGENT_VERSION", DefaultKagentVersion),
		Model:           env("MODEL", DefaultModel),
		ChatPort:        env("CHAT_PORT", DefaultChatPort),
		AdminPort:       env("ADMIN_PORT", DefaultAdminPort),
		OpsPort:         env("OPS_PORT", DefaultOpsPort),
		Credential:      env("CRED", DefaultCredential),
		ToolsCredential: env("CRED_TOOLS", DefaultToolsCredential),
		Confirm:         os.Getenv("KAIMAHI_CONFIRM"),
		KagentBin:       strings.TrimSpace(os.Getenv("KAGENT")),
	}
	switch c.ContainerEngine {
	case "docker", "podman":
	default:
		return nil, fmt.Errorf("unknown CONTAINER_ENGINE %q — expected 'docker' or 'podman'", c.ContainerEngine)
	}

	switch {
	case strings.TrimSpace(contextFlag) != "":
		c.KubeContext, c.ContextSource = strings.TrimSpace(contextFlag), "--context"
	case strings.TrimSpace(os.Getenv("KUBE_CTX")) != "":
		c.KubeContext, c.ContextSource = strings.TrimSpace(os.Getenv("KUBE_CTX")), "KUBE_CTX"
	default:
		if selected, err := ReadSelectedContext(); err != nil {
			return nil, err
		} else if selected != "" {
			c.KubeContext, c.ContextSource = selected, "kmx ctx"
		} else {
			c.KubeContext, c.ContextSource = "kind-"+c.KindCluster, "KIND_CLUSTER"
		}
	}
	return c, nil
}

// KindEnv returns the environment kind needs for the selected engine. kind
// talks to podman only when KIND_EXPERIMENTAL_PROVIDER says so, so the two
// are set together and can never disagree — a cluster created under one
// engine is invisible to the other, which otherwise reads as "kind is
// broken" (the rule PR #42 introduced).
func (c *Config) KindEnv() []string {
	if c.ContainerEngine == "podman" {
		return []string{"KIND_EXPERIMENTAL_PROVIDER=podman"}
	}
	return nil
}

// stateDir is where kmx keeps the selected context and the cached kagent
// binary. Overridable with KMX_HOME, which is what the tests use.
func stateDir() (string, error) {
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
	dir, err := stateDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "context"), nil
}

// CacheDir is where the pinned kagent binary is cached, so a kmx installed
// with `go install` (no clone, no bin/) still has somewhere to put it.
func CacheDir() (string, error) {
	dir, err := stateDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "bin"), nil
}

// ToolchainDir is where kmx puts a plain-named symlink for each cluster tool
// it had to fetch itself (kind, kubectl, Helm). It is prepended to PATH for
// the life of one command.
//
// It cannot be CacheDir: entries there carry their version in the name, so a
// pin bump can never be served the previous binary, and nothing looks for a
// command under `kind-0.33.0-linux-amd64`.
func ToolchainDir() (string, error) {
	dir, err := stateDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "path"), nil
}

// PlaneCacheDir is where the proxy binary built for the plane's image is put
// on the clone-free path. It is kmx's own directory rather than the
// operator's GOBIN, so building the plane never lands a binary on top of
// something they installed themselves.
func (c *Config) PlaneCacheDir() (string, error) {
	dir, err := stateDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "plane-bin"), nil
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

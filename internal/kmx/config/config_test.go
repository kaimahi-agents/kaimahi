package config

import (
	"testing"
)

func TestProductDefaultsAreUsable(t *testing.T) {
	for name, value := range map[string]string{
		"kind cluster": DefaultKindCluster, "kagent version": DefaultKagentVersion,
		"model": DefaultModel, "agent": DefaultAgent, "task": DefaultTask,
		"container engine": DefaultContainerEngine,
	} {
		if value == "" {
			t.Errorf("%s default is empty", name)
		}
	}
}

// Context resolution is the difference between acting on the cluster the
// operator meant and acting on whatever was left in the environment, so
// assert the whole precedence chain.
func TestContextResolutionOrder(t *testing.T) {
	home := t.TempDir()
	t.Setenv("KMX_HOME", home)
	t.Setenv("KIND_CLUSTER", "mine")
	t.Setenv("KUBE_CTX", "")
	t.Setenv("CONTAINER_ENGINE", "")

	// 4. KIND_CLUSTER names the cluster: kind-<KIND_CLUSTER>, and the source
	// says a variable chose it.
	c, err := Load("")
	if err != nil {
		t.Fatal(err)
	}
	if c.KubeContext != "kind-mine" || c.ContextSource != SourceKindCluster {
		t.Errorf("KIND_CLUSTER: got %q from %q, want kind-mine from KIND_CLUSTER", c.KubeContext, c.ContextSource)
	}

	// 3. a context selected by `kmx ctx` beats the default.
	if _, err := WriteSelectedContext("kind-selected"); err != nil {
		t.Fatal(err)
	}
	if c, err = Load(""); err != nil {
		t.Fatal(err)
	}
	if c.KubeContext != "kind-selected" || c.ContextSource != "kmx ctx" {
		t.Errorf("selected: got %q from %q", c.KubeContext, c.ContextSource)
	}

	// 2. KUBE_CTX beats the selection for environment-driven automation.
	t.Setenv("KUBE_CTX", "kind-fromenv")
	if c, err = Load(""); err != nil {
		t.Fatal(err)
	}
	if c.KubeContext != "kind-fromenv" || c.ContextSource != "KUBE_CTX" {
		t.Errorf("env: got %q from %q", c.KubeContext, c.ContextSource)
	}

	// 1. --context beats everything.
	if c, err = Load("kind-flag"); err != nil {
		t.Fatal(err)
	}
	if c.KubeContext != "kind-flag" || c.ContextSource != SourceFlag {
		t.Errorf("flag: got %q from %q", c.KubeContext, c.ContextSource)
	}
}

// The case that let kmx act on a cluster nobody picked: with nothing set at
// all the name is still kind-kaimahi-p1, but it is an invention, and the
// source has to say so. Labelling it KIND_CLUSTER made a made-up target
// indistinguishable from one an operator typed.
func TestAnUnchosenContextSaysItWasNotChosen(t *testing.T) {
	t.Setenv("KMX_HOME", t.TempDir())
	t.Setenv("KIND_CLUSTER", "")
	t.Setenv("KUBE_CTX", "")
	t.Setenv("CONTAINER_ENGINE", "")

	c, err := Load("")
	if err != nil {
		t.Fatal(err)
	}
	if c.KubeContext != "kind-"+DefaultKindCluster {
		t.Errorf("default context: got %q, want kind-%s", c.KubeContext, DefaultKindCluster)
	}
	if c.ContextSource != SourceDefault {
		t.Errorf("default source: got %q, want %q — an invented target must not read as a choice",
			c.ContextSource, SourceDefault)
	}
}

// An unknown engine is refused rather than defaulted: silently falling back
// to docker would create a cluster the
// operator's kind cannot see.
func TestUnknownContainerEngineIsRefused(t *testing.T) {
	t.Setenv("KMX_HOME", t.TempDir())
	t.Setenv("CONTAINER_ENGINE", "containerd")
	if _, err := Load(""); err == nil {
		t.Fatal("an unknown CONTAINER_ENGINE must be refused")
	}
}

func TestPodmanCarriesTheKindProvider(t *testing.T) {
	t.Setenv("KMX_HOME", t.TempDir())
	t.Setenv("CONTAINER_ENGINE", "podman")
	c, err := Load("")
	if err != nil {
		t.Fatal(err)
	}
	env := c.KindEnv()
	if len(env) != 1 || env[0] != "KIND_EXPERIMENTAL_PROVIDER=podman" {
		t.Errorf("podman must carry KIND_EXPERIMENTAL_PROVIDER, got %v", env)
	}
	t.Setenv("CONTAINER_ENGINE", "docker")
	if c, err = Load(""); err != nil {
		t.Fatal(err)
	}
	if len(c.KindEnv()) != 0 {
		t.Errorf("docker must set no kind provider, got %v", c.KindEnv())
	}
}

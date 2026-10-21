package agentsessions

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"os"
	"regexp"
	"strings"
	"testing"

	v1 "github.com/agent-primitives/sessions/api/genpb"
	"github.com/agent-primitives/sessions/canon"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/runtime"
	"google.golang.org/protobuf/encoding/protojson"
)

var updateGolden = flag.Bool("update", false, "rewrite render golden files")

const renderTestImage = "registry.example.invalid/agentsessions/harnessnode@sha256:2c26b46b68ffc68ff99b453c1d30413413422d706483bfa0f98a5e886266e7ae"

func renderTestBinding(t *testing.T, path string) runtime.AgentSessionsBinding {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	b, err := runtime.ParseAgentSessionsBinding(raw)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func renderTestInput(t *testing.T) RenderInput {
	return RenderInput{Suite: "support-desk", Member: "triage", Model: "fixture-model", HarnessImage: renderTestImage, Binding: renderTestBinding(t, "../runtime/testdata/agentsessions-binding-microvm.json")}
}

// The golden files are full plan-only renders: the POC's micro-VM binding (with
// the model section the kind adds) and a gVisor variant with VOLUMES fidelity
// and no worker selector. scripts/validate-agentsessions-render.sh feeds each
// actorTemplate through substrate's protojson decoding and CreateActorTemplate
// validation at SubstrateAPICommit.
func TestRenderGolden(t *testing.T) {
	for _, tc := range []struct {
		golden string
		edit   func(*RenderInput)
	}{
		{"testdata/render-microvm.golden.json", func(*RenderInput) {}},
		{"testdata/render-gvisor.golden.json", func(in *RenderInput) {
			in.Binding.Sandbox = runtime.AgentSessionsSandboxBinding{Class: "gvisor", ConfigName: "gvisor"}
			in.Binding.Snapshot.Fidelity = "VOLUMES"
			in.Binding.WorkerSelector = runtime.AgentSessionsWorkerSelectorBinding{}
		}},
	} {
		t.Run(tc.golden, func(t *testing.T) {
			in := renderTestInput(t)
			tc.edit(&in)
			rendered, err := Render(in)
			if err != nil {
				t.Fatal(err)
			}
			got, err := json.MarshalIndent(rendered, "", "  ")
			if err != nil {
				t.Fatal(err)
			}
			got = append(got, '\n')
			if *updateGolden {
				if err := os.WriteFile(tc.golden, got, 0o644); err != nil {
					t.Fatal(err)
				}
			}
			want, err := os.ReadFile(tc.golden)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(got, want) {
				t.Fatalf("render changed (run with -update to accept):\ngot:\n%s\nwant:\n%s", got, want)
			}
		})
	}
}

func TestRenderPortsPOCRules(t *testing.T) {
	in := renderTestInput(t)
	rendered, err := Render(in)
	if err != nil {
		t.Fatal(err)
	}
	bindingDigest, err := runtime.AgentSessionsBindingDigest(in.Binding)
	if err != nil || rendered.BindingDigest != bindingDigest {
		t.Fatal("binding digest not recorded", err)
	}
	sum := sha256.Sum256([]byte("kmx.agentsessions-actortemplate/v2\n" + renderTestImage + "\n" + bindingDigest))
	wantName := "support-desk-triage-" + hex.EncodeToString(sum[:])[:8]
	tmpl := rendered.ActorTemplate
	if tmpl.Metadata.Name != wantName || tmpl.Metadata.Atespace != "agentsuite-demo" {
		t.Fatalf("template identity = %+v, want %s", tmpl.Metadata, wantName)
	}
	if tmpl.SandboxConfig != (ActorSandboxConfig{SandboxClass: "microvm", ConfigName: "microvm"}) {
		t.Fatalf("sandbox = %+v", tmpl.SandboxConfig)
	}
	if tmpl.SnapshotConfig != (ActorSnapshotConfig{StorageLocation: "gs://ate-snapshots/agentsuite-demo/" + wantName + "/", PreferredFidelity: "SNAPSHOT_FIDELITY_MEMORY"}) {
		t.Fatalf("snapshot = %+v", tmpl.SnapshotConfig)
	}
	if tmpl.WorkerSelector == nil || tmpl.WorkerSelector.MatchLabels["workload"] != "agentsuite-demo-microvm" {
		t.Fatalf("worker selector = %+v", tmpl.WorkerSelector)
	}
	c := tmpl.Containers[0]
	if len(tmpl.Containers) != 1 || c.Image != renderTestImage || c.WakeupProbe != (ActorWakeupProbe{HTTPGet: ActorHTTPGet{Path: "/readyz", Port: 8081}, TimeoutSeconds: 120}) {
		t.Fatalf("container = %+v", c)
	}
	if len(tmpl.Resources.Limits) != 2 || tmpl.Resources.Limits[0] != (ActorLimit{"cpu", "1"}) || tmpl.Resources.Limits[1] != (ActorLimit{"memory", "512Mi"}) {
		t.Fatalf("template resources = %+v", tmpl.Resources)
	}
	env := map[string]string{}
	for _, e := range c.Env {
		env[e.Name] = e.Value
	}
	if len(env) != 4 || env["HARNESS_KIND"] != "chat" || env["HARNESS_MODEL"] != "fixture-model" || env["HARNESS_ADDR"] != ":80" || env["HARNESS_READYZ"] != ":8081" {
		t.Fatalf("env = %v", env)
	}
	// No model credential reference, endpoint or provider reaches the template.
	raw, err := json.Marshal(tmpl)
	if err != nil {
		t.Fatal(err)
	}
	for _, leaked := range []string{"model-credentials", "api-key", "fakemodel", "openai", "secret"} {
		if strings.Contains(string(raw), leaked) {
			t.Fatalf("template carries host model configuration %q: %s", leaked, raw)
		}
	}
	if ref := rendered.HostModel.Credential.SecretRef; ref == nil || ref.Name != "model-credentials" || ref.Key != "api-key" || rendered.HostModel.Name != "fixture-model" {
		t.Fatalf("host model references lost: %+v", rendered.HostModel)
	}
}

func TestRenderRegistrationIsStrictProtoJSONWithHostSpecDigest(t *testing.T) {
	rendered, err := Render(renderTestInput(t))
	if err != nil {
		t.Fatal(err)
	}
	var reg v1.HarnessRegistration
	if err := (protojson.UnmarshalOptions{}).Unmarshal(rendered.HarnessRegistration, &reg); err != nil {
		t.Fatal(err)
	}
	sub := reg.GetSpec().GetSubstrate()
	if reg.GetMetadata().GetName() != "triage" || sub.GetAtespace() != "agentsuite-demo" || sub.GetTemplate() != rendered.ActorTemplate.Metadata.Name ||
		reg.GetSpec().GetDescriptorId() != "chat" || reg.GetSpec().GetCapabilities().GetResumability() != v1.Resumability_RESUMABILITY_STATELESS_REPLAY {
		t.Fatalf("registration = %v", &reg)
	}
	c, err := canon.Proto(reg.GetSpec())
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(c)
	if rendered.RegistrationSpecDigest != "sha256:"+hex.EncodeToString(sum[:]) {
		t.Fatal("registration spec digest is not the host's canonical spec digest")
	}
}

func TestRenderGVisorBindingAndOptionalSections(t *testing.T) {
	in := renderTestInput(t)
	in.Binding.Sandbox.Class, in.Binding.Sandbox.ConfigName = "gvisor", "gvisor-default"
	in.Binding.Resources = runtime.AgentSessionsResourcesBinding{CPU: "500m", Memory: "1Gi"}
	in.Binding.WorkerSelector = runtime.AgentSessionsWorkerSelectorBinding{}
	rendered, err := Render(in)
	if err != nil {
		t.Fatal(err)
	}
	micro, err := Render(renderTestInput(t))
	if err != nil {
		t.Fatal(err)
	}
	if rendered.ActorTemplate.SandboxConfig.SandboxClass != "gvisor" || rendered.ActorTemplate.WorkerSelector != nil || rendered.ActorTemplate.Resources.Limits[0] != (ActorLimit{"cpu", "500m"}) {
		t.Fatalf("gvisor render = %+v", rendered.ActorTemplate)
	}
	if rendered.ActorTemplate.Metadata.Name == micro.ActorTemplate.Metadata.Name {
		t.Fatal("another binding reused the template name")
	}
}

func TestRenderRefusesModelMismatch(t *testing.T) {
	in := renderTestInput(t)
	in.Model = "other-model"
	_, err := Render(in)
	if !errors.Is(err, runtime.ErrAgentSessionsModelMismatch) || !strings.Contains(err.Error(), "other-model") || !strings.Contains(err.Error(), "fixture-model") {
		t.Fatalf("model mismatch rendered: %v", err)
	}
}

func TestRenderRefusesInvalidInputs(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*RenderInput)
		want string
	}{
		{"tag only", func(in *RenderInput) { in.HarnessImage = "registry.example.invalid/harnessnode:v1" }, "digest-pinned"},
		{"tag and digest", func(in *RenderInput) {
			in.HarnessImage = strings.Replace(renderTestImage, "@", ":v1@", 1)
		}, "digest-pinned"},
		{"short digest", func(in *RenderInput) { in.HarnessImage = "registry.example.invalid/harnessnode@sha256:abc" }, "digest-pinned"},
		{"empty image", func(in *RenderInput) { in.HarnessImage = "" }, "digest-pinned"},
		{"reserved chat", func(in *RenderInput) { in.Member = "chat" }, "serves itself"},
		{"reserved echo", func(in *RenderInput) { in.Member = "echo" }, "serves itself"},
		{"oversized image", func(in *RenderInput) {
			in.HarnessImage = "registry.example.invalid/" + strings.Repeat("a", 512) + "@sha256:" + strings.Repeat("a", 64)
		}, "digest-pinned"},
		{"member case", func(in *RenderInput) { in.Member = "Triage" }, "DNS label"},
		{"member dot", func(in *RenderInput) { in.Member = "triage.v2" }, "DNS label"},
		{"member length", func(in *RenderInput) { in.Member = strings.Repeat("a", 64) }, "DNS label"},
		{"suite name", func(in *RenderInput) { in.Suite = "Support" }, "workspace suite name"},
		{"missing resources", func(in *RenderInput) { in.Binding.Resources.Memory = "" }, "resources.memory"},
		{"binding identity", func(in *RenderInput) { in.Binding.Kind = "KagentBindings" }, "kind"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in := renderTestInput(t)
			tc.edit(&in)
			if _, err := Render(in); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("invalid input rendered: %v", err)
			}
		})
	}
}

// Workspace suite names may contain '_' and '.' and end in '-'; the template
// name sanitizes them.
func TestRenderAcceptsWorkspaceSuiteNames(t *testing.T) {
	label := regexp.MustCompile(`^[a-z0-9]([-a-z0-9]{0,61}[a-z0-9])?$`)
	for _, suite := range []string{"support_desk", "support-desk-", "support.desk", "s"} {
		in := renderTestInput(t)
		in.Suite = suite
		rendered, err := Render(in)
		if err != nil {
			t.Fatalf("suite %q refused: %v", suite, err)
		}
		if name := rendered.ActorTemplate.Metadata.Name; !label.MatchString(name) {
			t.Fatalf("suite %q rendered template name %q", suite, name)
		}
	}
}

// The same content digest from another registry or repository is a different
// containers[0].image, so it must be a different template.
func TestRenderMirroredImageIsAnotherTemplate(t *testing.T) {
	digest := "@sha256:2c26b46b68ffc68ff99b453c1d30413413422d706483bfa0f98a5e886266e7ae"
	seen := map[string]string{}
	for _, image := range []string{
		renderTestImage,
		"mirror.example.invalid/agentsessions/harnessnode" + digest,
		"registry.example.invalid/other/harnessnode" + digest,
		"registry.example.invalid:5000/agentsessions/harnessnode" + digest,
	} {
		in := renderTestInput(t)
		in.HarnessImage = image
		rendered, err := Render(in)
		if err != nil {
			t.Fatal(err)
		}
		name := rendered.ActorTemplate.Metadata.Name
		if prior, ok := seen[name]; ok {
			t.Fatalf("%s and %s render different specs under one template name %s", prior, image, name)
		}
		seen[name] = image
		if rendered.ActorTemplate.Containers[0].Image != image || !strings.Contains(rendered.ActorTemplate.SnapshotConfig.StorageLocation, "/"+name+"/") {
			t.Fatalf("render of %s = %+v", image, rendered.ActorTemplate)
		}
	}
}

func TestTemplateNameIsABoundedDNSLabel(t *testing.T) {
	label := regexp.MustCompile(`^[a-z0-9]([-a-z0-9]{0,61}[a-z0-9])?$`)
	digest := "sha256:" + strings.Repeat("a", 64)
	for _, tc := range []struct{ suite, member string }{
		{"support-desk", "triage"},
		{strings.Repeat("s", 80), "triage"},
		{"suite", strings.Repeat("m", 80)},
		{strings.Repeat("a", 40) + "-" + strings.Repeat("b", 40), strings.Repeat("c", 30) + "-x"},
		{"Support.Desk", "Tri_age"},
	} {
		name := TemplateName(tc.suite, tc.member, digest, digest)
		if len(name) > 63 || !label.MatchString(name) || strings.Contains(name, "--") {
			t.Fatalf("TemplateName(%q, %q) = %q", tc.suite, tc.member, name)
		}
		if TemplateName(tc.suite, tc.member, digest, "sha256:"+strings.Repeat("b", 64)) == name {
			t.Fatal("binding digest did not change the name")
		}
		if templateName("kmx.agentsessions-actortemplate/v1", tc.suite, tc.member, digest, digest) == name {
			t.Fatal("renderer version did not change the name")
		}
	}
}

// A host-owned credential source reaches only the host model references: the
// template stays credential-free, and the source is part of the template name.
func TestRenderHostCredentialRefStaysWithTheHost(t *testing.T) {
	secret, err := Render(renderTestInput(t))
	if err != nil {
		t.Fatal(err)
	}
	in := renderTestInput(t)
	in.Binding.Model.Credential = runtime.AgentSessionsCredentialRef{HostRef: &runtime.AgentSessionsHostCredentialRef{Type: "workload-identity", Name: "triage-model-identity"}}
	hosted, err := Render(in)
	if err != nil {
		t.Fatal(err)
	}
	if ref := hosted.HostModel.Credential; ref.SecretRef != nil || ref.HostRef == nil || *ref.HostRef != (runtime.AgentSessionsHostCredentialRef{Type: "workload-identity", Name: "triage-model-identity"}) {
		t.Fatalf("host credential reference lost: %+v", hosted.HostModel)
	}
	raw, err := json.Marshal(hosted.ActorTemplate)
	if err != nil {
		t.Fatal(err)
	}
	for _, leaked := range []string{"workload-identity", "triage-model-identity", "credential"} {
		if strings.Contains(string(raw), leaked) {
			t.Fatalf("template carries the host credential reference %q: %s", leaked, raw)
		}
	}
	if hosted.BindingDigest == secret.BindingDigest || hosted.ActorTemplate.Metadata.Name == secret.ActorTemplate.Metadata.Name {
		t.Fatal("credential source did not change the binding digest and template name")
	}
}

package runtime

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"

	agentsuite "github.com/agent-primitives/suite"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/secretshapes"
)

const validAgentSessionsBindingYAML = `# Environment binding only; the suite pins the model, this file says where it is served.
apiVersion: kmx.kaimahi.dev/v1alpha1
kind: AgentSessionsBinding
target:
    platform: kubernetes
    id: agentsuite-demo
placement:
    backend: substrate
atespace: agentsuite-demo
sandbox:
    class: microvm
    configName: microvm
snapshot:
    fidelity: MEMORY
    storageLocation: gs://ate-snapshots/agentsuite-demo/
resources:
    cpu: "1"
    memory: 512Mi
workerSelector:
    matchLabels:
        workload: agentsuite-demo-microvm
model:
    name: fixture-model
    provider: openai
    baseURL: http://fakemodel.agentsuite-demo.svc:8080/v1
    secretRef:
        name: model-credentials
        key: api-key
`

func validAgentSessionsBinding() AgentSessionsBinding {
	return AgentSessionsBinding{
		Target:         AgentSessionsTargetBinding{Platform: "kubernetes", ID: "agentsuite-demo"},
		Placement:      AgentSessionsPlacementBinding{Backend: "substrate"},
		Atespace:       "agentsuite-demo",
		Sandbox:        AgentSessionsSandboxBinding{Class: "microvm", ConfigName: "microvm"},
		Snapshot:       AgentSessionsSnapshotBinding{Fidelity: "MEMORY", StorageLocation: "gs://ate-snapshots/agentsuite-demo/"},
		Resources:      AgentSessionsResourcesBinding{CPU: "1", Memory: "512Mi"},
		WorkerSelector: AgentSessionsWorkerSelectorBinding{MatchLabels: map[string]string{"workload": "agentsuite-demo-microvm"}},
		Model: AgentSessionsModelBinding{
			Name:      "fixture-model",
			Provider:  "openai",
			BaseURL:   "http://fakemodel.agentsuite-demo.svc:8080/v1",
			SecretRef: AgentSessionsSecretRefBinding{Name: "model-credentials", Key: "api-key"},
		},
	}
}

func TestAgentSessionsBindingStrictlyDecodeAndRoundTrip(t *testing.T) {
	binding, err := ParseAgentSessionsBinding([]byte(validAgentSessionsBindingYAML))
	if err != nil {
		t.Fatal(err)
	}
	want := validAgentSessionsBinding()
	want.APIVersion, want.Kind = AgentSessionsBindingAPIVersion, AgentSessionsBindingKind
	if err := ValidateAgentSessionsBinding(binding); err != nil {
		t.Fatal(err)
	}
	encoded, err := EncodeAgentSessionsBinding(binding)
	if err != nil {
		t.Fatal(err)
	}
	if string(encoded) != validAgentSessionsBindingYAML {
		t.Fatalf("binding encoding changed:\ngot:\n%s\nwant:\n%s", encoded, validAgentSessionsBindingYAML)
	}
	fromValue, err := EncodeAgentSessionsBinding(validAgentSessionsBinding())
	if err != nil || string(fromValue) != validAgentSessionsBindingYAML {
		t.Fatalf("encoding from a value lost fields: %v\n%s", err, fromValue)
	}
	if binding.Model.SecretRef != want.Model.SecretRef || binding.WorkerSelector.MatchLabels["workload"] != "agentsuite-demo-microvm" || binding.Resources != want.Resources {
		t.Fatalf("binding lost fields: %+v", binding)
	}
}

// The POC's JSON spelling parses, and its digest is the JCS digest of the
// document as written, so the template-name input is reproducible outside kmx.
func TestAgentSessionsBindingDigestIsJCSOfTheDocument(t *testing.T) {
	raw, err := os.ReadFile("testdata/agentsessions-binding-microvm.json")
	if err != nil {
		t.Fatal(err)
	}
	fromJSON, err := ParseAgentSessionsBinding(raw)
	if err != nil {
		t.Fatal(err)
	}
	fromYAML, err := ParseAgentSessionsBinding([]byte(validAgentSessionsBindingYAML))
	if err != nil {
		t.Fatal(err)
	}
	direct, err := agentsuite.Digest(raw)
	if err != nil {
		t.Fatal(err)
	}
	for _, b := range []AgentSessionsBinding{fromJSON, fromYAML} {
		got, err := AgentSessionsBindingDigest(b)
		if err != nil || got != direct {
			t.Fatalf("binding digest = %q, %v; want JCS digest %q", got, err, direct)
		}
	}
	commented, err := ParseAgentSessionsBinding([]byte("# another comment\n" + validAgentSessionsBindingYAML))
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := AgentSessionsBindingDigest(commented); got != direct {
		t.Fatal("a comment changed the binding digest")
	}
	changed := fromYAML
	changed.Sandbox.Class, changed.Sandbox.ConfigName = "gvisor", "gvisor-default"
	if got, _ := AgentSessionsBindingDigest(changed); got == direct {
		t.Fatal("a placement change kept the binding digest")
	}
	if _, err := AgentSessionsBindingDigest(validAgentSessionsBinding()); err == nil {
		t.Fatal("digest accepted a document without identity")
	}
}

func TestAgentSessionsBindingStorageLocationAcceptsObjectPrefixes(t *testing.T) {
	for _, location := range []string{"gs://bucket", "gs://bucket/", "gs://bucket/p", "gs://bucket/p/", "s3://bucket/a/b.c/..d/", "gs://bucket/.a/", "gs://bucket/" + strings.Repeat("a", maxAgentSessionsStorageLocation-len("gs://bucket/"))} {
		b := validAgentSessionsBinding()
		b.APIVersion, b.Kind = AgentSessionsBindingAPIVersion, AgentSessionsBindingKind
		b.Snapshot.StorageLocation = location
		if err := ValidateAgentSessionsBinding(b); err != nil {
			t.Fatalf("%q refused: %v", location, err)
		}
	}
}

func TestAgentSessionsBindingAcceptsSubstrateResourceBounds(t *testing.T) {
	b := validAgentSessionsBinding()
	b.Resources = AgentSessionsResourcesBinding{CPU: "999999m", Memory: "9223372036854775807"}
	b.WorkerSelector.MatchLabels = map[string]string{}
	for i := range maxAgentSessionsWorkerLabels {
		b.WorkerSelector.MatchLabels[fmt.Sprintf("k%d", i)] = "v"
	}
	if _, err := EncodeAgentSessionsBinding(b); err != nil {
		t.Fatalf("substrate's largest limits and selector refused: %v", err)
	}
}

func TestAgentSessionsBindingEmptyWorkerSelectorIsOmitted(t *testing.T) {
	omitted := strings.Replace(validAgentSessionsBindingYAML, "workerSelector:\n    matchLabels:\n        workload: agentsuite-demo-microvm\n", "", 1)
	if omitted == validAgentSessionsBindingYAML {
		t.Fatal("fixture has no workerSelector to omit")
	}
	want, err := ParseAgentSessionsBinding([]byte(omitted))
	if err != nil {
		t.Fatal(err)
	}
	wantDigest, err := AgentSessionsBindingDigest(want)
	if err != nil {
		t.Fatal(err)
	}
	wantEncoded, err := EncodeAgentSessionsBinding(want)
	if err != nil {
		t.Fatal(err)
	}
	for name, doc := range map[string]string{
		"empty selector":    omitted + "workerSelector: {}\n",
		"empty matchLabels": omitted + "workerSelector:\n    matchLabels: {}\n",
	} {
		b, err := ParseAgentSessionsBinding([]byte(doc))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if got, err := AgentSessionsBindingDigest(b); err != nil || got != wantDigest {
			t.Fatalf("%s: digest %q, %v; want %q", name, got, err, wantDigest)
		}
		if got, err := EncodeAgentSessionsBinding(b); err != nil || string(got) != string(wantEncoded) {
			t.Fatalf("%s: encoded %v\n%s", name, err, got)
		}
	}
	// In-memory callers get the same identity as parsed documents.
	inMemory := want
	inMemory.WorkerSelector.MatchLabels = map[string]string{}
	if got, err := AgentSessionsBindingDigest(inMemory); err != nil || got != wantDigest {
		t.Fatalf("in-memory empty matchLabels digest %q, %v; want %q", got, err, wantDigest)
	}
}

func TestAgentSessionsBindingModelMustMatchSuite(t *testing.T) {
	b := validAgentSessionsBinding()
	if err := CheckAgentSessionsBindingModel(b, "fixture-model"); err != nil {
		t.Fatal(err)
	}
	for _, suite := range []string{"fixture-model-2", "Fixture-Model", "fixture", "fixture-model "} {
		err := CheckAgentSessionsBindingModel(b, suite)
		if err == nil || !strings.Contains(err.Error(), "new suite version") || !strings.Contains(err.Error(), "fixture-model") {
			t.Fatalf("model %q matched: %v", suite, err)
		}
		if !errors.Is(err, ErrAgentSessionsModelMismatch) {
			t.Fatalf("mismatch is not classified: %v", err)
		}
	}
}

func TestValidateAgentSessionsBindingRequiresClosedIdentity(t *testing.T) {
	b := validAgentSessionsBinding()
	if err := ValidateAgentSessionsBinding(b); err == nil || !strings.Contains(err.Error(), "apiVersion") {
		t.Fatalf("validator accepted an unstated document identity: %v", err)
	}
	b.APIVersion = AgentSessionsBindingAPIVersion
	if err := ValidateAgentSessionsBinding(b); err == nil || !strings.Contains(err.Error(), "kind") {
		t.Fatalf("validator accepted an unstated kind: %v", err)
	}
	b.Kind = AgentSessionsBindingKind
	if err := ValidateAgentSessionsBinding(b); err != nil {
		t.Fatal(err)
	}
}

func TestAgentSessionsBindingOptionalSectionsMayBeOmitted(t *testing.T) {
	doc := strings.Replace(validAgentSessionsBindingYAML, "workerSelector:\n    matchLabels:\n        workload: agentsuite-demo-microvm\n", "", 1)
	doc = strings.Replace(doc, "    baseURL: http://fakemodel.agentsuite-demo.svc:8080/v1\n", "", 1)
	b, err := ParseAgentSessionsBinding([]byte(doc))
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := EncodeAgentSessionsBinding(b)
	if err != nil || string(encoded) != doc {
		t.Fatalf("optional sections did not round-trip: %v\n%s", err, encoded)
	}
}

func TestAgentSessionsBindingRejectUnknownAmbiguousAndIncompleteDocuments(t *testing.T) {
	for _, tc := range []struct{ name, doc string }{
		{"unknown", strings.Replace(validAgentSessionsBindingYAML, "    provider: openai\n", "    provider: openai\n    value: forbidden\n", 1)},
		{"unknown top level", validAgentSessionsBindingYAML + "image: example.invalid/harness\n"},
		{"secret value field", strings.Replace(validAgentSessionsBindingYAML, "        key: api-key\n", "        key: api-key\n        value: forbidden\n", 1)},
		{"duplicate", strings.Replace(validAgentSessionsBindingYAML, "atespace: agentsuite-demo\n", "atespace: agentsuite-demo\natespace: other\n", 1)},
		{"merge", strings.Replace(validAgentSessionsBindingYAML, "    class: microvm\n", "    <<: {class: gvisor}\n    class: microvm\n", 1)},
		{"alias", strings.Replace(validAgentSessionsBindingYAML, "    class: microvm\n    configName: microvm\n", "    class: &c microvm\n    configName: *c\n", 1)},
		{"extra document", validAgentSessionsBindingYAML + "---\nkind: ConfigMap\n"},
		{"missing apiVersion", strings.Replace(validAgentSessionsBindingYAML, "apiVersion: kmx.kaimahi.dev/v1alpha1\n", "", 1)},
		{"wrong kind", strings.Replace(validAgentSessionsBindingYAML, "kind: AgentSessionsBinding\n", "kind: KagentBindings\n", 1)},
		{"missing target", strings.Replace(validAgentSessionsBindingYAML, "target:\n    platform: kubernetes\n    id: agentsuite-demo\n", "", 1)},
		{"missing placement", strings.Replace(validAgentSessionsBindingYAML, "placement:\n    backend: substrate\n", "", 1)},
		{"missing atespace", strings.Replace(validAgentSessionsBindingYAML, "atespace: agentsuite-demo\n", "", 1)},
		{"missing fidelity", strings.Replace(validAgentSessionsBindingYAML, "    fidelity: MEMORY\n", "", 1)},
		{"missing storage", strings.Replace(validAgentSessionsBindingYAML, "    storageLocation: gs://ate-snapshots/agentsuite-demo/\n", "", 1)},
		{"missing resources", strings.Replace(validAgentSessionsBindingYAML, "resources:\n    cpu: \"1\"\n    memory: 512Mi\n", "", 1)},
		{"missing cpu", strings.Replace(validAgentSessionsBindingYAML, "    cpu: \"1\"\n", "", 1)},
		{"missing memory", strings.Replace(validAgentSessionsBindingYAML, "    memory: 512Mi\n", "", 1)},
		{"missing model", strings.Replace(validAgentSessionsBindingYAML, "    name: fixture-model\n", "", 1)},
		{"missing secret key", strings.Replace(validAgentSessionsBindingYAML, "        key: api-key\n", "", 1)},
		{"sequence root", "- apiVersion: kmx.kaimahi.dev/v1alpha1\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := ParseAgentSessionsBinding([]byte(tc.doc)); err == nil {
				t.Fatal("accepted invalid AgentSessions binding")
			}
		})
	}
}

func TestAgentSessionsBindingValidateFields(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*AgentSessionsBinding)
		want string
	}{
		{"platform", func(b *AgentSessionsBinding) { b.Target.Platform = "nomad" }, "target.platform"},
		{"target id", func(b *AgentSessionsBinding) { b.Target.ID = "Demo" }, "target.id"},
		{"backend", func(b *AgentSessionsBinding) { b.Placement.Backend = "local" }, "placement.backend"},
		{"atespace", func(b *AgentSessionsBinding) { b.Atespace = "a.b" }, "atespace"},
		{"sandbox class", func(b *AgentSessionsBinding) { b.Sandbox.Class = "runc" }, "sandbox.class"},
		{"sandbox config", func(b *AgentSessionsBinding) { b.Sandbox.ConfigName = "Micro VM" }, "sandbox.configName"},
		{"fidelity", func(b *AgentSessionsBinding) { b.Snapshot.Fidelity = "memory" }, "snapshot.fidelity"},
		{"rootfs on gvisor", func(b *AgentSessionsBinding) {
			b.Sandbox.Class, b.Sandbox.ConfigName, b.Snapshot.Fidelity = "gvisor", "gvisor-default", "ROOTFS"
		}, "requires sandbox.class microvm"},
		{"storage scheme", func(b *AgentSessionsBinding) { b.Snapshot.StorageLocation = "/var/snapshots" }, "storageLocation"},
		{"storage credentials", func(b *AgentSessionsBinding) { b.Snapshot.StorageLocation = "s3://user:pw@bucket/p/" }, "storageLocation"},
		{"storage query", func(b *AgentSessionsBinding) { b.Snapshot.StorageLocation = "gs://bucket/p/?x=1" }, "storageLocation"},
		{"storage dot segment", func(b *AgentSessionsBinding) { b.Snapshot.StorageLocation = "gs://bucket/./p/" }, "storageLocation"},
		{"storage dot-dot segment", func(b *AgentSessionsBinding) { b.Snapshot.StorageLocation = "gs://bucket/p/../q/" }, "storageLocation"},
		{"storage trailing dot-dot", func(b *AgentSessionsBinding) { b.Snapshot.StorageLocation = "gs://bucket/p/.." }, "storageLocation"},
		{"storage encoded dot-dot", func(b *AgentSessionsBinding) { b.Snapshot.StorageLocation = "gs://bucket/%2e%2E/p/" }, "storageLocation"},
		{"storage empty segment", func(b *AgentSessionsBinding) { b.Snapshot.StorageLocation = "gs://bucket/p//q/" }, "storageLocation"},
		{"storage empty root segment", func(b *AgentSessionsBinding) { b.Snapshot.StorageLocation = "gs://bucket//" }, "storageLocation"},
		{"storage double trailing slash", func(b *AgentSessionsBinding) { b.Snapshot.StorageLocation = "gs://bucket/p//" }, "storageLocation"},
		{"storage length", func(b *AgentSessionsBinding) {
			b.Snapshot.StorageLocation = "gs://bucket/" + strings.Repeat("a", maxAgentSessionsStorageLocation)
		}, "storageLocation"},
		{"storage dots-only segment", func(b *AgentSessionsBinding) { b.Snapshot.StorageLocation = "gs://bucket/.../" }, "storageLocation"},
		{"storage encoded dots and space", func(b *AgentSessionsBinding) { b.Snapshot.StorageLocation = "gs://bucket/.%20./p/" }, "storageLocation"},
		{"storage backslash", func(b *AgentSessionsBinding) { b.Snapshot.StorageLocation = `gs://bucket/a\b/` }, "storageLocation"},
		{"storage encoded backslash", func(b *AgentSessionsBinding) { b.Snapshot.StorageLocation = "gs://bucket/a%5Cb/" }, "storageLocation"},
		{"storage rendered length", func(b *AgentSessionsBinding) {
			b.Snapshot.StorageLocation = "gs://bucket/" + strings.Repeat("a", maxAgentSessionsStorageLocation-len("gs://bucket/")+1)
		}, "storageLocation"},
		{"cpu", func(b *AgentSessionsBinding) { b.Resources.CPU = "one" }, "resources.cpu"},
		{"memory", func(b *AgentSessionsBinding) { b.Resources.Memory = "-1Gi" }, "resources.memory"},
		{"cpu required", func(b *AgentSessionsBinding) { b.Resources.CPU = "" }, "resources.cpu is required"},
		{"memory required", func(b *AgentSessionsBinding) { b.Resources.Memory = "" }, "resources.memory is required"},
		{"cpu zero", func(b *AgentSessionsBinding) { b.Resources.CPU = "0" }, "resources.cpu"},
		{"cpu above substrate bound", func(b *AgentSessionsBinding) { b.Resources.CPU = "1000" }, "resources.cpu"},
		{"quantity length", func(b *AgentSessionsBinding) { b.Resources.Memory = "512" + strings.Repeat("0", 30) + "m" }, "resources.memory"},
		{"too many worker labels", func(b *AgentSessionsBinding) {
			b.WorkerSelector.MatchLabels = map[string]string{}
			for i := range maxAgentSessionsWorkerLabels + 1 {
				b.WorkerSelector.MatchLabels[fmt.Sprintf("k%d", i)] = "v"
			}
		}, "more than 10 labels"},
		{"label key", func(b *AgentSessionsBinding) { b.WorkerSelector.MatchLabels = map[string]string{"bad key": "x"} }, "matchLabels key"},
		{"label value", func(b *AgentSessionsBinding) { b.WorkerSelector.MatchLabels = map[string]string{"k": "bad value"} }, "matchLabels value"},
		{"model name", func(b *AgentSessionsBinding) { b.Model.Name = " fixture-model" }, "model.name"},
		{"model provider", func(b *AgentSessionsBinding) { b.Model.Provider = "anthropic" }, "model.provider"},
		{"base URL", func(b *AgentSessionsBinding) { b.Model.BaseURL = "https://user:password@example.invalid" }, "model.baseURL"},
		{"base URL scheme", func(b *AgentSessionsBinding) { b.Model.BaseURL = "ftp://example.invalid" }, "model.baseURL"},
		{"secret name", func(b *AgentSessionsBinding) { b.Model.SecretRef.Name = "review/key" }, "model.secretRef.name"},
		{"secret key", func(b *AgentSessionsBinding) { b.Model.SecretRef.Key = "review/key" }, "model.secretRef.key"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := validAgentSessionsBinding()
			tc.edit(&b)
			_, err := EncodeAgentSessionsBinding(b)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("invalid binding encoded: %v", err)
			}
			for _, secret := range []string{"password", "pw@"} {
				if strings.Contains(err.Error(), secret) {
					t.Fatal("refusal echoed a credential-bearing value")
				}
			}
		})
	}
	b := validAgentSessionsBinding()
	b.Snapshot.Fidelity = "ROOTFS"
	if _, err := EncodeAgentSessionsBinding(b); err != nil {
		t.Fatalf("ROOTFS on microvm refused: %v", err)
	}
}

func TestAgentSessionsBindingRejectInvalidUTF8(t *testing.T) {
	if _, err := ParseAgentSessionsBinding(append([]byte(validAgentSessionsBindingYAML), 0xff)); err == nil || !strings.Contains(err.Error(), "UTF-8") {
		t.Fatalf("invalid raw UTF-8 accepted: %v", err)
	}
	b := validAgentSessionsBinding()
	b.Model.SecretRef.Name = "review\xffkey"
	if _, err := EncodeAgentSessionsBinding(b); err == nil || !strings.Contains(err.Error(), "UTF-8") || strings.Contains(err.Error(), "\xffkey") {
		t.Fatalf("invalid decoded UTF-8 accepted or echoed: %v", err)
	}
	b = validAgentSessionsBinding()
	b.WorkerSelector.MatchLabels = map[string]string{"k": "v\xff"}
	if _, err := EncodeAgentSessionsBinding(b); err == nil || !strings.Contains(err.Error(), "UTF-8") {
		t.Fatalf("invalid label UTF-8 accepted: %v", err)
	}
}

func TestAgentSessionsBindingNeverAcceptsCredentialValues(t *testing.T) {
	for _, shape := range secretshapes.All() {
		t.Run(shape.Name, func(t *testing.T) {
			for _, doc := range []string{
				strings.Replace(validAgentSessionsBindingYAML, "    id: agentsuite-demo", "    id: "+shape.Example, 1),
				strings.Replace(validAgentSessionsBindingYAML, "atespace: agentsuite-demo", "atespace: "+shape.Example, 1),
				strings.Replace(validAgentSessionsBindingYAML, "    storageLocation: gs://ate-snapshots/agentsuite-demo/", "    storageLocation: "+shape.Example, 1),
				strings.Replace(validAgentSessionsBindingYAML, "        workload: agentsuite-demo-microvm", "        workload: "+shape.Example, 1),
				strings.Replace(validAgentSessionsBindingYAML, "    name: fixture-model", "    name: "+shape.Example, 1),
				strings.Replace(validAgentSessionsBindingYAML, "    baseURL: http://fakemodel.agentsuite-demo.svc:8080/v1", "    baseURL: "+shape.Example, 1),
				strings.Replace(validAgentSessionsBindingYAML, "        name: model-credentials", "        name: "+shape.Example, 1),
				strings.Replace(validAgentSessionsBindingYAML, "        key: api-key", "        key: "+shape.Example, 1),
			} {
				_, err := ParseAgentSessionsBinding([]byte(doc))
				assertRefusedWithoutEcho(t, err, shape.Example)
			}
		})
	}
}

func TestAgentSessionsBindingRefusesCredentialShapesAssembledByYAML(t *testing.T) {
	proven := 0
	for _, shape := range secretshapes.All() {
		if shape.Example == "" || shape.Example[0] >= 0x80 || strings.ContainsAny(shape.Example, "\"\\\n") {
			continue
		}
		for _, doc := range []string{
			strings.Replace(validAgentSessionsBindingYAML, "        key: api-key", "        key: "+escapedPortableKey(shape.Example), 1),
			strings.Replace(validAgentSessionsBindingYAML, "        workload: agentsuite-demo-microvm", "        workload: "+escapedPortableKey(shape.Example), 1),
		} {
			if secretshapes.Match(doc) != nil {
				continue
			}
			proven++
			_, err := ParseAgentSessionsBinding([]byte(doc))
			assertRefusedWithoutEcho(t, err, shape.Example)
		}
	}
	if proven == 0 {
		t.Fatal("decoded AgentSessions binding credential scan was not exercised")
	}
}

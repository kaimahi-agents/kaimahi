package scaffold

import (
	"path/filepath"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

func migration() MigrateSpec {
	return MigrateSpec{
		Namespace: "demo", Deployment: "concierge", Container: "concierge",
		Upstream: "orka", Model: "local/qwen2.5:3b", Secret: "kaimahi-concierge-token",
		BaseURLVar: MigrateBaseURLVar, KeyVar: MigrateKeyVar, ModelVar: MigrateModelVar,
		OrkaNamespace: "orka-system", ServiceAccount: "kaimahi-migrate",
	}
}

func mustRender(t *testing.T, render func(MigrateSpec) (string, error), spec MigrateSpec) string {
	t.Helper()
	document, err := render(spec)
	if err != nil {
		t.Fatal(err)
	}
	var parsed any
	for _, doc := range strings.Split(document, "\n---\n") {
		if err := yaml.Unmarshal([]byte(doc), &parsed); err != nil {
			t.Fatalf("emitted document is not YAML: %v\n%s", err, doc)
		}
	}
	return document
}

// The claim the whole command rests on: the application is repointed by
// configuration. If the patch ever names an image, a command or a source
// path, that claim is gone.
func TestThePatchChangesConfigurationAndNothingElse(t *testing.T) {
	patch := mustRender(t, GenerateMigratePatch, migration())
	for _, forbidden := range []string{"image:", "command:", "args:", "initContainers"} {
		if strings.Contains(patch, forbidden) {
			t.Fatalf("the patch carries %q, so the application is no longer unchanged:\n%s", forbidden, patch)
		}
	}
	for _, want := range []string{
		`- name: "concierge"`,
		"name: OPENAI_BASE_URL",
		`value: "https://kaimahi-proxy.kaimahi.svc.cluster.local:8080/upstream/orka/v1"`,
		"name: OPENAI_CHAT_MODEL",
		`value: "local/qwen2.5:3b"`,
		"name: SSL_CERT_FILE",
		`value: "/etc/kaimahi/plane-ca/ca.crt"`,
	} {
		if !strings.Contains(patch, want) {
			t.Fatalf("the patch does not carry %q:\n%s", want, patch)
		}
	}
}

// The application stops holding a model credential. It presents a kmh_
// token for the plane, resolved from a Secret, and the key that opens the
// endpoint behind the seam is never in this file at all.
func TestThePatchNamesTheCredentialAndNeverValuesIt(t *testing.T) {
	patch := mustRender(t, GenerateMigratePatch, migration())
	if !strings.Contains(patch, "secretKeyRef") ||
		!strings.Contains(patch, `name: "kaimahi-concierge-token"`) ||
		!strings.Contains(patch, "key: api-key") {
		t.Fatalf("the patch does not resolve the credential from a Secret:\n%s", patch)
	}
	if strings.Contains(patch, "kaimahi-orka-token") {
		t.Fatalf("the patch names the seam's own upstream token, which the application must never hold:\n%s", patch)
	}
}

// The identity is the plane's, in Orka's namespace, and it grants exactly
// the permission Orka's route table authorizes its compatible endpoint
// against — not a wildcard, and not cluster-wide.
func TestTheIdentityGrantsTheOnePermissionTheEndpointAuthorizes(t *testing.T) {
	identity := mustRender(t, GenerateMigrateIdentity, migration())
	for _, want := range []string{
		"kind: ServiceAccount",
		"kind: Role\n",
		"kind: RoleBinding",
		"namespace: \"orka-system\"",
		`apiGroups: ["core.orka.ai"]`,
		`resources: ["chats"]`,
		`verbs: ["create"]`,
	} {
		if !strings.Contains(identity, want) {
			t.Fatalf("the identity does not carry %q:\n%s", want, identity)
		}
	}
	for _, forbidden := range []string{"ClusterRole", `verbs: ["*"]`, `resources: ["*"]`} {
		if strings.Contains(identity, forbidden) {
			t.Fatalf("the identity carries %q, which is wider than the endpoint asks for:\n%s", forbidden, identity)
		}
	}
}

// The seam allowance opens the model port for one namespace. The tool
// seam's port is a separate decision and must not ride along.
func TestTheSeamAllowanceOpensTheModelPortForOneNamespace(t *testing.T) {
	access := mustRender(t, GenerateMigrateSeamAccess, migration())
	if !strings.Contains(access, `name: "kaimahi-proxy-ingress-demo"`) ||
		!strings.Contains(access, "namespace: kaimahi") {
		t.Fatalf("the allowance is not the plane's own object:\n%s", access)
	}
	if !strings.Contains(access, "kubernetes.io/metadata.name: \"demo\"") {
		t.Fatalf("the allowance does not name the application's namespace:\n%s", access)
	}
	if !strings.Contains(access, "port: 8080") {
		t.Fatalf("the allowance does not open the model port:\n%s", access)
	}
	if strings.Contains(access, "port: 8081") {
		t.Fatalf("the allowance opens the tool seam too, which is a decision it was not given:\n%s", access)
	}
}

// Every document is refused rather than emitted when a value could
// change what it says. A migration writes into somebody else's
// namespace, so a name that is not a name is refused at the top.
func TestAMigrationIsRefusedRatherThanEmittedWithAValueThatCouldInject(t *testing.T) {
	for _, tc := range []struct {
		name   string
		break_ func(*MigrateSpec)
		says   string
	}{
		{"a namespace that is not one", func(s *MigrateSpec) { s.Namespace = "Demo Namespace" }, "namespace"},
		{"a deployment that is not an object name", func(s *MigrateSpec) { s.Deployment = "con cierge" }, "deployment"},
		{"a container carrying a newline", func(s *MigrateSpec) { s.Container = "concierge\nfoo: bar" }, "container"},
		{"an upstream that is not one", func(s *MigrateSpec) { s.Upstream = "../etc" }, "upstream name"},
		{"no model at all", func(s *MigrateSpec) { s.Model = "  " }, "--model"},
		{"a variable name that is not one", func(s *MigrateSpec) { s.ModelVar = "2MODEL" }, "--model-var"},
		// --model is the one field with no format of its own, so it is
		// the one the two key-shape passes exist for.
		{"a model name carrying a newline", func(s *MigrateSpec) { s.Model = "gpt\nfoo: bar" }, "control characters"},
		{"a model name shaped like a credential",
			func(s *MigrateSpec) { s.Model = "sk-" + strings.Repeat("a", 48) }, "shaped like"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			spec := migration()
			tc.break_(&spec)
			for _, render := range []func(MigrateSpec) (string, error){
				GenerateMigrateIdentity, GenerateMigrateSeamAccess, GenerateMigratePatch,
			} {
				if _, err := render(spec); err == nil {
					t.Fatalf("a spec broken by %q was rendered anyway", tc.name)
				} else if !strings.Contains(strings.ToLower(err.Error()), strings.ToLower(tc.says)) {
					t.Fatalf("the refusal does not say %q: %v", tc.says, err)
				}
			}
		})
	}
}

// The base URL stops at /v1 because an OpenAI client appends the rest.
// A trailing `responses` here would produce `…/v1/responses/responses`,
// which the seam refuses as a path it does not accept — a migration that
// reported success and never worked.
func TestTheBaseURLStopsWhereTheClientTakesOver(t *testing.T) {
	got := MigrateBaseURL("orka")
	if !strings.HasSuffix(got, "/upstream/orka/v1") {
		t.Fatalf("MigrateBaseURL(orka) = %q", got)
	}
	if strings.Contains(got, "responses") || strings.Contains(got, "chat/completions") {
		t.Fatalf("the base URL names a protocol path the client appends itself: %q", got)
	}
}

// A migration is meant to be re-runnable — minting a fresh token for an
// expiring one is the same command — so a file this command would have
// written verbatim is left alone rather than refused. A file that
// DIFFERS is still refused, because that one may be the operator's.
func TestARerunKeepsAnIdenticalFileAndRefusesAChangedOne(t *testing.T) {
	path := filepath.Join(t.TempDir(), "concierge.yaml")
	document, err := GenerateMigratePatch(migration())
	if err != nil {
		t.Fatal(err)
	}
	unchanged, err := WriteNewOrIdentical(path, document)
	if err != nil || unchanged {
		t.Fatalf("the first write should have created the file: unchanged=%v err=%v", unchanged, err)
	}
	unchanged, err = WriteNewOrIdentical(path, document)
	if err != nil || !unchanged {
		t.Fatalf("an identical re-write should be reported unchanged: unchanged=%v err=%v", unchanged, err)
	}
	if _, err := WriteNewOrIdentical(path, document+"\n# edited by hand\n"); err == nil {
		t.Fatal("a file whose contents differ was overwritten")
	}
}

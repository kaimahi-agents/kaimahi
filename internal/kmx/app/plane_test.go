package app

import (
	"encoding/base64"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// kmx builds and side-loads the image under one tag and deploys a manifest
// that names another only if these two drift. Nothing would fail at deploy
// time — kind's `imagePullPolicy: Never` would simply keep running whatever
// was already there under the manifest's tag, and the plane would silently
// be the previous build.
//
// The manifest is PARSED, not grepped: the string "imagePullPolicy: Never"
// appears in that file's own comments, so a grep would keep passing after
// the real field was deleted. That is scripts/plane-deploy.sh's rule, and it
// is why python3 + PyYAML do the reading here (the same skip-if-absent
// pattern the scaffolder's manifest test uses; ci.yml asserts this ran).
func TestPlaneImageMatchesTheCommittedManifest(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 is not installed; ci.yml asserts this test ran")
	}
	manifest := filepath.Join("..", "..", "..", "k8s", "plane", "proxy.yaml")
	script := `
import sys, yaml
found = [
    (c.get("image"), c.get("imagePullPolicy"))
    for d in yaml.safe_load_all(open(sys.argv[1]))
    if d and d.get("kind") == "Deployment"
    for c in d["spec"]["template"]["spec"]["containers"]
    if c["name"] == "proxy"
]
if len(found) != 1:
    sys.exit(f"expected exactly 1 proxy container, found {len(found)}")
print("%s\t%s" % found[0])
`
	out, err := exec.Command(python, "-c", script, manifest).Output()
	if err != nil {
		t.Skipf("cannot parse the manifest (PyYAML missing?): %v", err)
	}
	fields := strings.Split(strings.TrimSpace(string(out)), "\t")
	if len(fields) != 2 {
		t.Fatalf("unexpected reader output %q", out)
	}
	image, policy := fields[0], fields[1]

	if image != PlaneImage {
		t.Errorf("kmx builds %q but k8s/plane/proxy.yaml deploys %q — the plane would keep running the previous image",
			PlaneImage, image)
	}
	// The pin is deliberate and stays committed: a side-loaded LOCAL tag
	// must never quietly fall back to PULLING a squattable public name.
	if policy != "Never" {
		t.Errorf("committed proxy pull policy is %q, want Never", policy)
	}
}

// The plane's manifests are applied in the order `kubectl apply -f
// k8s/plane/` applies them (kubectl sorts by filename), and ALL of them are
// applied. A manifest embedded but not applied — or applied before the
// namespace exists — is a plane that half deploys.
func TestEveryPlaneManifestIsAppliedInKubectlsOrder(t *testing.T) {
	entries, err := os.ReadDir(filepath.Join("..", "..", "..", "k8s", "plane"))
	if err != nil {
		t.Fatal(err)
	}
	var onDisk []string
	for _, e := range entries {
		if name := e.Name(); strings.HasSuffix(name, ".yaml") || strings.HasSuffix(name, ".yml") {
			onDisk = append(onDisk, "plane/"+name)
		}
	}
	if len(onDisk) != len(planeManifests) {
		t.Fatalf("k8s/plane holds %v but kmx applies %v", onDisk, planeManifests)
	}
	for i := range onDisk {
		// os.ReadDir returns entries sorted by filename, which is exactly
		// the order `kubectl apply -f <dir>` uses.
		if onDisk[i] != planeManifests[i] {
			t.Errorf("apply order differs from kubectl's: %v vs %v", planeManifests, onDisk)
			break
		}
	}
	if planeManifests[0] != "plane/namespace.yaml" {
		t.Errorf("the namespace must be applied first, got %q", planeManifests[0])
	}
}

// Generated secrets travel through the pipe into kubectl and nowhere else.
// The shell had to write them to 0600 files first, because `kubectl create
// secret --from-file` reads a path; this is the property that replaces that
// file, so it is worth asserting rather than commenting.
func TestSecretManifestCarriesValuesInTheDocumentOnly(t *testing.T) {
	body := string(secretManifest("kaimahi-governed-token", "orka-system",
		map[string]string{"api-key": "kmh_" + strings.Repeat("a", 64)},
		map[string]string{"kaimahi.dev/credential": "hello-world"}))

	if strings.Contains(body, "kmh_") {
		t.Errorf("the token appears in cleartext in the manifest:\n%s", body)
	}
	encoded := base64.StdEncoding.EncodeToString([]byte("kmh_" + strings.Repeat("a", 64)))
	if !strings.Contains(body, "api-key: "+encoded) {
		t.Errorf("the token is not carried as data:\n%s", body)
	}
	for _, want := range []string{
		"kind: Secret",
		"name: kaimahi-governed-token",
		"namespace: orka-system",
		`kaimahi.dev/credential: "hello-world"`,
		"type: Opaque",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("manifest lacks %q:\n%s", want, body)
		}
	}
}

// A random value is a random value: 32 bytes, hex, and never the same twice.
// The failure this prevents is a plane bootstrapped with a predictable admin
// bearer, which is the credential that gates issuing every other one.
func TestGeneratedSecretsAreRandomAndFullLength(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 50; i++ {
		v, err := randomHex(32)
		if err != nil {
			t.Fatal(err)
		}
		if len(v) != 64 {
			t.Fatalf("generated %d hex characters, want 64", len(v))
		}
		if seen[v] {
			t.Fatal("the same value was generated twice")
		}
		seen[v] = true
	}
}

// PLANE_IMAGE is the registry path's variable. Honouring it on kind would
// build one tag and deploy another; ignoring it silently would be worse.
func TestAForeignPlaneImageIsRefusedRatherThanIgnored(t *testing.T) {
	a := &App{Cfg: nil}
	t.Setenv("PLANE_IMAGE", "")
	if err := a.refuseForeignImageTag(); err != nil {
		t.Errorf("unset PLANE_IMAGE was refused: %v", err)
	}
	t.Setenv("PLANE_IMAGE", PlaneImage)
	if err := a.refuseForeignImageTag(); err != nil {
		t.Errorf("PLANE_IMAGE naming the committed tag was refused: %v", err)
	}
	t.Setenv("PLANE_IMAGE", "myregistry.example/kaimahi-proxy:p10")
	err := a.refuseForeignImageTag()
	if err == nil {
		t.Fatal("a registry image was accepted on the kind path")
	}
	if !strings.Contains(err.Error(), "kmx aks up --payload orka --step plane") {
		t.Errorf("the refusal does not name the path that does render: %v", err)
	}
	if strings.Contains(err.Error(), "<orka|kagent>") {
		t.Errorf("the refusal still offers the retired kagent payload: %v", err)
	}
}

// Which source the plane is built from decides what a CI run proves. The
// Makefile passes `--source .`; a developer inside a clone gets the same
// answer without asking; a `go install` user gets the module proxy; and
// `--source -` is how you test the proxy path from inside a checkout.
func TestPlaneSourceSelection(t *testing.T) {
	repo := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repo, "plane"), 0o755); err != nil {
		t.Fatal(err)
	}
	for path, body := range map[string]string{
		"go.mod":       "module github.com/kaimahi-agents/kaimahi\n",
		"plane/go.mod": "module github.com/kaimahi-agents/kaimahi/plane\n",
	} {
		if err := os.WriteFile(filepath.Join(repo, path), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	a := &App{Err: io.Discard}

	got, err := a.planeSource(repo)
	if err != nil || got != repo {
		t.Errorf("--source <checkout>: got %q, %v", got, err)
	}

	// "-" forces the clone-free path even standing inside a checkout, which
	// is the only way to exercise the fetch from a development machine.
	if got, err := a.planeSource("-"); err != nil || got != "" {
		t.Errorf(`--source -: got %q, %v; want the module proxy`, got, err)
	}

	// A path that is not this repository is refused, not silently treated
	// as "no source" and fetched instead: the operator asked for a build of
	// something specific and did not get it.
	notARepo := t.TempDir()
	if _, err := a.planeSource(notARepo); err == nil {
		t.Errorf("--source %s was accepted", notARepo)
	} else if !strings.Contains(err.Error(), "not a checkout") {
		t.Errorf("unhelpful refusal: %v", err)
	}

	// No flag, run from inside a checkout: found.
	t.Chdir(filepath.Join(repo, "plane"))
	if got, err := a.planeSource(""); err != nil || got != repo {
		t.Errorf("auto-detection from inside the checkout: got %q, %v; want %q", got, err, repo)
	}
	// No flag, run from somewhere that is not a checkout: the module proxy.
	t.Chdir(notARepo)
	if got, err := a.planeSource(""); err != nil || got != "" {
		t.Errorf("auto-detection outside a checkout: got %q, %v; want the module proxy", got, err)
	}
}

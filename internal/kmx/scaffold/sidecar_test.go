package scaffold

import (
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

func shim() SidecarSpec {
	return SidecarSpec{
		Upstream:   "warehouse",
		Namespace:  "acme",
		Secret:     "kaimahi-warehouse-token",
		Deployment: "concierge",
	}
}

// The shim's whole reason to exist: the token is presented without ever
// being written down. It comes from the Secret, through the pod's
// environment, into a config nginx renders at startup.
func TestTheShimNamesTheSecretAndNeverValuesIt(t *testing.T) {
	cm, patch, err := GenerateSidecar(shim())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(cm, `Bearer ${KMH}`) {
		t.Fatalf("the config does not put the credential on the request:\n%s", cm)
	}
	if !strings.Contains(patch, "secretKeyRef") ||
		!strings.Contains(patch, "name: kaimahi-warehouse-token") ||
		!strings.Contains(patch, "key: api-key") {
		t.Fatalf("the patch does not resolve the token from the Secret:\n%s", patch)
	}
	// Only KMH is substituted, or nginx's own $variables would be blanked
	// by the same pass.
	if !strings.Contains(patch, "NGINX_ENVSUBST_FILTER") || !strings.Contains(patch, "value: KMH") {
		t.Fatalf("the template pass is not filtered to the token:\n%s", patch)
	}
}

// Both seams serve TLS under an authority the plane mints for itself. A
// shim that spoke plain http, or verified against the system trust store,
// or skipped verification, would each fail differently and one of them
// silently.
func TestTheShimVerifiesTheSeamAgainstThePlanesAuthority(t *testing.T) {
	cm, patch, err := GenerateSidecar(shim())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(cm, "proxy_pass https://") {
		t.Fatalf("the shim does not speak TLS to the seam:\n%s", cm)
	}
	if !strings.Contains(cm, "proxy_ssl_verify              on;") {
		t.Fatalf("the shim does not verify the seam:\n%s", cm)
	}
	if !strings.Contains(cm, "proxy_ssl_trusted_certificate /etc/kaimahi/ca.crt;") {
		t.Fatalf("the shim verifies against nothing it was given:\n%s", cm)
	}
	// The certificate carries the two-label form; verifying against
	// anything else fails with a hostname error that reads like a
	// network fault.
	if !strings.Contains(cm, "proxy_ssl_name                kaimahi-mcp-gateway.kaimahi;") {
		t.Fatalf("the shim verifies the wrong name:\n%s", cm)
	}
	if !strings.Contains(patch, "secretName: "+PlaneCASecret) ||
		!strings.Contains(patch, "mountPath: /etc/kaimahi") {
		t.Fatalf("the authority is not mounted where the config reads it:\n%s", patch)
	}
}

// The shim reaches ONE upstream by its operator-owned name, and proxies
// nothing else. It is a credential, not a route.
func TestTheShimProxiesOneUpstreamAndNothingElse(t *testing.T) {
	cm, _, err := GenerateSidecar(shim())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(cm, GatewayURL("warehouse")) {
		t.Fatalf("the shim does not point at the upstream's seam:\n%s", cm)
	}
	if !strings.Contains(cm, "location / { return 404; }") {
		t.Fatalf("the shim proxies more than the seam:\n%s", cm)
	}
	// Both URL forms a client may produce, from one block.
	if !strings.Contains(cm, "location = /mcp/ { rewrite ^ /mcp last; }") {
		t.Fatalf("the trailing-slash form is not served:\n%s", cm)
	}
	// SSE has to stream, or a long tool call looks like a hang.
	if !strings.Contains(cm, "proxy_buffering    off;") {
		t.Fatalf("the shim buffers the gateway's stream:\n%s", cm)
	}
}

// The patch merges into somebody else's Deployment. Its two lists merge by
// name, which is what makes applying it twice idempotent.
func TestTheShimPatchIsAStrategicMerge(t *testing.T) {
	_, patch, err := GenerateSidecar(shim())
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Spec struct {
			Template struct {
				Spec struct {
					Containers []struct {
						Name  string `yaml:"name"`
						Image string `yaml:"image"`
					} `yaml:"containers"`
					Volumes []struct {
						Name string `yaml:"name"`
					} `yaml:"volumes"`
				} `yaml:"spec"`
			} `yaml:"template"`
		} `yaml:"spec"`
	}
	if err := yaml.Unmarshal([]byte(patch), &doc); err != nil {
		t.Fatalf("the patch is not YAML: %v\n%s", err, patch)
	}
	containers := doc.Spec.Template.Spec.Containers
	if len(containers) != 1 || containers[0].Name != SidecarContainer {
		t.Fatalf("the patch adds %d containers: %+v", len(containers), containers)
	}
	if len(doc.Spec.Template.Spec.Volumes) != 2 {
		t.Fatalf("the patch adds %d volumes, want the config and the authority",
			len(doc.Spec.Template.Spec.Volumes))
	}
	// A patch is not an object. Emitting one with apiVersion/kind would
	// invite `kubectl apply -f`, which would replace the Deployment's
	// whole pod spec with these two entries.
	for _, objectOnly := range []string{"apiVersion:", "kind: Deployment"} {
		if strings.Contains(patch, objectOnly) {
			t.Fatalf("the patch looks like an object and could be applied: %q", objectOnly)
		}
	}
}

// The ConfigMap IS an object, and kmx applies it.
func TestTheShimConfigMapIsAnObject(t *testing.T) {
	cm, _, err := GenerateSidecar(shim())
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		APIVersion string `yaml:"apiVersion"`
		Kind       string `yaml:"kind"`
		Metadata   struct {
			Name      string `yaml:"name"`
			Namespace string `yaml:"namespace"`
		} `yaml:"metadata"`
		Data map[string]string `yaml:"data"`
	}
	if err := yaml.Unmarshal([]byte(cm), &doc); err != nil {
		t.Fatalf("the ConfigMap is not YAML: %v\n%s", err, cm)
	}
	if doc.Kind != "ConfigMap" || doc.Metadata.Namespace != "acme" {
		t.Fatalf("unexpected object: %+v", doc)
	}
	// The key nginx's entrypoint renders. Any other name and the shim
	// starts with the stock config and refuses every request as a 404.
	if _, ok := doc.Data["default.conf.template"]; !ok {
		t.Fatalf("the config is not under the key nginx renders: %v", doc.Data)
	}
}

func TestTheShimRefusesInputItCannotBuildFrom(t *testing.T) {
	for name, mutate := range map[string]func(*SidecarSpec){
		"no upstream":     func(s *SidecarSpec) { s.Upstream = "" },
		"bad namespace":   func(s *SidecarSpec) { s.Namespace = "Acme Corp" },
		"no deployment":   func(s *SidecarSpec) { s.Deployment = "" },
		"bad secret name": func(s *SidecarSpec) { s.Secret = "kaimahi/warehouse" },
		"committed name":  func(s *SidecarSpec) { s.Upstream = "slack" },
		// Underscores are not object names — and a value shaped like a
		// key is one of the things that reach a --secret flag by mistake.
		"key-shaped secret": func(s *SidecarSpec) { s.Secret = "ghp" + "_" + strings.Repeat("0", 36) },
	} {
		t.Run(name, func(t *testing.T) {
			spec := shim()
			mutate(&spec)
			if _, _, err := GenerateSidecar(spec); err == nil {
				t.Fatal("accepted")
			}
		})
	}
}

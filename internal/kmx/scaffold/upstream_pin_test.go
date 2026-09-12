package scaffold

import (
	"os"
	"strings"
	"testing"
)

// The overlay ConfigMap kmx writes must be the one the proxy mounts.
func TestTheOverlayConfigMapIsTheOneTheProxyMounts(t *testing.T) {
	raw, err := os.ReadFile("../../../k8s/plane/proxy.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "name: "+OverlayConfigMap) {
		t.Fatalf("proxy does not mount %q", OverlayConfigMap)
	}
}

// Pin the surviving model generator, not a removed gateway generator.
func TestTheProxySelectorMatchesTheCommittedBoundary(t *testing.T) {
	raw, err := os.ReadFile("../../../k8s/plane/network-policy.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), ProxySelectorKey+": "+ProxySelectorValue) {
		t.Fatalf("committed boundary does not select %s: %s", ProxySelectorKey, ProxySelectorValue)
	}
	doc, err := GenerateModel(ModelSpec{Name: "house", BaseURL: "http://model.demo:8000", Path: "v1/responses", Protocol: "responses", Classification: "free", ServiceNamespace: "demo", PodPort: 8000, PodLabels: map[string]string{"app": "model"}})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(doc, ProxySelectorKey+": "+ProxySelectorValue) != 2 {
		t.Fatalf("both model policies must pin the proxy label:\n%s", doc)
	}
}

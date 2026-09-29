package scaffold

import (
	"io"
	"os"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
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

func TestPlaneEgressAllowsBothOrkaControllerLabels(t *testing.T) {
	raw, err := os.ReadFile("../../../k8s/plane/network-policy.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var chart, legacy bool
	decoder := yaml.NewDecoder(strings.NewReader(string(raw)))
	for {
		var policy struct {
			Metadata struct {
				Name string `yaml:"name"`
			} `yaml:"metadata"`
			Spec struct {
				Egress []struct {
					To []struct {
						NamespaceSelector struct {
							MatchLabels map[string]string `yaml:"matchLabels"`
						} `yaml:"namespaceSelector"`
						PodSelector struct {
							MatchLabels map[string]string `yaml:"matchLabels"`
						} `yaml:"podSelector"`
					} `yaml:"to"`
					Ports []struct {
						Protocol string `yaml:"protocol"`
						Port     int    `yaml:"port"`
					} `yaml:"ports"`
				} `yaml:"egress"`
			} `yaml:"spec"`
		}
		err := decoder.Decode(&policy)
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if policy.Metadata.Name != "kaimahi-proxy" {
			continue
		}
		for _, rule := range policy.Spec.Egress {
			port8080 := false
			for _, port := range rule.Ports {
				port8080 = port8080 || port.Protocol == "TCP" && port.Port == 8080
			}
			if !port8080 {
				continue
			}
			for _, peer := range rule.To {
				if peer.NamespaceSelector.MatchLabels["kubernetes.io/metadata.name"] != "orka-system" || peer.PodSelector.MatchLabels["app.kubernetes.io/name"] != "orka" {
					continue
				}
				chart = chart || peer.PodSelector.MatchLabels["app.kubernetes.io/component"] == "controller"
				legacy = legacy || peer.PodSelector.MatchLabels["control-plane"] == "controller-manager"
			}
		}
	}
	if !chart || !legacy {
		t.Fatalf("plane egress missing v0.2.0 chart or v0.1.3 controller selector (chart=%t legacy=%t)", chart, legacy)
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

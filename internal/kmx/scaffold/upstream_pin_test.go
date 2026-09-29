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
	orkaPeers := 0
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
			for _, peer := range rule.To {
				if peer.NamespaceSelector.MatchLabels["kubernetes.io/metadata.name"] != "orka-system" {
					continue
				}
				orkaPeers++
				if len(rule.To) != 2 || len(rule.Ports) != 1 || rule.Ports[0].Protocol != "TCP" || rule.Ports[0].Port != 8080 ||
					len(peer.NamespaceSelector.MatchLabels) != 1 || len(peer.PodSelector.MatchLabels) != 2 || peer.PodSelector.MatchLabels["app.kubernetes.io/name"] != "orka" {
					t.Fatal("Orka egress must have exactly the two named controller peers on TCP 8080")
				}
				switch {
				case peer.PodSelector.MatchLabels["app.kubernetes.io/component"] == "controller" && peer.PodSelector.MatchLabels["control-plane"] == "":
					chart = true
				case peer.PodSelector.MatchLabels["control-plane"] == "controller-manager" && peer.PodSelector.MatchLabels["app.kubernetes.io/component"] == "":
					legacy = true
				default:
					t.Fatal("Orka egress selected an unrelated pod")
				}
			}
		}
	}
	if orkaPeers != 2 || !chart || !legacy {
		t.Fatalf("plane egress must select exactly the two Orka controller roles (peers=%d chart=%t legacy=%t)", orkaPeers, chart, legacy)
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

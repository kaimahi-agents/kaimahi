package app

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/config"
	"go.yaml.in/yaml/v3"
)

// The retained archive is the original source package, not a fresh timestamped
// Helm package. OCI fixtures contain metadata only, never image layers.
func offlineRestrictedInstallFixture(t *testing.T) (string, map[string]any) {
	t.Helper()
	dir, m := offlineInstallFixture(t)
	raw, p := restrictedProfileFixture(t)
	fixture, err := os.ReadFile("testdata/offline-7916-install.json")
	if err != nil {
		t.Fatal(err)
	}
	var f struct {
		Files       map[string]string
		ChartChunks []string
	}
	if err = json.Unmarshal(fixture, &f); err != nil {
		t.Fatal(err)
	}
	for name, body := range f.Files {
		if err = os.WriteFile(filepath.Join(dir, name), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	var chart []byte
	for _, chunk := range f.ChartChunks {
		b, err := base64.StdEncoding.DecodeString(chunk)
		if err != nil {
			t.Fatal(err)
		}
		chart = append(chart, b...)
	}
	if offlineHash(chart) != p.Install.Chart.SHA256 {
		t.Fatal("retained source package changed")
	}
	if err = os.WriteFile(filepath.Join(dir, "chart.tgz"), chart, 0600); err != nil {
		t.Fatal(err)
	}
	m["profile"] = p.Profile
	m["profileSha256"] = offlineHash(raw)
	m["orka"] = map[string]any{"mode": "source", "identity": p.Orka.Identity}
	m["chart"] = map[string]any{"path": "chart.tgz", "sha256": p.Install.Chart.SHA256}
	m["artifacts"] = map[string]any{"path": "images.json", "sha256": p.Install.Artifacts.SHA256}
	rows := []any{}
	for _, name := range strings.Fields("controller ai-worker general-worker publisher session-loader") {
		pin := p.Install.Images[name]
		child := pin.Platforms[runtime.GOARCH]
		prefix := name + "-" + runtime.GOARCH
		rows = append(rows, map[string]any{"component": name, "repository": pin.Repository, "indexPath": prefix + "-index.json", "indexDigest": pin.IndexDigest, "manifestPath": prefix + "-manifest.json", "manifestDigest": child.ManifestDigest, "configPath": prefix + "-config.json", "configDigest": child.ConfigDigest, "pullPolicy": "IfNotPresent"})
	}
	m["images"] = rows
	offlineWriteManifest(t, dir, m)
	return dir, m
}

// Full install loading exercises the catalog, archive, descriptor and native OCI
// evidence. It does not import images, contact a cluster or configure a provider.
func TestOfflineRestrictedInstallBundle(t *testing.T) {
	dir, m := offlineRestrictedInstallFixture(t)
	b, err := loadOfflineInstallBundle(dir, offlineTestIdentity())
	if err != nil {
		t.Fatal(err)
	}
	var got offlineValues
	if err = json.Unmarshal(b.install.values, &got); err != nil {
		t.Fatal(err)
	}
	if got.Workers.AI.RBACProfile != "restricted" {
		t.Fatal("install lost restricted values")
	}
	for _, field := range []string{"chart", "artifacts"} {
		t.Run(field, func(t *testing.T) {
			row := m[field].(map[string]any)
			old := row["sha256"]
			row["sha256"] = strings.Repeat("f", 64)
			offlineWriteManifest(t, dir, m)
			if _, err := loadOfflineInstallBundle(dir, offlineTestIdentity()); err == nil {
				t.Fatal("unreviewed install pin accepted", field)
			}
			row["sha256"] = old
			offlineWriteManifest(t, dir, m)
		})
	}
}

// Both architecture inventories must retain real, linked OCI bytes even when
// the test host can install only one architecture. This reads no image layers.
func TestOfflineRestrictedInstallOCIPlatforms(t *testing.T) {
	dir, _ := offlineRestrictedInstallFixture(t)
	_, p := restrictedProfileFixture(t)
	for _, arch := range []string{"amd64", "arm64"} {
		for name, pin := range p.Install.Images {
			t.Run(arch+"/"+name, func(t *testing.T) {
				prefix := name + "-" + arch
				read := func(suffix string) []byte {
					t.Helper()
					raw, err := os.ReadFile(filepath.Join(dir, prefix+"-"+suffix+".json"))
					if err != nil {
						t.Fatal(err)
					}
					return raw
				}
				child := pin.Platforms[arch]
				row := offlineNodeRow{Repository: pin.Repository, IndexDigest: &pin.IndexDigest, ManifestDigest: child.ManifestDigest, ConfigDigest: child.ConfigDigest}
				if _, err := validateOfflineOCI(row, offlinePlatform{OS: "linux", Architecture: arch}, read("index"), read("manifest"), read("config")); err != nil {
					t.Fatal(err)
				}
			})
		}
	}
}

// Actual offline Helm rendering covers both native image sets and the security
// switches in the source-packaged chart, rather than a synthetic render stub.
func TestOfflineRestrictedInstallActualHelm(t *testing.T) {
	if _, err := exec.LookPath("helm"); err != nil {
		t.Skip("guest supplied Helm unavailable")
	}
	dir, _ := offlineRestrictedInstallFixture(t)
	b, err := loadOfflineInstallBundle(dir, offlineTestIdentity())
	if err != nil {
		t.Fatal(err)
	}
	_, p := restrictedProfileFixture(t)
	a := New(&config.Config{KubeContext: "kind-test"}).offlineHelmTarget()
	if err = a.renderOfflineChart(b.install); err != nil {
		t.Fatal(err)
	}
	chart, cleanup, err := offlineChartFile(b.install.chart)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	for _, arch := range []string{"amd64", "arm64"} {
		t.Run(arch, func(t *testing.T) {
			values, err := json.Marshal(p.Install.Values[arch])
			if err != nil {
				t.Fatal(err)
			}
			raw, err := a.offlineInput(values, "helm", "template", orkaRelease, chart, "-n", OrkaNamespace, "--kube-context", a.Cfg.KubeContext, "-f", "-")
			if err != nil {
				t.Fatal(err)
			}
			images := append([]offlineImageRow(nil), b.install.images...)
			for i := range images {
				images[i].ManifestDigest = p.Install.Images[images[i].Component].Platforms[arch].ManifestDigest
			}
			workloads, err := offlineRenderAudit(raw, images)
			if err != nil {
				t.Fatal(err)
			}
			n := 0
			for _, arg := range workloads["orka-api-controller"].Containers[0].Args {
				if strings.HasPrefix(arg, "--context-token-authz-mode") {
					if arg != "--context-token-authz-mode=enforce" {
						t.Fatal("authorization is not enforced", arg)
					}
					n++
				}
			}
			if n != 1 {
				t.Fatal("enforcement flag must occur exactly once", n)
			}
			offlineAssertRestrictedRole(t, raw)
		})
	}
}

// Assert permission semantics, so a correctly named but privileged role fails.
func offlineAssertRestrictedRole(t *testing.T, raw []byte) {
	t.Helper()
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	found := false
	for {
		var obj struct {
			Kind     string
			Metadata struct{ Name string }
			Rules    []struct{ Verbs, Resources []string }
		}
		err := dec.Decode(&obj)
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if obj.Kind != "ClusterRole" || obj.Metadata.Name != "orka-api-ai-worker-role" {
			continue
		}
		if found || len(obj.Rules) == 0 {
			t.Fatal("duplicate/empty restricted role")
		}
		found = true
		for _, rule := range obj.Rules {
			for _, v := range rule.Verbs {
				if v != "get" && v != "list" && v != "watch" {
					t.Fatal("restricted role can mutate", v)
				}
			}
			for _, r := range rule.Resources {
				if r == "secrets" || r == "serviceaccounts/token" || r == "*" {
					t.Fatal("restricted role can read credentials", r)
				}
			}
		}
	}
	if !found {
		t.Fatal("restricted worker role is absent")
	}
}

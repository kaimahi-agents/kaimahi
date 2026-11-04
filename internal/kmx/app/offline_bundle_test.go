package app

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func offlineTestHash(b []byte) string { return fmt.Sprintf("%x", sha256.Sum256(b)) }

func offlineTestIdentity() offlineKMXIdentity {
	return offlineKMXIdentity{Version: "v0.0.0-dev+ac75e9b9935c", Revision: "ac75e9b9935c6756967030af58fd234fa182cade", SHA256: strings.Repeat("a", 64)}
}

func offlineFixture(t *testing.T) (string, map[string]any) {
	t.Helper()
	if runtime.GOOS != "linux" || runtime.GOARCH != "amd64" && runtime.GOARCH != "arm64" {
		t.Skip("Linux-native offline bundle")
	}
	raw, err := os.ReadFile("testdata/offline-node.json")
	if err != nil {
		t.Fatal(err)
	}
	var evidence struct {
		Index       string
		IndexDigest string
		Platforms   map[string]struct{ Manifest, Config, ManifestDigest, ConfigDigest string }
	}
	if err := json.Unmarshal(raw, &evidence); err != nil {
		t.Fatal(err)
	}
	p := evidence.Platforms[runtime.GOARCH]
	dir := t.TempDir()
	for path, body := range map[string]string{"index.json": evidence.Index, "manifest.json": p.Manifest, "config.json": p.Config} {
		if err := os.WriteFile(filepath.Join(dir, path), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	profile, err := os.ReadFile("offline_profiles/orka-59703d6e-native-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	identity := offlineTestIdentity()
	row := map[string]any{"repository": "kindest/node", "localRef": "kindest/node:bridge-" + p.ManifestDigest[7:23] + "-" + runtime.GOARCH,
		"indexPath": "index.json", "indexDigest": evidence.IndexDigest, "manifestPath": "manifest.json", "manifestDigest": p.ManifestDigest, "configPath": "config.json", "configDigest": p.ConfigDigest}
	components := map[string]any{}
	for _, name := range strings.Fields("controller ai-worker general-worker publisher session-loader scm-egress-proxy store") {
		components[name] = true
	}
	for _, name := range strings.Fields("acp-codex-runtime acp-claude-runtime acp-copilot-runtime acp-opencode-runtime provider-proxy toolboxes execution-workspace-dispatch substrate-mcp-tools connectors dashboard agent-hosts ingress") {
		components[name] = false
	}
	images := []any{}
	repos := []string{"ghcr.io/orka-agents/orka", "ghcr.io/orka-agents/orka/ai-worker", "ghcr.io/orka-agents/orka/general-worker", "ghcr.io/orka-agents/orka/workspace-publisher", "docker.io/library/busybox"}
	for i, name := range strings.Fields("controller ai-worker general-worker publisher session-loader") {
		images = append(images, map[string]any{"component": name, "repository": repos[i], "indexPath": nil, "indexDigest": nil,
			"manifestPath": "absent/" + name + ".json", "manifestDigest": "sha256:" + strings.Repeat("b", 64), "configPath": "absent/" + name + "-config.json", "configDigest": "sha256:" + strings.Repeat("c", 64), "pullPolicy": "IfNotPresent"})
	}
	m := map[string]any{"schemaVersion": "kmx-offline/v1", "profile": "orka-59703d6e-native/v1", "profileSha256": offlineTestHash(profile),
		"platform":  map[string]any{"os": "linux", "architecture": runtime.GOARCH},
		"kmx":       map[string]any{"version": identity.Version, "revision": identity.Revision, "sha256": identity.SHA256},
		"orka":      map[string]any{"mode": "source", "identity": "59703d6eac1760aa2dc5fed4dee9dcb74c6e5186"},
		"chart":     map[string]any{"path": "absent/chart.tgz", "sha256": strings.Repeat("d", 64)},
		"artifacts": map[string]any{"path": "absent/images.json", "sha256": strings.Repeat("e", 64)},
		"node":      row, "components": components, "images": images}
	offlineWriteManifest(t, dir, m)
	return dir, m
}

func offlineWriteManifest(t *testing.T, dir string, m map[string]any) {
	t.Helper()
	raw, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "install.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
}

// Removing any shape, catalog, local-file or build-identity check must reject a
// different row here. Install-only files are deliberately absent even on success.
func TestOfflineBundleValidation(t *testing.T) {
	tests := []struct {
		name   string
		change func(*testing.T, string, map[string]any, *offlineKMXIdentity)
	}{
		{"valid node only", nil},
		{"unknown", func(t *testing.T, d string, m map[string]any, i *offlineKMXIdentity) { m["extra"] = true }},
		{"missing", func(t *testing.T, d string, m map[string]any, i *offlineKMXIdentity) { delete(m, "chart") }},
		{"null required", func(t *testing.T, d string, m map[string]any, i *offlineKMXIdentity) { m["chart"] = nil }},
		{"case alias", func(t *testing.T, d string, m map[string]any, i *offlineKMXIdentity) {
			m["SchemaVersion"] = m["schemaVersion"]
			delete(m, "schemaVersion")
		}},
		{"profile", func(t *testing.T, d string, m map[string]any, i *offlineKMXIdentity) { m["profile"] = "other" }},
		{"profile hash", func(t *testing.T, d string, m map[string]any, i *offlineKMXIdentity) {
			m["profileSha256"] = strings.Repeat("f", 64)
		}},
		{"schema", func(t *testing.T, d string, m map[string]any, i *offlineKMXIdentity) {
			m["schemaVersion"] = "kmx-offline/v2"
		}},
		{"source", func(t *testing.T, d string, m map[string]any, i *offlineKMXIdentity) {
			m["orka"].(map[string]any)["identity"] = strings.Repeat("a", 40)
		}},
		{"platform", func(t *testing.T, d string, m map[string]any, i *offlineKMXIdentity) {
			m["platform"].(map[string]any)["architecture"] = "riscv64"
		}},
		{"non-native", func(t *testing.T, d string, m map[string]any, i *offlineKMXIdentity) {
			m["platform"].(map[string]any)["os"] = "darwin"
		}},
		{"dirty", func(t *testing.T, d string, m map[string]any, i *offlineKMXIdentity) { i.Modified = true }},
		{"unknown build", func(t *testing.T, d string, m map[string]any, i *offlineKMXIdentity) { i.Version = "unknown" }},
		{"absent revision", func(t *testing.T, d string, m map[string]any, i *offlineKMXIdentity) { i.Revision = "" }},
		{"wrong version", func(t *testing.T, d string, m map[string]any, i *offlineKMXIdentity) {
			m["kmx"].(map[string]any)["version"] = "v9.9.9"
		}},
		{"wrong revision", func(t *testing.T, d string, m map[string]any, i *offlineKMXIdentity) {
			m["kmx"].(map[string]any)["revision"] = strings.Repeat("a", 40)
		}},
		{"wrong executable", func(t *testing.T, d string, m map[string]any, i *offlineKMXIdentity) {
			m["kmx"].(map[string]any)["sha256"] = strings.Repeat("b", 64)
		}},
		{"enable disabled", func(t *testing.T, d string, m map[string]any, i *offlineKMXIdentity) {
			m["components"].(map[string]any)["dashboard"] = true
		}},
		{"missing component", func(t *testing.T, d string, m map[string]any, i *offlineKMXIdentity) {
			delete(m["components"].(map[string]any), "dashboard")
		}},
		{"extra component", func(t *testing.T, d string, m map[string]any, i *offlineKMXIdentity) {
			m["components"].(map[string]any)["unknown"] = false
		}},
		{"null component", func(t *testing.T, d string, m map[string]any, i *offlineKMXIdentity) {
			m["components"].(map[string]any)["dashboard"] = nil
		}},
		{"missing image", func(t *testing.T, d string, m map[string]any, i *offlineKMXIdentity) {
			m["images"] = m["images"].([]any)[:4]
		}},
		{"duplicate image", func(t *testing.T, d string, m map[string]any, i *offlineKMXIdentity) {
			rows := m["images"].([]any)
			rows[4] = rows[0]
		}},
		{"unknown image field", func(t *testing.T, d string, m map[string]any, i *offlineKMXIdentity) {
			m["images"].([]any)[0].(map[string]any)["extra"] = 1
		}},
		{"bad policy", func(t *testing.T, d string, m map[string]any, i *offlineKMXIdentity) {
			m["images"].([]any)[0].(map[string]any)["pullPolicy"] = "Always"
		}},
		{"bad digest", func(t *testing.T, d string, m map[string]any, i *offlineKMXIdentity) {
			m["images"].([]any)[0].(map[string]any)["configDigest"] = "sha256:AB"
		}},
		{"traversal node", func(t *testing.T, d string, m map[string]any, i *offlineKMXIdentity) {
			m["node"].(map[string]any)["configPath"] = "../escape"
		}},
		{"traversal unread chart", func(t *testing.T, d string, m map[string]any, i *offlineKMXIdentity) {
			m["chart"].(map[string]any)["path"] = "../escape"
		}},
		{"traversal unread image", func(t *testing.T, d string, m map[string]any, i *offlineKMXIdentity) {
			m["images"].([]any)[0].(map[string]any)["manifestPath"] = "https://example.test/x"
		}},
		{"null pairing", func(t *testing.T, d string, m map[string]any, i *offlineKMXIdentity) {
			m["node"].(map[string]any)["indexPath"] = nil
		}},
		{"null pairing image", func(t *testing.T, d string, m map[string]any, i *offlineKMXIdentity) {
			m["images"].([]any)[0].(map[string]any)["indexDigest"] = "sha256:" + strings.Repeat("f", 64)
		}},
		{"foreign node", func(t *testing.T, d string, m map[string]any, i *offlineKMXIdentity) {
			cb, err := os.ReadFile(filepath.Join(d, "config.json"))
			if err != nil {
				t.Fatal(err)
			}
			var config, manifest, index map[string]any
			if err := json.Unmarshal(cb, &config); err != nil {
				t.Fatal(err)
			}
			config["created"] = "2000-01-01T00:00:00Z"
			cb, _ = json.Marshal(config)
			mb, _ := os.ReadFile(filepath.Join(d, "manifest.json"))
			json.Unmarshal(mb, &manifest)
			descriptor := manifest["config"].(map[string]any)
			descriptor["digest"], descriptor["size"] = "sha256:"+offlineTestHash(cb), len(cb)
			mb, _ = json.Marshal(manifest)
			ib, _ := os.ReadFile(filepath.Join(d, "index.json"))
			json.Unmarshal(ib, &index)
			for _, child := range index["manifests"].([]any) {
				c := child.(map[string]any)
				if c["platform"].(map[string]any)["architecture"] == runtime.GOARCH {
					c["digest"], c["size"] = "sha256:"+offlineTestHash(mb), len(mb)
				}
			}
			ib, _ = json.Marshal(index)
			row := m["node"].(map[string]any)
			row["indexDigest"], row["manifestDigest"], row["configDigest"] = "sha256:"+offlineTestHash(ib), "sha256:"+offlineTestHash(mb), "sha256:"+offlineTestHash(cb)
			row["localRef"] = "kindest/node:bridge-" + offlineTestHash(mb)[:16] + "-" + runtime.GOARCH
			for path, body := range map[string][]byte{"index.json": ib, "manifest.json": mb, "config.json": cb} {
				if err := os.WriteFile(filepath.Join(d, path), body, 0600); err != nil {
					t.Fatal(err)
				}
			}
		}},
		{"wrong local ref", func(t *testing.T, d string, m map[string]any, i *offlineKMXIdentity) {
			m["node"].(map[string]any)["localRef"] = "kindest/node:latest"
		}},
		{"corrupt index", func(t *testing.T, d string, m map[string]any, i *offlineKMXIdentity) {
			os.WriteFile(filepath.Join(d, "index.json"), []byte("{}"), 0600)
		}},
		{"corrupt manifest", func(t *testing.T, d string, m map[string]any, i *offlineKMXIdentity) {
			os.WriteFile(filepath.Join(d, "manifest.json"), []byte("{}"), 0600)
		}},
		{"corrupt config", func(t *testing.T, d string, m map[string]any, i *offlineKMXIdentity) {
			os.WriteFile(filepath.Join(d, "config.json"), []byte("{}"), 0600)
		}},
		{"symlink file", func(t *testing.T, d string, m map[string]any, i *offlineKMXIdentity) {
			os.Rename(filepath.Join(d, "config.json"), filepath.Join(d, "real.json"))
			if err := os.Symlink("real.json", filepath.Join(d, "config.json")); err != nil {
				t.Fatal(err)
			}
		}},
		{"symlink ancestor", func(t *testing.T, d string, m map[string]any, i *offlineKMXIdentity) {
			if err := os.Symlink(d, filepath.Join(d, "link")); err != nil {
				t.Fatal(err)
			}
			m["node"].(map[string]any)["configPath"] = "link/config.json"
		}},
		{"oversize", func(t *testing.T, d string, m map[string]any, i *offlineKMXIdentity) {
			os.WriteFile(filepath.Join(d, "config.json"), []byte(strings.Repeat(" ", (1<<20)+1)), 0600)
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			d, m := offlineFixture(t)
			id := offlineTestIdentity()
			if tc.change != nil {
				tc.change(t, d, m, &id)
				offlineWriteManifest(t, d, m)
			}
			b, err := loadOfflineNodeBundle(d, id)
			if tc.change == nil {
				if err != nil || b == nil {
					t.Fatalf("node-only bundle: %v", err)
				}
			} else {
				want := map[string]string{
					"unknown": "complete closed shape", "missing": "complete closed shape",
					"null required": "complete closed shape", "case alias": "complete closed shape",
					"profile": "profile hash or source identity", "profile hash": "profile hash or source identity",
					"schema": "profile hash or source identity", "source": "profile hash or source identity",
					"platform": "platform must match", "non-native": "platform must match",
					"dirty": "known clean kmx", "unknown build": "known clean kmx", "absent revision": "known clean kmx",
					"wrong version": "executable hash mismatch", "wrong revision": "executable hash mismatch",
					"wrong executable": "executable hash mismatch", "enable disabled": "unsupported component dashboard",
					"missing component": "complete fixed map", "extra component": "complete fixed map",
					"null component": "complete closed shape", "missing image": "exactly five",
					"duplicate image": "duplicate image component", "unknown image field": "complete closed shape",
					"bad policy": "image component/repository/policy", "bad digest": "image controller: invalid OCI paths",
					"traversal node": "invalid OCI paths", "traversal unread chart": "invalid chart/artifacts path",
					"traversal unread image": "image controller: invalid OCI paths", "null pairing": "index null pairing",
					"null pairing image": "image controller: invalid OCI paths", "foreign node": "compiled offline profile",
					"wrong local ref": "localRef does not match", "corrupt index": "index bytes differ",
					"corrupt manifest": "manifest/config bytes differ", "corrupt config": "manifest/config bytes differ",
					"symlink file": "node config: config.json:", "symlink ancestor": "node config: link/config.json:",
					"oversize": "node config: config.json exceeds 1 MiB",
				}[tc.name]
				if want == "" || err == nil || !strings.Contains(err.Error(), want) {
					t.Fatalf("want refusal %q, got %v", want, err)
				}
			}
		})
	}
	for _, suffix := range []string{",\"schemaVersion\":\"kmx-offline/v1\"}", "} {}"} {
		t.Run("duplicate or trailing "+suffix, func(t *testing.T) {
			d, _ := offlineFixture(t)
			p := filepath.Join(d, "install.json")
			b, _ := os.ReadFile(p)
			os.WriteFile(p, append(b[:len(b)-1], []byte(suffix)...), 0600)
			_, err := loadOfflineNodeBundle(d, offlineTestIdentity())
			want := "install.json: invalid or duplicate JSON"
			if suffix == "} {}" {
				want = "install.json: trailing JSON data"
			}
			if err == nil || !strings.Contains(err.Error(), want) {
				t.Fatalf("want %q, got %v", want, err)
			}
		})
	}
}

// Both spellings address one decoded field. The pinned value is last in either
// alias order, so accepting last-wins JSON would incorrectly accept this proof.
func TestOfflineBundleEngineJSONAliases(t *testing.T) {
	for _, engine := range []string{"docker", "podman"} {
		for _, field := range []string{"image ID", "image OS unicode", "image rootfs", "image layers unicode", "container image", "container config", "container labels", "container digest"} {
			for _, reverse := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/%s/reverse=%t", engine, field, reverse), func(t *testing.T) {
					a, _, b, log := offlineClusterApp(t, engine)
					image := offlineImageFixture(t, b, b.node.ConfigDigest)
					container := fmt.Sprintf(`[{"Image":%q,"Name":"/test-control-plane","Config":{"Labels":{"io.x-k8s.kind.cluster":"test"}},"ImageManifestDescriptor":{"Digest":%q}}]`, b.node.ConfigDigest, b.node.ManifestDigest)
					var key, alias, valid, foreign string
					switch field {
					case "image ID":
						key, alias, valid, foreign = "Id", "ID", fmt.Sprintf("%q", b.node.ConfigDigest), `"foreign"`
					case "image OS unicode":
						key, alias, valid, foreign = "Os", "Oſ", `"linux"`, `"darwin"`
					case "image rootfs":
						key, alias, foreign = "RootFS", "rootfs", `{"Type":"foreign"}`
						layers, err := json.Marshal(b.diffIDs)
						if err != nil {
							t.Fatal(err)
						}
						valid = fmt.Sprintf(`{"Layers":%s,"Type":"layers"}`, layers)
					case "image layers unicode":
						key, alias, foreign = "Layers", "Layerſ", `[]`
						layers, err := json.Marshal(b.diffIDs)
						if err != nil {
							t.Fatal(err)
						}
						valid = string(layers)
					case "container image":
						key, alias, valid, foreign = "Image", "image", fmt.Sprintf("%q", b.node.ConfigDigest), `"foreign"`
					case "container config":
						key, alias, valid, foreign = "Config", "config", `{"Labels":{"io.x-k8s.kind.cluster":"test"}}`, `{"Labels":{"io.x-k8s.kind.cluster":"foreign"}}`
					case "container labels":
						key, alias, valid, foreign = "Labels", "labels", `{"io.x-k8s.kind.cluster":"test"}`, `{"io.x-k8s.kind.cluster":"foreign"}`
					case "container digest":
						key, alias, valid, foreign = "Digest", "digest", fmt.Sprintf("%q", b.node.ManifestDigest), `"foreign"`
					}
					first, last := alias, key
					if reverse {
						first, last = key, alias
					}
					replace := func(raw string) string {
						old := fmt.Sprintf(`%q:%s`, key, valid)
						if !strings.Contains(raw, old) {
							t.Fatalf("fixture lacks %s", old)
						}
						return strings.Replace(raw, old, fmt.Sprintf(`%q:%s,%q:%s`, first, foreign, last, valid), 1)
					}
					isContainer := strings.HasPrefix(field, "container")
					if isContainer {
						container = replace(container)
					} else {
						image = replace(image)
					}
					t.Setenv("OFFLINE_ALIAS_IMAGE", image)
					t.Setenv("OFFLINE_ALIAS_CONTAINER", container)
					fakeTool(t, filepath.Dir(log), engine, `case "$1 $2" in
"image inspect") printf '%s' "$OFFLINE_ALIAS_IMAGE" ;;
"container inspect") printf '%s' "$OFFLINE_ALIAS_CONTAINER" ;;
"ps -a") echo test-control-plane ;;
*) exit 98;; esac`)
					var err error
					if isContainer {
						_, err = a.verifyOfflineClusterNodes(b)
					} else {
						err = a.verifyOfflineNodeImage(b, b.node.LocalRef)
					}
					if err == nil {
						t.Fatal("ambiguous aliases accepted with the pinned value last")
					}
					want := "malformed/unknown engine inspection"
					if isContainer {
						want = "unknown or mismatched immutable image/cluster identity"
					}
					if !strings.Contains(err.Error(), want) {
						t.Fatalf("want ambiguous JSON refusal %q, got %v", want, err)
					}
				})
			}
		}
	}
}

func TestOfflineBundleOpenJSON(t *testing.T) {
	var rows []struct {
		ID     string `json:"Id"`
		Config struct{ Labels map[string]string }
	}
	raw := []byte(`[{"ID":"pinned","config":{"labels":{"Foo":"upper","foo":"lower"}},"Extra":123,"extra":{"Id":"one","ID":"two"}}]`)
	if err := offlineJSON(raw, &rows, false); err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].ID != "pinned" || rows[0].Config.Labels["Foo"] != "upper" || rows[0].Config.Labels["foo"] != "lower" {
		t.Fatalf("valid aliases, unknown fields or case-distinct map keys lost: %+v", rows)
	}
}

func TestOfflineBundleControlPaths(t *testing.T) {
	for _, path := range []string{"node/indexPath", "node/manifestPath", "node/configPath", "chart/path", "artifacts/path", "image/indexPath", "image/manifestPath", "image/configPath"} {
		for _, control := range []string{"\x1b", "\n", "\u0085"} {
			t.Run(fmt.Sprintf("%s/%q", path, control), func(t *testing.T) {
				a, dir, _, log := offlineClusterApp(t, "docker")
				raw, err := os.ReadFile(filepath.Join(dir, "install.json"))
				if err != nil {
					t.Fatal(err)
				}
				var m map[string]any
				if err := json.Unmarshal(raw, &m); err != nil {
					t.Fatal(err)
				}
				group, key, _ := strings.Cut(path, "/")
				var row map[string]any
				if group == "image" {
					row = m["images"].([]any)[0].(map[string]any)
					if key == "indexPath" {
						row["indexDigest"] = "sha256:" + strings.Repeat("a", 64)
					}
				} else {
					row = m[group].(map[string]any)
				}
				row[key] = "absent" + control + ".json"
				offlineWriteManifest(t, dir, m)
				err = a.UpWithOptions(UpOptions{Step: "cluster", OfflineBundle: dir})
				if err == nil || !strings.Contains(err.Error(), "invalid") {
					t.Errorf("want path syntax refusal, got %v", err)
				}
				if err != nil && strings.Contains(err.Error(), control) {
					t.Errorf("raw terminal control in diagnostic: %q", err.Error())
				}
				if calls, err := os.ReadFile(log); !os.IsNotExist(err) {
					t.Fatalf("tools reached before path refusal: calls=%q err=%v", calls, err)
				}
			})
		}
	}
}

func TestOfflineBundleUnicodePaths(t *testing.T) {
	dir, m := offlineFixture(t)
	node := m["node"].(map[string]any)
	for _, key := range []string{"indexPath", "manifestPath", "configPath"} {
		old := node[key].(string)
		name := "节点-é-" + old
		if err := os.Rename(filepath.Join(dir, old), filepath.Join(dir, name)); err != nil {
			t.Fatal(err)
		}
		node[key] = name
	}
	m["chart"].(map[string]any)["path"] = "目录/chart.tgz"
	m["artifacts"].(map[string]any)["path"] = "目录/images.json"
	m["images"].([]any)[0].(map[string]any)["manifestPath"] = "目录/manifest.json"
	offlineWriteManifest(t, dir, m)
	if _, err := loadOfflineNodeBundle(dir, offlineTestIdentity()); err != nil {
		t.Fatalf("ordinary Unicode paths refused: %v", err)
	}
}

// Changing catalog checks alone cannot exercise self-consistent bad OCI
// descriptors, because the compiled digest rejects them first. Exercise that
// separate evidence boundary directly without adding a production pin override.
func TestOfflineOCIEvidence(t *testing.T) {
	for _, arch := range []string{"amd64", "arm64"} {
		for _, bad := range []string{"", "config size", "config digest", "child size", "child membership", "config platform", "config os", "ambiguous", "variant", "v8", "rootfs"} {
			t.Run(arch+"/"+bad, func(t *testing.T) {
				raw, err := os.ReadFile("testdata/offline-node.json")
				if err != nil {
					t.Fatal(err)
				}
				var e struct {
					Index     string
					Platforms map[string]struct{ Manifest, Config string }
				}
				json.Unmarshal(raw, &e)
				var index, manifest, config map[string]any
				json.Unmarshal([]byte(e.Index), &index)
				json.Unmarshal([]byte(e.Platforms[arch].Manifest), &manifest)
				json.Unmarshal([]byte(e.Platforms[arch].Config), &config)
				child := index["manifests"].([]any)[0].(map[string]any)
				if arch == "arm64" {
					child = index["manifests"].([]any)[1].(map[string]any)
				}
				switch bad {
				case "config size":
					manifest["config"].(map[string]any)["size"] = 1
				case "child size":
					child["size"] = 1
				case "child membership":
					child["digest"] = "sha256:" + strings.Repeat("f", 64)
				case "config platform":
					config["architecture"] = "riscv64"
				case "config os":
					config["os"] = "darwin"
				case "v8":
					child["platform"].(map[string]any)["variant"] = "v8"
				case "ambiguous":
					index["manifests"] = append(index["manifests"].([]any), child)
				case "variant":
					child["platform"].(map[string]any)["variant"] = "v7"
				case "rootfs":
					config["rootfs"].(map[string]any)["diff_ids"] = []any{}
				}
				cb, _ := json.Marshal(config)
				descriptor := manifest["config"].(map[string]any)
				descriptor["digest"] = "sha256:" + offlineTestHash(cb)
				if bad == "config digest" {
					descriptor["digest"] = "sha256:" + strings.Repeat("f", 64)
				}
				if bad != "config size" {
					descriptor["size"] = len(cb)
				}
				mb, _ := json.Marshal(manifest)
				if bad != "child membership" {
					child["digest"] = "sha256:" + offlineTestHash(mb)
				}
				if bad != "child size" {
					child["size"] = len(mb)
				}
				ib, _ := json.Marshal(index)
				ip, id := "index.json", "sha256:"+offlineTestHash(ib)
				row := offlineNodeRow{IndexPath: &ip, IndexDigest: &id, ManifestDigest: "sha256:" + offlineTestHash(mb), ConfigDigest: "sha256:" + offlineTestHash(cb)}
				_, err = validateOfflineOCI(row, offlinePlatform{OS: "linux", Architecture: arch}, ib, mb, cb)
				if bad == "" || bad == "v8" && arch == "arm64" {
					if err != nil {
						t.Fatalf("valid %s evidence: %v", arch, err)
					}
				} else {
					want := map[string]string{
						"config size": "config descriptor digest/size", "config digest": "config descriptor digest/size", "child size": "index child membership/size",
						"child membership": "index child membership/size", "config platform": "config platform/rootfs", "config os": "config platform/rootfs",
						"ambiguous": "exactly one native platform child", "variant": "exactly one native platform child", "v8": "exactly one native platform child",
						"rootfs": "config platform/rootfs",
					}[bad]
					if err == nil || !strings.Contains(err.Error(), want) {
						t.Fatalf("want %q, got %v", want, err)
					}
				}
			})
		}
	}
}

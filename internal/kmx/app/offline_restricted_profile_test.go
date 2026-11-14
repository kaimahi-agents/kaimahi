package app

import (
	"encoding/json"
	"os"
	"testing"
)

const restrictedProfileID = "orka-7916cd1c-native/v1"
const restrictedSourceSHA = "7916cd1c8c083ade6b10df6ba408f854d930190c"
const cancelProfileID = "orka-3cb1f0df-native/v1"
const cancelSourceSHA = "3cb1f0df36705af9ff81cb8b4e8a0f8d36ee6eea"

// Literal hashes bind the catalog's shipped bytes independently of its selector.
var offlineSourceProfiles = []struct{ key, id, source, hash, tree, chart string }{
	{"7916cd1c", restrictedProfileID, restrictedSourceSHA, "6ab8213cae9f1d58fe59f34b2402f93ed951752269decdf750fc2a170bddf223", "2df37384a46a435ec6f3ec5e93468760771f6c1d", "19c50cff36855b8792b4777c89c598cf33ea70180e677e1a4758fc0211f069ed"},
	{"3cb1f0df", cancelProfileID, cancelSourceSHA, "20647cab1e653394fe7466608a08dd816f05d1a44bc8a8598fa478f7e20fc8f5", "47daa71d98e45d833df5f741437dbe6aaf63c6cb", "11cc020b923dd5bc810a6f3b97f236a6e3123ce8ad80791d912c0af760abcca8"},
}

// Removing the typed AI-only field silently restores the chart's default RBAC.
func TestOfflineRestrictedValuesPreserved(t *testing.T) {
	var values offlineValues
	if err := offlineJSON([]byte(`{"workers":{"ai":{"rbacProfile":"restricted"}}}`), &values, false); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(values)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err = json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	workers := got["workers"].(map[string]any)
	if workers["ai"].(map[string]any)["rbacProfile"] != "restricted" {
		t.Fatal("restricted AI setting lost before Helm", string(raw))
	}
	for _, name := range []string{"general", "sessionLoader"} {
		if _, ok := workers[name].(map[string]any)["rbacProfile"]; ok {
			t.Fatal("AI policy leaked to unrelated worker", name)
		}
	}
}

// Reject aliases before Go's case-insensitive decoder could overwrite a value.
func TestOfflineRestrictedValueAliasesRefused(t *testing.T) {
	var v offlineValues
	if err := offlineJSON([]byte(`{"workers":{"ai":{"image":{"digest":"first"},"Image":{"digest":"second"}}}}`), &v, false); err == nil {
		t.Fatal("embedded image alias bypassed duplicate check")
	}
}

// Read the shipped bytes, whose exact hash is the catalog authority.
func offlineSourceProfileFixture(t *testing.T, key string) ([]byte, offlineProfile) {
	t.Helper()
	raw, err := os.ReadFile("offline_profiles/orka-" + key + "-native-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	var p offlineProfile
	if err = offlineJSON(raw, &p, false); err != nil {
		t.Fatal(err)
	}
	return raw, p
}

// Source metadata describes the package; the compiled bytes authorize its hash.
func TestOfflineRestrictedProfilePins(t *testing.T) {
	for _, profile := range offlineSourceProfiles {
		t.Run(profile.key, func(t *testing.T) {
			raw, p := offlineSourceProfileFixture(t, profile.key)
			if offlineHash(raw) != profile.hash || p.Profile != profile.id || p.Orka.Mode != "source" || p.Orka.Identity != profile.source || p.Install == nil {
				t.Fatal("wrong restricted source")
			}
			var doc map[string]any
			if err := json.Unmarshal(raw, &doc); err != nil {
				t.Fatal(err)
			}
			chart := doc["install"].(map[string]any)["chart"].(map[string]any)
			source, ok := chart["source"].(map[string]any)
			if !ok || source["commit"] != profile.source || source["directory"] != "manifest_staging/charts/orka" || source["gitTree"] != profile.tree || chart["sha256"] != profile.chart {
				t.Fatal("chart source binding missing", chart)
			}
			if p.Install.Chart.Version != "0.0.0-dev" || p.Install.Chart.AppVersion != "v0.0.0-dev" {
				t.Fatal("wrong chart identity")
			}
			for _, arch := range []string{"amd64", "arm64"} {
				b, err := json.Marshal(p.Install.Values[arch])
				if err != nil {
					t.Fatal(err)
				}
				var v map[string]any
				if err = json.Unmarshal(b, &v); err != nil {
					t.Fatal(err)
				}
				if v["workers"].(map[string]any)["ai"].(map[string]any)["rbacProfile"] != "restricted" {
					t.Fatal("restricted not enabled", arch)
				}
				for name, image := range p.Install.Images {
					child := image.Platforms[arch]
					if !offlineDigest(image.IndexDigest) || !offlineDigest(child.ManifestDigest) || !offlineDigest(child.ConfigDigest) {
						t.Fatal("missing OCI platform", name, arch)
					}
				}
			}
			published, err := os.ReadFile("testdata/offline-" + profile.key[:4] + "-images.json")
			if err != nil {
				t.Fatal(err)
			}
			var images struct {
				SourceSHA string            `json:"sourceSHA"`
				Images    map[string]string `json:"images"`
			}
			if err = json.Unmarshal(published, &images); err != nil {
				t.Fatal(err)
			}
			if images.SourceSHA != profile.source || len(images.Images) != 8 || offlineHash(published) != p.Install.Artifacts.SHA256 {
				t.Fatal("published descriptor binding changed")
			}
			for _, name := range []string{"controller", "ai-worker", "general-worker", "publisher"} {
				key := name
				if name == "publisher" {
					key = "workspace-publisher"
				}
				pin := p.Install.Images[name]
				if images.Images[key] != pin.Repository+"@"+pin.IndexDigest {
					t.Fatal("published image changed", name)
				}
			}
			var old offlineProfile
			if err = json.Unmarshal(offlineProfileJSON, &old); err != nil {
				t.Fatal(err)
			}
			if p.Node.IndexDigest != old.Node.IndexDigest || p.Install.Images["session-loader"].IndexDigest != old.Install.Images["session-loader"].IndexDigest {
				t.Fatal("unrelated kind/session loader repinned")
			}
			oldValues, err := json.Marshal(old.Install.Values["amd64"])
			if err != nil {
				t.Fatal(err)
			}
			var oldMap map[string]any
			if err = json.Unmarshal(oldValues, &oldMap); err != nil {
				t.Fatal(err)
			}
			if _, ok := oldMap["workers"].(map[string]any)["ai"].(map[string]any)["rbacProfile"]; ok {
				t.Fatal("old profile values changed")
			}
		})
	}
}

// These are node-only bundles: no chart/image files or external tools are needed.
func TestOfflineRestrictedCatalog(t *testing.T) {
	for _, profile := range offlineSourceProfiles {
		t.Run(profile.key, func(t *testing.T) {
			for _, bad := range []string{"valid", "old-hash", "old-source", "old-id-new-hash", "restricted-id-new-hash", "unknown"} {
				t.Run(bad, func(t *testing.T) {
					dir, m := offlineFixture(t)
					m["profile"] = profile.id
					m["profileSha256"] = profile.hash
					m["orka"] = map[string]any{"mode": "source", "identity": profile.source}
					switch bad {
					case "old-hash":
						m["profileSha256"] = offlineHash(offlineProfileJSON)
					case "old-source":
						m["orka"] = map[string]any{"mode": "source", "identity": "59703d6eac1760aa2dc5fed4dee9dcb74c6e5186"}
					case "old-id-new-hash":
						m["profile"] = "orka-59703d6e-native/v1"
					case "restricted-id-new-hash":
						m["profile"] = restrictedProfileID
						m["profileSha256"] = offlineSourceProfiles[1].hash
					case "unknown":
						m["profile"] = "orka-unknown-native/v1"
					}
					offlineWriteManifest(t, dir, m)
					_, err := loadOfflineNodeBundle(dir, offlineTestIdentity())
					if (err == nil) != (bad == "valid") {
						t.Fatalf("catalog %s: %v", bad, err)
					}
				})
			}
		})
	}
}

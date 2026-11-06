package app

import (
	"encoding/json"
	"os"
	"testing"
)

const restrictedProfileID = "orka-7916cd1c-native/v1"
const restrictedSourceSHA = "7916cd1c8c083ade6b10df6ba408f854d930190c"

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

func TestOfflineRestrictedValueAliasesRefused(t *testing.T) {
	var v offlineValues
	if err := offlineJSON([]byte(`{"workers":{"ai":{"image":{"digest":"first"},"Image":{"digest":"second"}}}}`), &v, false); err == nil {
		t.Fatal("embedded image alias bypassed duplicate check")
	}
}

func restrictedProfileFixture(t *testing.T) ([]byte, offlineProfile) {
	t.Helper()
	raw, err := os.ReadFile("offline_profiles/orka-7916cd1c-native-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	var p offlineProfile
	if err = offlineJSON(raw, &p, false); err != nil {
		t.Fatal(err)
	}
	return raw, p
}

func TestOfflineRestrictedProfilePins(t *testing.T) {
	raw, p := restrictedProfileFixture(t)
	if p.Profile != restrictedProfileID || p.Orka.Mode != "source" || p.Orka.Identity != restrictedSourceSHA || p.Install == nil {
		t.Fatal("wrong restricted source")
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	chart := doc["install"].(map[string]any)["chart"].(map[string]any)
	source, ok := chart["source"].(map[string]any)
	if !ok || source["commit"] != restrictedSourceSHA || source["directory"] != "manifest_staging/charts/orka" || source["gitTree"] != "2df37384a46a435ec6f3ec5e93468760771f6c1d" || chart["sha256"] != "19c50cff36855b8792b4777c89c598cf33ea70180e677e1a4758fc0211f069ed" {
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
		json.Unmarshal(b, &v)
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
	published, err := os.ReadFile("testdata/offline-7916-images.json")
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
	if images.SourceSHA != restrictedSourceSHA || len(images.Images) != 8 || offlineHash(published) != p.Install.Artifacts.SHA256 {
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
	oldValues, _ := json.Marshal(old.Install.Values["amd64"])
	var oldMap map[string]any
	json.Unmarshal(oldValues, &oldMap)
	if _, ok := oldMap["workers"].(map[string]any)["ai"].(map[string]any)["rbacProfile"]; ok {
		t.Fatal("old profile values changed")
	}
}

// These are node-only bundles: no chart/image files or external tools are needed.
func TestOfflineRestrictedCatalog(t *testing.T) {
	raw, p := restrictedProfileFixture(t)
	for _, bad := range []string{"valid", "old-hash", "old-source", "old-id-new-hash", "unknown"} {
		t.Run(bad, func(t *testing.T) {
			dir, m := offlineFixture(t)
			m["profile"] = p.Profile
			m["profileSha256"] = offlineHash(raw)
			m["orka"] = map[string]any{"mode": "source", "identity": p.Orka.Identity}
			switch bad {
			case "old-hash":
				m["profileSha256"] = offlineHash(offlineProfileJSON)
			case "old-source":
				m["orka"] = map[string]any{"mode": "source", "identity": "59703d6eac1760aa2dc5fed4dee9dcb74c6e5186"}
			case "old-id-new-hash":
				m["profile"] = "orka-59703d6e-native/v1"
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
}

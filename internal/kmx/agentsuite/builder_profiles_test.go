package agentsuite

import (
	"os"
	"reflect"
	"testing"

	suitecore "github.com/agent-primitives/suite/core"
)

func TestResolveSandboxPlanProfiledExactTuple(t *testing.T) {
	root := t.TempDir()
	if err := os.CopyFS(root, os.DirFS(specFixture("minimal"))); err != nil {
		t.Fatal(err)
	}
	var suite suitecore.Suite
	if err := suitecore.Unmarshal(readBuilderTestFile(t, root, "agentsuite.json"), &suite); err != nil {
		t.Fatal(err)
	}
	var bp suitecore.BuildProfile
	if err := suitecore.Unmarshal(readBuilderTestFile(t, root, suite.BuildProfiles[0].Path), &bp); err != nil {
		t.Fatal(err)
	}
	base := suite.Compositions[0]
	var composition suitecore.Composition
	if err := suitecore.Unmarshal(readBuilderTestFile(t, root, base.Path), &composition); err != nil {
		t.Fatal(err)
	}
	suite.Compositions = nil
	suite.BuildProfiles = nil
	for _, id := range []string{"b", "a"} {
		rp := suitecore.RuntimeProfile{SchemaVersion: suitecore.SpecVersion, MediaType: suitecore.MediaTypeRuntimeProfile, ID: id, Runtime: suitecore.EvidenceImplementation{ID: "test-runtime", Version: "test-only", Digest: bp.RuntimeBase[0].Image.Digest}, Harness: suitecore.EvidenceImplementation{ID: "test-harness", Version: "test-only", Digest: bp.Harness[0].Image.Digest}, Renderer: suitecore.EvidenceImplementation{ID: "test-renderer", Version: "test-only", Digest: bp.Harness[0].Image.Digest}, ConfigurationABI: "test-only", ImageRequired: true}
		path := "runtime-profiles/" + id + ".json"
		suite.RuntimeProfiles = append(suite.RuntimeProfiles, suitecore.ManifestRef{ID: id, Path: path, Descriptor: writeBuilderTestDescriptor(t, root, path, suitecore.MediaTypeRuntimeProfile, rp)})
		bp.ID = "build-" + id
		path = "build-profiles/" + id + ".json"
		suite.BuildProfiles = append(suite.BuildProfiles, suitecore.ManifestRef{ID: bp.ID, Path: path, Descriptor: writeBuilderTestDescriptor(t, root, path, suitecore.MediaTypeBuildProfile, bp)})
		composition.RuntimeProfile = id
		composition.BuildProfile = bp.ID
		ref := base
		ref.RuntimeProfile = id
		ref.Path = "compositions/" + id + ".json"
		ref.Descriptor = writeBuilderTestDescriptor(t, root, ref.Path, suitecore.MediaTypeComposition, composition)
		suite.Compositions = append(suite.Compositions, ref)
	}
	writeBuilderTestJSON(t, root, "agentsuite.json", suite)
	for _, s := range []BuildSelection{{}, {Platform: "linux/amd64"}, {RuntimeProfile: "missing"}, {RuntimeProfile: "a", Platform: "linux/arm64"}} {
		if _, err := ResolveSandboxPlan(root, s); err == nil {
			t.Fatalf("accepted %+v", s)
		}
	}
	for i, id := range []string{"b", "a"} {
		p, err := ResolveSandboxPlan(root, BuildSelection{RuntimeProfile: id})
		if err != nil {
			t.Fatal(err)
		}
		if p.RuntimeProfile.ID != id || !reflect.DeepEqual(p.RuntimeProfileDescriptor, suite.RuntimeProfiles[i].Descriptor) || p.BuildProfile.ID != "build-"+id || p.BuildProfileDigest != suite.BuildProfiles[i].Descriptor.Digest || p.CompositionDigest != suite.Compositions[i].Descriptor.Digest {
			t.Fatal("wrong exact tuple", p)
		}
	}
	if _, err := ResolveSandboxPlan(specFixture("minimal"), BuildSelection{RuntimeProfile: "a"}); err == nil {
		t.Fatal("legacy accepted profile selector")
	}
}

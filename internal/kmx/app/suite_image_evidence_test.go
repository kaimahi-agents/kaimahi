package app

import (
	"strings"
	"testing"

	spec "github.com/kaimahi-agents/kaimahi/agentsuite"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentsuite/workspace"
	"github.com/opencontainers/go-digest"
)

func TestImagePassingEvidenceBindsCompleteCasesAndImageSubject(t *testing.T) {
	caseBytes := []byte("id: example\ninput: What is 2+2?\nexpectContains: [\"4\"]\n")
	d := "sha256:" + strings.Repeat("a", 64)
	suite := &workspace.ImageSuite{Digest: d, MemberID: "hello", Cases: []workspace.PackagedCase{{Path: "metadata/kmx/eval/example.yaml", Digest: digest.FromBytes(caseBytes).String(), Bytes: caseBytes}}}
	inventory, casesDigest, err := imageInventory(suite)
	if err != nil {
		t.Fatal(err)
	}
	m := inventory[0].Metadata
	r := suiteImagePassingReceipt{SchemaVersion: 1, Domain: "kmx.image-http.passing/v1", RunID: "image-run", Result: "pass", CasesDigest: casesDigest, Subject: suiteImageSubject{ReleaseDigest: d, SuiteDigest: d, Member: "hello", ImageDigest: d, Profile: "kmx.image-http/v1", ConfigurationDigest: d, Model: "model-a", Target: ReleaseLiftTarget{Context: "kind-demo", ClusterUID: "cluster", Namespace: "demo", NamespaceUID: "namespace"}, BindingDigest: d}, Cases: []suiteImagePassingCase{{ID: m.ID, CaseDigest: m.CaseDigest, InputDigest: m.InputDigest, RequestID: "request-1", DeploymentUID: "deployment-1", AnswerDigest: strings.Repeat("b", 64), Assertions: m.Assertions}}}
	for _, kind := range []string{"ConfigMap", "Deployment", "Service"} {
		uid := "resource-1"
		if kind == "Deployment" {
			uid = "deployment-1"
		}
		r.Resources = append(r.Resources, ReleaseLiftResource{Kind: kind, Name: "hello", UID: uid, SpecDigest: d})
	}
	r.RunID = strings.Repeat("a", 32)
	r.Subject.ExecutionDigest = d
	r.Cases[0].RequestID = strings.Repeat("b", 32)
	raw, err := canonicalImagePassing(t.Context(), &r, suite)
	if err != nil {
		t.Fatal(err)
	}
	for _, scenario := range []string{"pass", "missing case", "wrong case", "failed assertion", "native receipt", "unknown payload", "missing image", "missing namespace UID"} {
		t.Run(scenario, func(t *testing.T) {
			var candidate suiteImagePassingReceipt
			if err := spec.Unmarshal(raw, &candidate); err != nil {
				t.Fatal(err)
			}
			switch scenario {
			case "missing case":
				candidate.Cases = nil
			case "wrong case":
				candidate.Cases[0].CaseDigest = strings.Repeat("c", 64)
			case "failed assertion":
				candidate.Cases[0].Assertions[0].Verdict = "fail"
			case "native receipt":
				candidate.Domain = "kmx.native-suite.passing/v1"
			case "missing image":
				candidate.Subject.ImageDigest = ""
			case "missing namespace UID":
				candidate.Subject.Target.NamespaceUID = ""
			}
			encoded, err := spec.Marshal(candidate)
			if err != nil {
				t.Fatal(err)
			}
			if scenario == "unknown payload" {
				encoded = append([]byte(`{"answer":"private",`), encoded[1:]...)
			}
			_, err = decodeImagePassing(t.Context(), encoded, suite)
			if (err == nil) != (scenario == "pass") {
				t.Fatalf("unexpected evidence validation: %v", err)
			}
		})
	}
}

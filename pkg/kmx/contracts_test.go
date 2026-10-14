package kmx

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestDigestCanonicalRoundTrip(t *testing.T) {
	digest := agentSourceDigest([]byte("agent source"))
	const want = "b33be396932ae95ab5b5958886391fa94a69a676640516d442e0881a742fa323"
	if got := digest.String(); got != want {
		t.Fatalf("Digest = %q, want %q", got, want)
	}
	parsed, err := ParseDigest(want)
	if err != nil {
		t.Fatal(err)
	}
	if parsed != digest {
		t.Fatalf("parsed digest = %q, want %q", parsed, digest)
	}
	if _, err := ParseDigest("sha256:ABC"); err == nil {
		t.Fatal("ParseDigest accepted a non-canonical digest")
	}
	if _, err := ParseDigest("0000000000000000000000000000000000000000000000000000000000000000"); err == nil {
		t.Fatal("ParseDigest accepted the all-zero unspecified value")
	}
}

func TestDigestDomainsMatchPortableAndRenderedFraming(t *testing.T) {
	source := []byte("same bytes")
	revision := agentSourceDigest(source)
	rendered := framedDigest(digestEntry{path: "rendered/000.yaml", data: source})
	if revision == rendered {
		t.Fatal("portable and rendered digest domains collapsed")
	}
	const portableWant = "58ffa43055abb647e61f397356d89da693a0a259c7a1b6a55d91d38a02afd2d9"
	const renderedWant = "2f0e0cc314a5458f53507186b83394a4f38fac030654ff0fd8fe4b03991c0bb2"
	if revision.String() != portableWant || rendered.String() != renderedWant {
		t.Fatalf("framed digests = %s, %s; want %s, %s", revision, rendered, portableWant, renderedWant)
	}
}

func TestSourceRevisionAndBindingAreImmutable(t *testing.T) {
	sourceBytes := []byte("agent source")
	source, err := NewAgentSource(sourceBytes)
	if err != nil {
		t.Fatal(err)
	}
	revision, err := NewAgentRevision(source)
	if err != nil {
		t.Fatal(err)
	}
	sourceBytes[0] = 'X'
	copyOfSource := revision.Source()
	copyOfSource[0] = 'Y'
	if got := string(revision.Source()); got != "agent source" {
		t.Fatalf("revision source changed through caller mutation: %q", got)
	}
	if revision.Ref().Digest != agentSourceDigest([]byte("agent source")) {
		t.Fatal("revision digest was not computed from its source")
	}

	target := TargetRef{Platform: "example", ID: "target-1"}
	bindingBytes := []byte("binding")
	binding, err := NewTargetBinding(target, "example.dev/v1alpha1", "ExampleBinding", bindingBytes)
	if err != nil {
		t.Fatal(err)
	}
	bindingBytes[0] = 'X'
	copyOfBinding := binding.Bytes()
	copyOfBinding[0] = 'Y'
	if got := string(binding.Bytes()); got != "binding" {
		t.Fatalf("target binding changed through caller mutation: %q", got)
	}
}

func TestReceiptJSONRoundTrip(t *testing.T) {
	at := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
	target := TargetRef{Platform: "example", ID: "target-1"}
	source, _ := NewAgentSource([]byte("agent source"))
	revision, _ := NewAgentRevision(source)
	sourceRef, _ := NewRevisionDeploymentSourceRef(revision.Ref())
	runtime := RuntimeRef{Runtime: "example", Installation: "install-1", Target: target}
	bindingDigest, _ := ParseDigest("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	renderedDigest, _ := ParseDigest("bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb")
	deployDigest, _ := ParseDigest("cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc")
	deployment := DeploymentReceipt{
		ID: "receipt-1", Operation: "lift-1",
		Deployment:    DeploymentRef{ID: "deployment-1", Source: sourceRef, Runtime: runtime},
		BindingDigest: bindingDigest, RenderedDigest: renderedDigest,
		DeployDigest: deployDigest, RecordedAt: at,
	}
	data, err := json.Marshal(deployment)
	if err != nil {
		t.Fatal(err)
	}
	var decoded DeploymentReceipt
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(decoded, deployment) {
		t.Fatalf("receipt round trip = %#v, want %#v", decoded, deployment)
	}
	if err := json.Unmarshal([]byte(`{"id":"broken"}`), &decoded); err == nil {
		t.Fatal("invalid receipt JSON was accepted")
	}
	invalid := deployment
	invalid.Deployment.ID = DeploymentID(string([]byte{0xff}))
	if _, err := json.Marshal(invalid); err == nil {
		t.Fatal("receipt with invalid UTF-8 identity was accepted")
	}
}

func TestDurableRefsRejectInvalidUTF8AndRoundTrip(t *testing.T) {
	target := TargetRef{Platform: "example", ID: "target-1"}
	data, err := json.Marshal(target)
	if err != nil {
		t.Fatal(err)
	}
	var decoded TargetRef
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded != target {
		t.Fatalf("target round trip = %#v, want %#v", decoded, target)
	}
	invalid := TargetRef{Platform: "example", ID: TargetID(string([]byte{0xff}))}
	if _, err := json.Marshal(invalid); err == nil {
		t.Fatal("TargetRef accepted invalid UTF-8")
	}
	if err := json.Unmarshal([]byte{'{', '"', 'i', 'd', '"', ':', '"', 0xff, '"', '}'}, &decoded); err == nil {
		t.Fatal("TargetRef decoded invalid UTF-8 JSON")
	}
}

func TestEnvironmentValuesValidateAndRoundTrip(t *testing.T) {
	environment := Environment{
		Ref:  EnvironmentRef{ID: "local"},
		Spec: EnvironmentSpec{Name: "local"},
	}
	if err := environment.Validate(); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(environment)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(data), `{"ref":{"id":"local"},"spec":{"name":"local"}}`; got != want {
		t.Fatalf("environment JSON = %s, want %s", got, want)
	}
	var decoded Environment
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded != environment {
		t.Fatalf("environment round trip = %#v, want %#v", decoded, environment)
	}
	for name, value := range map[string]any{
		"spec":        EnvironmentSpec{},
		"reference":   EnvironmentRef{},
		"environment": Environment{Ref: environment.Ref},
	} {
		if _, err := json.Marshal(value); err == nil {
			t.Errorf("invalid %s serialized", name)
		}
	}
	if err := json.Unmarshal([]byte(`{"ref":{"id":"local"},"spec":{}}`), &decoded); err == nil {
		t.Fatal("environment decoded without a name")
	}
	if err := json.Unmarshal([]byte(`{"name":"local","runtime":"orka"}`), new(EnvironmentSpec)); err == nil {
		t.Fatal("EnvironmentSpec accepted an implementation selector")
	}
	for _, raw := range []string{
		`{"Name":"local"}`,
		`{"name":"local","name":"other"}`,
	} {
		if err := json.Unmarshal([]byte(raw), new(EnvironmentSpec)); err == nil {
			t.Fatalf("EnvironmentSpec accepted ambiguous JSON %s", raw)
		}
	}
	if raw := `{"ref":{"id":"local","id":"other"},"spec":{"name":"local"}}`; json.Unmarshal([]byte(raw), &decoded) == nil {
		t.Fatalf("Environment accepted ambiguous nested JSON %s", raw)
	}
	invalid := EnvironmentRef{ID: EnvironmentID(string([]byte{0xff}))}
	if _, err := json.Marshal(invalid); err == nil {
		t.Fatal("EnvironmentRef accepted invalid UTF-8")
	}
}

func TestInProcessValuesRefuseAccidentalJSON(t *testing.T) {
	source, _ := NewAgentSource([]byte("agent source"))
	revision, err := NewAgentRevision(source)
	if err != nil {
		t.Fatal(err)
	}
	target := TargetRef{Platform: "example", ID: "target-1"}
	binding, _ := NewTargetBinding(target, "example.dev/v1alpha1", "ExampleBinding", []byte("binding"))
	sourceRef, _ := NewRevisionDeploymentSource(revision)
	for name, values := range map[string]struct {
		encode any
		decode any
	}{
		"source":            {source, new(AgentSource)},
		"revision":          {revision, new(AgentRevision)},
		"binding":           {binding, new(TargetBinding)},
		"deployment source": {sourceRef, new(DeploymentSource)},
		"lift request":      {LiftRequest{Source: sourceRef, Binding: binding}, new(LiftRequest)},
	} {
		if _, err := json.Marshal(values.encode); err == nil {
			t.Fatalf("%s silently serialized", name)
		}
		if err := json.Unmarshal([]byte(`{}`), values.decode); err == nil {
			t.Fatalf("%s silently deserialized", name)
		}
	}
}

func TestOutcomeUnknownErrorPreservesCause(t *testing.T) {
	cause := errors.New("connection ended after submission")
	err, constructionErr := NewOutcomeUnknownError("lift", "lift-1", cause)
	if constructionErr != nil {
		t.Fatal(constructionErr)
	}
	if !errors.Is(err, cause) {
		t.Fatal("OutcomeUnknownError did not preserve its cause")
	}
	if err.OperationID() != "lift-1" {
		t.Fatalf("OperationID = %q, want lift-1", err.OperationID())
	}
	if _, constructionErr := NewOutcomeUnknownError("lift", "", cause); constructionErr == nil {
		t.Fatal("NewOutcomeUnknownError accepted an empty operation ID")
	}
	invalid := OperationID(string([]byte{0xff}))
	if invalid.Validate() == nil {
		t.Fatal("OperationID accepted invalid UTF-8")
	}
	if _, constructionErr := NewOutcomeUnknownError("lift", invalid, cause); constructionErr == nil {
		t.Fatal("NewOutcomeUnknownError accepted invalid UTF-8 operation ID")
	}
}

func TestTargetBindingRejectsInvalidMetadata(t *testing.T) {
	target := TargetRef{Platform: "example", ID: "target-1"}
	if _, err := NewTargetBinding(target, string([]byte{0xff}), "Binding", []byte("data")); err == nil {
		t.Fatal("NewTargetBinding accepted invalid UTF-8 API version")
	}
}

func TestDeploymentSourceRequiresExactlyOneInput(t *testing.T) {
	if err := (DeploymentSourceRef{}).Validate(); err == nil {
		t.Fatal("empty deployment source was accepted")
	}
	source, _ := NewAgentSource([]byte("agent source"))
	revision, _ := NewAgentRevision(source)
	revisionRef := revision.Ref()
	image := AgentSandboxImage{
		Image: OCIArtifactRef{
			Location:  "registry.example/agents/writer",
			Digest:    "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			MediaType: MediaTypeOCIImageManifest,
		},
		SuiteManifest: "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		Agent:         "writer",
		Platform:      SandboxPlatform{OS: "linux", Architecture: "amd64"},
		BuildProfile:  "default",
		Composition:   "sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc",
		Binding:       "sha256:dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd",
	}
	revisionSource, _ := NewRevisionDeploymentSourceRef(revisionRef)
	sandboxSource, _ := NewSandboxDeploymentSourceRef(image.Ref())
	if revisionSource == sandboxSource {
		t.Fatal("revision and sandbox deployment sources compare equal")
	}
	data, err := json.Marshal(sandboxSource)
	if err != nil {
		t.Fatal(err)
	}
	var decoded DeploymentSourceRef
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded != sandboxSource {
		t.Fatalf("sandbox source round trip = %#v, want %#v", decoded, sandboxSource)
	}
	if strings.Contains(string(data), "location") || strings.Contains(string(data), "registry.example") {
		t.Fatalf("sandbox deployment identity serialized retrieval location: %s", data)
	}
	invalidUnion := `{"kind":"revision","revision":{"digest":"` + revisionRef.Digest.String() + `"},"sandboxImage":{}}`
	if err := json.Unmarshal([]byte(invalidUnion), &decoded); err == nil {
		t.Fatal("deployment source JSON accepted both union arms")
	}
	if _, err := NewSandboxDeploymentSource(image); err != nil {
		t.Fatal(err)
	}
	published := image
	published.Image.Location = "another.registry.example/agents/writer"
	publishedRef, err := NewSandboxDeploymentSourceRef(published.Ref())
	if err != nil {
		t.Fatal(err)
	}
	if publishedRef != sandboxSource {
		t.Fatal("sandbox deployment identity changed with retrieval location")
	}
	image.Binding = ""
	if _, err := NewSandboxDeploymentSource(image); err == nil {
		t.Fatal("sandbox deployment source accepted no binding digest")
	}
}

func TestAgentSuiteArtifactAndSandboxImageValidation(t *testing.T) {
	suite := AgentSuiteArtifact{
		Manifest: OCIArtifactRef{
			Location:     "/tmp/team-suite-layout",
			Digest:       "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			MediaType:    MediaTypeOCIImageManifest,
			ArtifactType: ArtifactTypeAgentSuite,
		},
	}
	if err := suite.Validate(); err != nil {
		t.Fatal(err)
	}
	if got := suite.Ref(); got.Manifest != suite.Manifest.Digest {
		t.Fatalf("AgentSuite artifact ref = %+v", got)
	}
	report := SuiteReport{
		Name: "team", Agents: 1, Compositions: 1,
		CompositionSelections: []SuiteCompositionSelection{{
			Agent: "writer", Platform: SandboxPlatform{OS: "linux", Architecture: "amd64"},
			BuildProfile: "default",
			Digest:       "sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc",
		}},
	}
	selection, err := report.Composition("writer", SandboxPlatform{OS: "linux", Architecture: "amd64"})
	if err != nil || selection.BuildProfile != "default" {
		t.Fatalf("Composition selection = %+v, %v", selection, err)
	}
	validated := ValidatedAgentSuite{Artifact: suite, Report: report}
	request := BuildSuiteRequest{Operation: "build-1", Suite: validated}
	if err := request.Validate(); err != nil {
		t.Fatal(err)
	}
	result := validated
	data, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	var decodedResult ValidatedAgentSuite
	if err := json.Unmarshal(data, &decodedResult); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(decodedResult, result) {
		t.Fatalf("validated suite round trip = %#v, want %#v", decodedResult, result)
	}
	bad := suite
	bad.Manifest.Digest = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	if err := bad.Validate(); err == nil {
		t.Fatal("AgentSuite artifact accepted a non-OCI digest")
	}
	request.Suite.Report.CompositionSelections[0].Platform.Architecture = "riscv64"
	if err := request.Validate(); err == nil {
		t.Fatal("suite build accepted a platform outside the AgentSuite draft")
	}
}

func TestAgentSuitesBuildAndPushStagesPreserveIdentities(t *testing.T) {
	validated, images := suiteBuildFixture()
	build := BuildSuiteResult{Operation: "build-1", Suite: validated, Images: images}
	if err := build.Validate(); err != nil {
		t.Fatal(err)
	}
	buildRequest := BuildSuiteRequest{Operation: build.Operation, Suite: validated}
	if err := build.ValidateAgainst(buildRequest); err != nil {
		t.Fatal(err)
	}
	wrongBuild := build
	wrongBuild.Operation = "build-2"
	if err := wrongBuild.ValidateAgainst(buildRequest); err == nil {
		t.Fatal("build validation accepted another operation")
	}
	for name, value := range map[string]any{
		"validated suite": validated,
		"build result":    build,
		"build progress":  BuildSuiteProgress{Operation: build.Operation, Suite: validated, Images: images, Complete: true},
	} {
		data, err := json.Marshal(value)
		if err != nil {
			t.Fatalf("marshal %s: %v", name, err)
		}
		decoded := reflect.New(reflect.TypeOf(value)).Interface()
		if err := json.Unmarshal(data, decoded); err != nil {
			t.Fatalf("unmarshal %s: %v", name, err)
		}
		switch got := reflect.ValueOf(decoded).Elem().Interface().(type) {
		case ValidatedAgentSuite:
			if got.Ref() != validated.Ref() || !sameSuiteReport(got.Report, validated.Report) {
				t.Fatalf("%s round trip changed identity or report", name)
			}
		case BuildSuiteResult:
			if got.Operation != build.Operation || got.Suite.Ref() != build.Suite.Ref() ||
				!sameSuiteReport(got.Suite.Report, build.Suite.Report) || !reflect.DeepEqual(got.Images, build.Images) {
				t.Fatalf("%s round trip changed operation, identity, report or images", name)
			}
		case BuildSuiteProgress:
			if got.Operation != build.Operation || !got.Complete || got.Suite.Ref() != build.Suite.Ref() ||
				!sameSuiteReport(got.Suite.Report, build.Suite.Report) || !reflect.DeepEqual(got.Images, build.Images) {
				t.Fatalf("%s round trip changed operation, state, identity, report or images", name)
			}
		default:
			t.Fatalf("unexpected staged value type %T", got)
		}
	}

	progress := BuildSuiteProgress{Operation: build.Operation, Suite: validated, Images: images[:1]}
	if err := progress.Validate(); err != nil {
		t.Fatal(err)
	}
	if err := progress.ValidateAgainst(buildRequest); err != nil {
		t.Fatal(err)
	}
	if _, err := progress.Result(); err == nil {
		t.Fatal("incomplete build progress produced a result")
	}
	progress.Complete = true
	if err := progress.Validate(); err == nil {
		t.Fatal("complete build progress accepted a missing member image")
	}

	pushRequest := PushSuiteRequest{Operation: "push-1", Build: build, Destination: "registry.example/team"}
	pushed := PushSuiteResult{Operation: pushRequest.Operation, Destination: pushRequest.Destination, Suite: validated, Images: append([]AgentSandboxImage(nil), images...)}
	pushed.Suite.Artifact.Manifest.Location = pushRequest.Destination
	for i := range pushed.Images {
		pushed.Images[i].Image.Location = pushRequest.Destination
	}
	if err := pushed.ValidateAgainst(pushRequest); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(pushed)
	if err != nil {
		t.Fatal(err)
	}
	var decodedPush PushSuiteResult
	if err := json.Unmarshal(data, &decodedPush); err != nil {
		t.Fatal(err)
	}
	if err := decodedPush.ValidateAgainst(pushRequest); err != nil {
		t.Fatalf("persisted push result no longer matches request: %v", err)
	}
	pushed.Images[0].Image.Digest = "sha256:eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"
	if err := pushed.ValidateAgainst(pushRequest); err == nil {
		t.Fatal("push validation accepted a substituted member image")
	}
	pushed = decodedPush
	pushed.Destination = "registry.example/another-team"
	if err := pushed.ValidateAgainst(pushRequest); err == nil {
		t.Fatal("push validation accepted another destination")
	}
	pushed = decodedPush
	pushed.Images[0].Image.Location = "/tmp/local-image"
	if err := pushed.ValidateAgainst(pushRequest); err == nil {
		t.Fatal("push validation accepted a local member-image location")
	}
}

func TestAgentSuitesBuildRejectsMismatchedAndUnsupportedOutputs(t *testing.T) {
	validated, images := suiteBuildFixture()
	tests := []struct {
		name   string
		mutate func(*ValidatedAgentSuite, *[]AgentSandboxImage)
	}{
		{"no images", func(_ *ValidatedAgentSuite, images *[]AgentSandboxImage) { *images = nil }},
		{"missing image", func(_ *ValidatedAgentSuite, images *[]AgentSandboxImage) { *images = (*images)[:1] }},
		{"duplicate image", func(_ *ValidatedAgentSuite, images *[]AgentSandboxImage) { (*images)[1] = (*images)[0] }},
		{"foreign suite", func(_ *ValidatedAgentSuite, images *[]AgentSandboxImage) {
			(*images)[0].SuiteManifest = "sha256:eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"
		}},
		{"wrong composition", func(_ *ValidatedAgentSuite, images *[]AgentSandboxImage) {
			(*images)[0].Composition = "sha256:eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"
		}},
		{"standalone tool image", func(suite *ValidatedAgentSuite, _ *[]AgentSandboxImage) { suite.Report.ToolCompositions = 1 }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			suite := validated
			suite.Report.CompositionSelections = append([]SuiteCompositionSelection(nil), validated.Report.CompositionSelections...)
			got := append([]AgentSandboxImage(nil), images...)
			test.mutate(&suite, &got)
			if err := (BuildSuiteResult{Operation: "build-1", Suite: suite, Images: got}).Validate(); err == nil {
				t.Fatal("invalid suite build succeeded")
			}
		})
	}
}

func suiteBuildFixture() (ValidatedAgentSuite, []AgentSandboxImage) {
	suiteDigest := OCIManifestDigest("sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	platform := SandboxPlatform{OS: "linux", Architecture: "amd64"}
	selections := []SuiteCompositionSelection{
		{Agent: "coordinator", Platform: platform, BuildProfile: "default", Digest: "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"},
		{Agent: "writer", Platform: platform, BuildProfile: "default", Digest: "sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"},
	}
	validated := ValidatedAgentSuite{
		Artifact: AgentSuiteArtifact{Manifest: OCIArtifactRef{
			Location: "/tmp/team-suite-layout", Digest: suiteDigest,
			MediaType: MediaTypeOCIImageManifest, ArtifactType: ArtifactTypeAgentSuite,
		}},
		Report: SuiteReport{Name: "team", Agents: 2, Compositions: 2, Capabilities: []string{}, CompositionSelections: selections},
	}
	images := make([]AgentSandboxImage, len(selections))
	for i, selection := range selections {
		imageDigest := OCIManifestDigest("sha256:dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd")
		if i == 1 {
			imageDigest = "sha256:eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"
		}
		images[i] = AgentSandboxImage{
			Image:         OCIArtifactRef{Location: "/tmp/" + selection.Agent + "-image", Digest: imageDigest, MediaType: MediaTypeOCIImageManifest},
			SuiteManifest: suiteDigest, Agent: selection.Agent, Platform: selection.Platform,
			BuildProfile: selection.BuildProfile, Composition: selection.Digest,
			Binding: "sha256:ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff",
		}
	}
	return validated, images
}

func TestSuiteReportRejectsDuplicateCompositionSelection(t *testing.T) {
	selection := SuiteCompositionSelection{
		Agent: "writer", Platform: SandboxPlatform{OS: "linux", Architecture: "amd64"},
		BuildProfile: "default",
		Digest:       "sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc",
	}
	report := SuiteReport{
		Name: "team", Agents: 1, Compositions: 2,
		CompositionSelections: []SuiteCompositionSelection{selection, selection},
	}
	if err := report.Validate(); err == nil {
		t.Fatal("SuiteReport accepted duplicate agent/platform selections")
	}
}

func TestSuiteReportRejectsIncoherentCountsAndPlatforms(t *testing.T) {
	selection := SuiteCompositionSelection{
		Agent: "writer", Platform: SandboxPlatform{OS: "linux", Architecture: "amd64"},
		BuildProfile: "default",
		Digest:       "sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc",
	}
	tests := []SuiteReport{
		{Name: "team", Agents: 2, Compositions: 1, CompositionSelections: []SuiteCompositionSelection{selection}},
		{Name: "team", Agents: int(^uint(0) >> 1), Compositions: 1, CompositionSelections: []SuiteCompositionSelection{selection}},
		{Name: "team", Agents: 1, Tools: -1, Compositions: 1, CompositionSelections: []SuiteCompositionSelection{selection}},
		{Name: "team", Agents: 1, ToolCompositions: -1, Compositions: 1, CompositionSelections: []SuiteCompositionSelection{selection}},
		{Name: "team", Agents: 1, Compositions: 1, CompositionSelections: []SuiteCompositionSelection{selection}, AgentPlatforms: []SuiteAgentPlatform{{Agent: "other", Platforms: []SandboxPlatform{selection.Platform}}}},
		{Name: "team", Agents: 1, Compositions: 1, Capabilities: []string{"bad_capability"}, CompositionSelections: []SuiteCompositionSelection{selection}},
		{Name: "team", Agents: 1, Compositions: 1, Capabilities: []string{"z", "a"}, CompositionSelections: []SuiteCompositionSelection{selection}},
		{Name: "team", Agents: 1, Compositions: 1, Capabilities: []string{"a", "a"}, CompositionSelections: []SuiteCompositionSelection{selection}},
		{Name: "team", Agents: 1, Compositions: 1, Capabilities: []string{string([]byte{0xff})}, CompositionSelections: []SuiteCompositionSelection{selection}},
	}
	for i, report := range tests {
		if err := report.Validate(); err == nil {
			t.Errorf("incoherent report %d succeeded", i)
		}
	}
}

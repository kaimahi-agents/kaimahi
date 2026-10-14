package app

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	spec "github.com/kaimahi-agents/kaimahi/agentsuite"
	core "github.com/kaimahi-agents/kaimahi/internal/kmx/agentsuite"
	suiteoras "github.com/kaimahi-agents/kaimahi/internal/kmx/agentsuite/oras"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentsuite/workspace"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/imagelift"
	"github.com/opencontainers/go-digest"
	"github.com/opencontainers/image-spec/specs-go"
	oci "github.com/opencontainers/image-spec/specs-go/v1"
	"oras.land/oras-go/v2/content"
	storeoci "oras.land/oras-go/v2/content/oci"
)

func TestImageEvaluateThenSharedLiftExactReuse(t *testing.T) {
	a, _, _, _ := releaseApplyFixture(t, "")
	root := t.TempDir()
	input := filepath.Join(t.TempDir(), "instructions.md")
	if err := os.WriteFile(input, []byte("Reply with 4."), 0600); err != nil {
		t.Fatal(err)
	}
	if err := workspace.Create(t.Context(), root, workspace.CreateOptions{Name: "image", InstructionsFile: input, Execution: workspace.ImageExecution, ContextTokens: 8192, OutputTokens: 1024}); err != nil {
		t.Fatal(err)
	}
	review, err := a.reviewSuiteWorkspace(t.Context(), root)
	if err != nil || review.image == nil || len(review.inventory) != 1 {
		t.Fatalf("shared image review: %v", err)
	}
	if !strings.Contains(strings.Join(review.sourceLines(100), "\n"), "image-http") {
		t.Fatal("image source review lost profile")
	}
	p, err := workspace.Prepare(t.Context(), root, filepath.Join(t.TempDir(), "cache"))
	if err != nil {
		t.Fatal(err)
	}
	store, err := storeoci.New(p.Path)
	if err != nil {
		t.Fatal(err)
	}
	extracted := filepath.Join(t.TempDir(), "source")
	if _, err := suiteoras.Pull(t.Context(), p.Path, p.Descriptor.Digest.String(), extracted); err != nil {
		t.Fatal(err)
	}
	plan, err := core.ResolveSandboxPlan(extracted, core.BuildSelection{Agent: "image", Platform: "linux/amd64"})
	if err != nil {
		t.Fatal(err)
	}
	label, err := core.EncodeImageDeployment(core.ImageDeployment{SchemaVersion: spec.SpecVersion, MediaType: core.ImageDeploymentMediaType, SuiteDigest: p.Descriptor.Digest.String(), Agent: "image", Platform: plan.Composition.Platform, CompositionDigest: plan.CompositionDigest, BuildProfile: plan.BuildProfile.ID, BuildProfileDigest: plan.BuildProfileDigest, Execution: *plan.BuildProfile.Execution, Inference: plan.Agent.Model.Capabilities})
	if err != nil {
		t.Fatal(err)
	}
	push := func(media string, value any) oci.Descriptor {
		raw, err := spec.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		d := content.NewDescriptorFromBytes(media, raw)
		if err := store.Push(t.Context(), d, bytes.NewReader(raw)); err != nil {
			t.Fatal(err)
		}
		return d
	}
	layer := push(oci.MediaTypeImageLayer, "synthetic")
	config := oci.Image{Platform: oci.Platform{OS: "linux", Architecture: "amd64"}, Config: oci.ImageConfig{User: "1000", Entrypoint: []string{"/agent"}, Cmd: []string{"--config", core.AgentKitConfigPath, "--protocol", "openai"}, Labels: map[string]string{core.ImageDeploymentLabel: label}}, RootFS: oci.RootFS{Type: "layers", DiffIDs: []digest.Digest{layer.Digest}}}
	image := push(oci.MediaTypeImageManifest, oci.Manifest{Versioned: specs.Versioned{SchemaVersion: 2}, Config: push(oci.MediaTypeImageConfig, config), Layers: []oci.Descriptor{layer}})
	suiteDescriptor := p.Descriptor
	suiteDescriptor.ArtifactType = ""
	suiteDescriptor.Annotations = nil
	release, err := suiteoras.PublishImageRelease(t.Context(), store, store, "release", suiteoras.ImageRelease{SchemaVersion: 1, Suite: suiteDescriptor, Images: []suiteoras.ImageReleaseSlot{{Member: "image", Platform: plan.Composition.Platform, Image: image, Graph: image}}}, false)
	if err != nil {
		t.Fatal(err)
	}
	listen := "0.0.0.0"
	env := imagelift.Environment{APIVersion: "kaimahi.dev/lift/v1alpha1", Name: "sample", Context: "kind-test", ClusterUID: "cluster-uid", Namespace: "demo", Adapter: spec.ExecutionHTTPV1, Platform: plan.Composition.Platform, Members: map[string]imagelift.MemberEnvironment{"image": {Name: "image", Inputs: map[string]imagelift.InputBinding{"agent-auth": {SecretRef: &spec.SecretKeyRef{Name: "auth", Key: "token"}}, "listen": {Value: &listen}}, Inference: &imagelift.InferenceBinding{Model: "model-a", Endpoint: "http://model.example/v1", Capabilities: *plan.Agent.Model.Capabilities, Evidence: "operator-declared"}}}}
	private := t.TempDir()
	if err := os.Chmod(private, 0700); err != nil {
		t.Fatal(err)
	}
	envPath := filepath.Join(private, "environment.json")
	raw, _ := json.Marshal(env)
	if err := os.WriteFile(envPath, raw, 0600); err != nil {
		t.Fatal(err)
	}
	opt := ReleaseLiftOptions{Reference: "registry.example/images@" + release.Digest.String(), FromLayout: p.Path, ToContext: "kind-test", Environment: envPath, Evidence: filepath.Join(private, "passing.json")}
	objects := map[string]map[string]any{}
	writes, admissions, calls := 0, 0, 0
	secretVersion := "1"
	base := a.captureCommand
	a.captureCommand = func(ctx context.Context, cmd *exec.Cmd) error {
		args := cmd.Args
		if slices.Contains(args, "config") {
			return base(ctx, cmd)
		}
		if slices.Contains(args, "rollout") {
			return nil
		}
		if i := slices.Index(args, "get"); i >= 0 {
			kind, name := strings.ToLower(args[i+1]), args[i+2]
			if kind == "namespace" {
				uid := "namespace-uid"
				if name == "kube-system" {
					uid = "cluster-uid"
				}
				return json.NewEncoder(cmd.Stdout).Encode(map[string]any{"kind": "Namespace", "metadata": map[string]any{"name": name, "uid": uid}})
			}
			if kind == "nodes" {
				_, err := io.WriteString(cmd.Stdout, `{"items":[{"status":{"conditions":[{"type":"Ready","status":"True"}]}}]}`)
				return err
			}
			if kind == "secret" {
				text := "present"
				if strings.Contains(strings.Join(args, " "), "resourceVersion") {
					text = "auth-uid|" + secretVersion + "|token;"
				}
				_, err := io.WriteString(cmd.Stdout, text)
				return err
			}
			if obj := objects[kind]; obj != nil {
				return json.NewEncoder(cmd.Stdout).Encode(obj)
			}
			return nil
		}
		if slices.Contains(args, "create") {
			var doc map[string]any
			if err := json.NewDecoder(cmd.Stdin).Decode(&doc); err != nil {
				return err
			}
			if slices.Contains(args, "--dry-run=server") {
				admissions++
				return json.NewEncoder(cmd.Stdout).Encode(doc)
			}
			if admissions != 3 && admissions != 6 && admissions != 9 {
				t.Fatal("write before all admissions")
			}
			writes++
			kind := strings.ToLower(doc["kind"].(string))
			meta := doc["metadata"].(map[string]any)
			meta["uid"], meta["resourceVersion"], meta["generation"] = kind+"-uid", "1", float64(1)
			if kind == "deployment" {
				doc["status"] = map[string]any{"observedGeneration": 1, "updatedReplicas": 1, "availableReplicas": 1}
			}
			objects[kind] = doc
			return json.NewEncoder(cmd.Stdout).Encode(doc)
		}
		return fmt.Errorf("unexpected image operation %v", args)
	}
	if err := a.evaluateImageRelease(t.Context(), opt, opt.Evidence, func(context.Context, *App, WorkspaceDeployment, string, string) (string, error) {
		calls++
		return "4", nil
	}); err != nil {
		t.Fatal(err)
	}
	if calls != 1 || writes != 3 {
		t.Fatalf("calls=%d writes=%d", calls, writes)
	}
	if err := os.RemoveAll(root); err != nil {
		t.Fatal(err)
	}
	prepared, err := a.PrepareReleaseLift(t.Context(), opt)
	if err != nil {
		t.Fatal(err)
	}
	result, err := a.ApplyReleaseLift(t.Context(), prepared, prepared.Summary().PlanDigest)
	if err != nil || result.State != "ready" {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	if err := a.VerifyReleaseLiftPlacement(t.Context(), result); err != nil {
		t.Fatal(err)
	}
	if result.Placement == "" {
		t.Fatal("lift did not retain placement")
	}
	// Reload through an independently constructed application value, with no
	// prepared plan or in-memory placement authority from the apply invocation.
	freshApp := *a
	loaded, err := freshApp.loadImagePlacement(t.Context(), result.Placement)
	if err != nil || loaded.subject.ImageDigest != prepared.image.subject.ImageDigest {
		t.Fatalf("fresh placement reload: %v", err)
	}
	if calls != 1 || writes != 3 || admissions != 3 {
		t.Fatal("lift executed or rewrote evaluated placement")
	}
	if _, err := a.ApplyReleaseLift(t.Context(), prepared, prepared.Summary().PlanDigest); err == nil {
		t.Fatal("image prepared plan reused")
	}
	before := writes
	if err := a.evaluateImageRelease(t.Context(), opt, filepath.Join(private, "second.json"), func(context.Context, *App, WorkspaceDeployment, string, string) (string, error) {
		calls++
		return "4", nil
	}); err == nil {
		t.Fatal("occupied image evaluation adopted live objects")
	}
	if calls != 1 || writes != before {
		t.Fatal("refused repeat caused effects")
	}
	promoted := opt
	promoted.Promote = true
	if _, err := a.PrepareReleaseLift(t.Context(), promoted); err == nil {
		t.Fatal("undefined image promotion accepted")
	}
	clear(objects)
	recreate, err := a.PrepareReleaseLift(t.Context(), opt)
	if err != nil {
		t.Fatal(err)
	}
	recreated, err := a.ApplyReleaseLift(t.Context(), recreate, recreate.Summary().PlanDigest)
	if err != nil || recreated.State != "ready" {
		t.Fatalf("absent image creation: %+v %v", recreated, err)
	}
	if writes != 6 || admissions != 6 || calls != 1 {
		t.Fatal("lift creation did not stay deploy-only")
	}
	if err := a.VerifyReleaseLiftPlacement(t.Context(), recreated); err != nil {
		t.Fatal(err)
	}
	clear(objects)
	env.Namespace = "destination"
	raw, _ = json.Marshal(env)
	if err := os.WriteFile(envPath, raw, 0600); err != nil {
		t.Fatal(err)
	}
	destination, err := a.PrepareReleaseLift(t.Context(), promoted)
	if err != nil {
		t.Fatal("explicit cross-namespace image promotion", err)
	}
	if destination.Summary().Gate != "promotion" || destination.Summary().SourceTarget.Namespace != "demo" {
		t.Fatal("lost source-only promotion disclosure")
	}
	placed, err := a.ApplyReleaseLift(t.Context(), destination, destination.Summary().PlanDigest)
	if err != nil || placed.State != "ready" {
		t.Fatalf("promotion apply: %+v %v", placed, err)
	}
	if err := a.VerifyReleaseLiftPlacement(t.Context(), placed); err != nil {
		t.Fatal(err)
	}
	if writes != 9 || admissions != 9 || calls != 1 {
		t.Fatal("promotion executed additional model requests")
	}
	clear(objects)
	wrong := env.Members["image"]
	wrong.Inference.Model = "model-b"
	env.Members["image"] = wrong
	raw, _ = json.Marshal(env)
	if err := os.WriteFile(envPath, raw, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := a.PrepareReleaseLift(t.Context(), promoted); err == nil {
		t.Fatal("model-changing promotion accepted")
	}
	wrong.Inference.Model = "model-a"
	env.Namespace = "demo"
	raw, _ = json.Marshal(env)
	if err := os.WriteFile(envPath, raw, 0600); err != nil {
		t.Fatal(err)
	}
	secretVersion = "2"
	if _, err := a.PrepareReleaseLift(t.Context(), opt); err == nil {
		t.Fatal("credential revision drift accepted")
	}
	if err := a.VerifyReleaseLiftPlacement(t.Context(), result); err == nil {
		t.Fatal("verification missed credential drift")
	}
}

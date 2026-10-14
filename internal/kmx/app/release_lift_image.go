package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"reflect"
	"strings"

	spec "github.com/kaimahi-agents/kaimahi/agentsuite"
	suiteoras "github.com/kaimahi-agents/kaimahi/internal/kmx/agentsuite/oras"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentsuite/workspace"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/imagelift"
	"github.com/opencontainers/go-digest"
)

type releaseLiftImage struct {
	suite    *workspace.ImageSuite
	env      imagelift.Environment
	plan     imagelift.Plan
	subject  suiteImageSubject
	evidence *suiteImagePassingReceipt
	options  ReleaseLiftOptions
	observed []ReleaseLiftResource
}

func imageAdmissionContent(doc map[string]any) map[string]any {
	copy, _ := cloneOrkaDoc(doc)
	delete(copy, "metadata")
	delete(copy, "status")
	if copy["kind"] == "Service" {
		s, _ := copy["spec"].(map[string]any)
		for _, key := range []string{"clusterIP", "clusterIPs", "ipFamilies", "ipFamilyPolicy"} {
			delete(s, key)
		}
	}
	return copy
}

func imageAdmission(desired map[string]any, raw []byte) (map[string]any, error) {
	var actual map[string]any
	if json.Unmarshal(raw, &actual) != nil || actual["apiVersion"] != desired["apiVersion"] || actual["kind"] != desired["kind"] {
		return nil, ErrReleaseLiftApply
	}
	want, _ := cloneOrkaDoc(desired)
	wm, _ := want["metadata"].(map[string]any)
	am, _ := actual["metadata"].(map[string]any)
	if am["name"] != wm["name"] || am["namespace"] != wm["namespace"] || am["deletionTimestamp"] != nil || !reflect.DeepEqual(am["labels"], wm["labels"]) || !reflect.DeepEqual(am["annotations"], wm["annotations"]) {
		return nil, ErrReleaseLiftApply
	}
	// Require authored fields to survive admission exactly. Server-added defaults
	// are retained by the preflight/create comparison and final whole-spec proof.
	var contains func(any, any) bool
	contains = func(want, got any) bool {
		switch w := want.(type) {
		case map[string]any:
			g, ok := got.(map[string]any)
			if !ok {
				return false
			}
			for k, v := range w {
				if !contains(v, g[k]) {
					return false
				}
			}
			return true
		case []any:
			g, ok := got.([]any)
			if !ok || len(w) != len(g) {
				return false
			}
			for i := range w {
				if !contains(w[i], g[i]) {
					return false
				}
			}
			return true
		default:
			return reflect.DeepEqual(want, got)
		}
	}
	if !contains(want["spec"], actual["spec"]) || !contains(want["data"], actual["data"]) {
		return nil, ErrReleaseLiftApply
	}
	return actual, nil
}

func (a *App) recheckImageSubject(ctx context.Context, x *releaseLiftImage) error {
	fresh, err := a.prepareImageCandidate(ctx, x.options)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(fresh.subject, x.subject) {
		return ErrReleaseLiftApply
	}
	raw, err := readImagePassingFile(x.options.Evidence)
	if err != nil {
		return err
	}
	e, err := decodeImagePassing(ctx, raw, x.suite)
	if err != nil || !reflect.DeepEqual(e, x.evidence) {
		return ErrReleaseLiftApply
	}
	return nil
}

func (p *PreparedReleaseLift) oneRuntime() bool {
	n := 0
	if p.native != nil {
		n++
	}
	if p.kagent != nil {
		n++
	}
	if p.image != nil {
		n++
	}
	return n == 1
}

func (a *App) prepareImageCandidate(ctx context.Context, opt ReleaseLiftOptions) (*releaseLiftImage, error) {
	r, artifact, repo, err := suiteoras.ReadImageReleaseArtifact(ctx, opt.Reference, opt.FromLayout, opt.PlainHTTP)
	if err != nil {
		return nil, err
	}
	suite, err := workspace.LoadImageArtifact(ctx, artifact)
	if err != nil {
		return nil, err
	}
	env, err := ReadSuiteEnvironment(opt.Environment)
	if err != nil {
		return nil, err
	}
	if env.Context != opt.ToContext || opt.ToNamespace != "" && env.Namespace != opt.ToNamespace || len(env.Members) != 1 || env.Platform != r.Images[0].Platform || env.PlainHTTP != opt.PlainHTTP {
		return nil, ErrReleaseLift
	}
	slot := r.Images[0]
	binding, ok := env.Members[slot.Member]
	if !ok || binding.Inference == nil {
		return nil, ErrReleaseLift
	}
	ref := repo + "@" + slot.Image.Digest.String()
	if binding.Image != "" && binding.Image != ref {
		return nil, ErrReleaseLift
	}
	binding.Image = ref
	env.Members[slot.Member] = binding
	image, err := suiteoras.ResolveImageReleaseMember(ctx, opt.Reference, opt.FromLayout, opt.PlainHTTP, slot)
	if err != nil {
		return nil, err
	}
	files := artifact.Files()
	var root spec.Suite
	var member spec.Agent
	if spec.Unmarshal(files["agentsuite.json"], &root) != nil || spec.Unmarshal(files[root.Agents[0].Path], &member) != nil {
		return nil, ErrReleaseLift
	}
	child := env
	child.Instance = env.Name
	child.Members = nil
	child.Name = binding.Name
	child.Inputs = binding.Inputs
	child.Inference = binding.Inference
	child.Instructions = string(files[member.Instructions.Path])
	plan, err := imagelift.Render(ref, image.Deployment, child)
	if err != nil {
		return nil, err
	}
	worker := a.releaseLiftWorker(ctx, opt)
	if err := imagelift.CheckTarget(ctx, imageLiftCluster{app: worker}, plan); err != nil {
		return nil, err
	}
	raw, err := worker.orkaCapture(ctx, nil, "get", "namespace", env.Namespace, "-o", "json")
	if err != nil {
		return nil, err
	}
	var ns struct{ Metadata struct{ UID string } }
	if json.Unmarshal(raw, &ns) != nil || ns.Metadata.UID == "" {
		return nil, ErrReleaseLift
	}
	observations := map[string]string{}
	for _, ref := range plan.Secrets {
		// Only key names and identity metadata cross this observation boundary.
		raw, err := worker.orkaCapture(ctx, nil, "-n", env.Namespace, "get", "secret", ref.Name, "-o", "go-template={{.metadata.uid}}|{{.metadata.resourceVersion}}|{{range $k,$v := .data}}{{$k}};{{end}}")
		if err != nil {
			return nil, err
		}
		parts := strings.Split(string(raw), "|")
		if len(parts) != 3 || parts[0] == "" || parts[1] == "" || ref.Key != "" && !strings.Contains(";"+parts[2], ";"+ref.Key+";") {
			return nil, ErrReleaseLift
		}
		observations[ref.Name+"/"+ref.Key] = parts[0] + "/" + parts[1]
	}
	bound, err := spec.Marshal(struct {
		Environment imagelift.Environment
		Secrets     map[string]string
	}{env, observations})
	if err != nil {
		return nil, err
	}
	configuration := ""
	for _, obj := range plan.Objects {
		if obj["kind"] == "ConfigMap" {
			configuration = digest.FromString(obj["data"].(imagelift.Object)["agent.json"].(string)).String()
		}
	}
	_, releaseDigest, _ := strings.Cut(opt.Reference, "@")
	subject := suiteImageSubject{ReleaseDigest: releaseDigest, SuiteDigest: suite.Digest, Member: suite.MemberID, ImageDigest: slot.Image.Digest.String(), Profile: "kmx.image-http/v1", ConfigurationDigest: configuration, Model: binding.Inference.Model, Target: ReleaseLiftTarget{Context: env.Context, ClusterUID: env.ClusterUID, Namespace: env.Namespace, NamespaceUID: ns.Metadata.UID}, BindingDigest: digest.FromBytes(bound).String()}
	// Promotion permits physical target/credential revisions only. All other
	// binding fields, including endpoint, model, inputs, placement names and image
	// retrieval reference, remain exact; newly added fields compare by default.
	executionEnv := cloneReleaseEnvironment(env)
	executionEnv.Context, executionEnv.ClusterUID, executionEnv.Namespace = "", "", ""
	execution, err := spec.Marshal(executionEnv)
	if err != nil {
		return nil, err
	}
	subject.ExecutionDigest = digest.FromBytes(execution).String()
	return &releaseLiftImage{suite: suite, env: env, plan: plan, subject: subject, options: opt}, nil
}

func (a *App) prepareReleaseLiftImage(ctx context.Context, opt ReleaseLiftOptions) (*PreparedReleaseLift, error) {
	raw, err := readImagePassingFile(opt.Evidence)
	if err != nil {
		return nil, err
	}
	x, err := a.prepareImageCandidate(ctx, opt)
	if err != nil {
		return nil, err
	}
	e, err := decodeImagePassing(ctx, raw, x.suite)
	if err != nil {
		return nil, err
	}
	gate := "same-subject"
	if opt.Promote {
		source, destination := e.Subject, x.subject
		source.Target, destination.Target = ReleaseLiftTarget{}, ReleaseLiftTarget{}
		source.BindingDigest, destination.BindingDigest = "", ""
		if !reflect.DeepEqual(source, destination) {
			return nil, errors.New("image promotion requires identical release, image, model, endpoint and configuration")
		}
		gate = "promotion"
	} else if !reflect.DeepEqual(e.Subject, x.subject) {
		return nil, errors.New("image evidence differs from destination subject")
	}
	x.evidence = e
	s := ReleaseLiftSummary{Suite: x.suite.Name, SuiteDigest: x.suite.Digest, Member: x.suite.MemberID, Model: x.subject.Model, Runtime: "image-http", Target: x.subject.Target, SourceTarget: e.Subject.Target, BindingDigest: x.subject.BindingDigest, RenderedDigest: x.plan.Digest, EvidenceDigest: digest.FromBytes(raw).String(), EvidenceRunID: e.RunID, Gate: gate, Promote: opt.Promote}
	worker := a.releaseLiftWorker(ctx, opt)
	for i, doc := range x.plan.Objects {
		row, err := worker.inspectImageReleaseResource(ctx, x, doc, &e.Resources[i])
		if err != nil {
			return nil, err
		}
		if opt.Promote && row.Action != "create" {
			return nil, errors.New("image promotion requires absent destination resources")
		}
		s.Resources = append(s.Resources, row)
	}
	body, err := spec.Marshal(struct {
		Domain string
		Plan   ReleaseLiftSummary
	}{"kmx.image-release.plan/v1", s})
	if err != nil {
		return nil, err
	}
	s.PlanDigest = digest.FromBytes(body).String()
	return &PreparedReleaseLift{options: opt, image: x, summary: s}, nil
}

func readImagePassingFile(path string) ([]byte, error) {
	root, name, parent, err := openSessionReferenceDirectory(path)
	if err != nil {
		return nil, errSuitePassingFile
	}
	defer root.Close()
	if parent.Mode().Perm()&0077 != 0 {
		return nil, errSuitePassingFile
	}
	before, err := root.Lstat(name)
	if err != nil || !before.Mode().IsRegular() || before.Mode().Perm()&0077 != 0 || before.Size() > maxSuitePassingReceiptBytes {
		return nil, errSuitePassingFile
	}
	dir, err := root.Open(".")
	if err != nil {
		return nil, errSuitePassingFile
	}
	defer dir.Close()
	f, err := openKagentFile(dir, name)
	if err != nil {
		return nil, errSuitePassingFile
	}
	defer f.Close()
	after, err := f.Stat()
	if err != nil || !os.SameFile(before, after) {
		return nil, errSuitePassingFile
	}
	raw, err := io.ReadAll(io.LimitReader(f, maxSuitePassingReceiptBytes+1))
	if err != nil || len(raw) > maxSuitePassingReceiptBytes {
		return nil, errSuitePassingFile
	}
	return raw, nil
}

func imageObjectDigest(doc map[string]any) (string, error) {
	body, err := spec.Marshal(struct{ Spec, Data any }{doc["spec"], doc["data"]})
	if err != nil {
		return "", err
	}
	return digest.FromBytes(body).String(), nil
}

func (a *App) inspectImageReleaseResource(ctx context.Context, x *releaseLiftImage, doc map[string]any, proof *ReleaseLiftResource) (ReleaseLiftResource, error) {
	kind := doc["kind"].(string)
	row := ReleaseLiftResource{Kind: kind, Name: x.plan.Name, Action: "create"}
	raw, err := a.orkaCapture(ctx, nil, "-n", x.env.Namespace, "get", strings.ToLower(kind), x.plan.Name, "--ignore-not-found=true", "-o", "json")
	if err != nil {
		return row, err
	}
	if len(raw) == 0 {
		row.SpecDigest, err = imageObjectDigest(doc)
		return row, err
	}
	var live map[string]any
	if json.Unmarshal(raw, &live) != nil {
		return row, ErrReleaseLift
	}
	meta, _ := live["metadata"].(map[string]any)
	uid, _ := meta["uid"].(string)
	rv, _ := meta["resourceVersion"].(string)
	generation, _ := meta["generation"].(float64)
	ann, _ := meta["annotations"].(map[string]any)
	labels, _ := meta["labels"].(map[string]any)
	if live["apiVersion"] != doc["apiVersion"] || live["kind"] != kind || meta["name"] != x.plan.Name || meta["namespace"] != x.env.Namespace || uid == "" || rv == "" || meta["deletionTimestamp"] != nil || ann[imagelift.PlanAnnotation] != x.plan.Digest || labels[imagelift.OwnerLabel] != x.plan.Owner || ann[imagelift.SubjectAnnotation] != x.plan.Owner {
		return row, ErrReleaseLift
	}
	d, err := imageObjectDigest(live)
	if err != nil {
		return row, err
	}
	if proof != nil && (proof.UID != uid || proof.SpecDigest != d || proof.Generation != int64(generation)) {
		return row, ErrReleaseLift
	}
	row.Action, row.UID, row.ResourceVersion, row.Generation, row.SpecDigest = "reuse", uid, rv, int64(generation), d
	if kind == "Deployment" {
		var observed struct {
			Status struct {
				ObservedGeneration                 int64
				UpdatedReplicas, AvailableReplicas int
			}
		}
		if json.Unmarshal(raw, &observed) != nil || observed.Status.ObservedGeneration < int64(generation) || observed.Status.UpdatedReplicas != 1 || observed.Status.AvailableReplicas != 1 {
			return row, fmt.Errorf("image deployment is not ready for its generation")
		}
	}
	return row, nil
}

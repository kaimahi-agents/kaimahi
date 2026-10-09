package app

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/guard"
	agentruntime "github.com/kaimahi-agents/kaimahi/internal/kmx/runtime"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/scaffold"
)

const (
	kagentPinnedCommit = "68df64f671800c37c4204d81ebe0dd66ec35d223"

	kagentControllerImage     = "ghcr.io/kagent-dev/kagent/controller:0.10.2"
	kagentControllerImageRepo = "ghcr.io/kagent-dev/kagent/controller"
	kagentGoImage             = "ghcr.io/kagent-dev/kagent/golang-adk:0.10.2"
	kagentGoImageRepo         = "ghcr.io/kagent-dev/kagent/golang-adk"
	kagentPythonImage         = "ghcr.io/kagent-dev/kagent/app:0.10.2"
	kagentPythonImageRepo     = "ghcr.io/kagent-dev/kagent/app"

	kagentControllerIndexDigest = "6adeef9ac70056e5871773a78233a77f12d1357f9f3484aaee59fe8abc6450b9"
	kagentControllerAMD64Digest = "01236c6253abb2f35e25fa4af463c06780052065b7161eb29f3b162703e29075"
	kagentControllerARM64Digest = "bf41012d08f381c37456f2f97becd9c09472a19dae7418d2a5684043533f36a1"
	kagentGoIndexDigest         = "c1ae7865e4c4ed1d4708bbcc04ea952c54c3014a3da3178427bf31922d0956dd"
	kagentGoAMD64Digest         = "922dd44552c55cad46c9e38c9d56b1138a682114e665e89b60caebde8f1952f6"
	kagentGoARM64Digest         = "0b8e0c9b04304240efab0bee39d943240cd88f852760af01b8da25b23e4c6245"
	kagentPythonIndexDigest     = "9eb019e6a0bb5fc1784d3b77025060657153b5cf9b291f63048d9c9f7451435c"
	kagentPythonAMD64Digest     = "7dfa664fde480f5504098e5937492312c218091996857c32c5269c67e5a1048f"
	kagentPythonARM64Digest     = "6bc282009e42cd507db291b9b8335e6662b64fa02e655d8797985b1f314b51f5"
)

// Variables keep tests small: production always starts from the exact
// canonical v0.10.2 schema digests below.
var (
	kagentAgentSchemaSHA256 = "b6048ddf43a7fff6ea11bb168b09d737c78ee6e36afd57df2f3a050f480e80c6"
	kagentModelSchemaSHA256 = "1742024902c0987df9f9131ece110aa2746e9a5a91055d96f91ba3e5b2ca48a3"
	kagentPollInterval      = time.Second
	kagentPreflightTimeout  = 5 * time.Minute
	kagentReadinessTimeout  = 10 * time.Minute
	kagentAgentCardTimeout  = 5 * time.Minute
	kagentTaskTimeout       = 5 * time.Minute
)

type kagentControllerService struct {
	Name, Namespace, UID, ResourceVersion string
	Selector                              map[string]string
	DeploymentName                        string
	DeploymentUID                         string
	DeploymentResourceVersion             string
	ControllerServiceAccount              string
	ConfigMapName                         string
	ConfigMapUID                          string
	ConfigMapResourceVersion              string
	Runtime, AuthMode, TargetNamespace    string
}

type kagentMetadata struct {
	Name            string            `json:"name"`
	Namespace       string            `json:"namespace"`
	UID             string            `json:"uid"`
	ResourceVersion string            `json:"resourceVersion"`
	Generation      int64             `json:"generation"`
	Labels          map[string]string `json:"labels"`
	Annotations     map[string]string `json:"annotations"`
	OwnerReferences []struct {
		APIVersion string `json:"apiVersion"`
		Kind       string `json:"kind"`
		Name       string `json:"name"`
		UID        string `json:"uid"`
		Controller *bool  `json:"controller"`
	} `json:"ownerReferences"`
	DeletionTimestamp *time.Time `json:"deletionTimestamp"`
}

type kagentContainer struct {
	Name    string   `json:"name"`
	Image   string   `json:"image"`
	Command []string `json:"command"`
	Args    []string `json:"args"`
	Env     []struct {
		Name      string          `json:"name"`
		Value     string          `json:"value"`
		ValueFrom json.RawMessage `json:"valueFrom"`
	} `json:"env"`
	EnvFrom []struct {
		Prefix       string `json:"prefix"`
		ConfigMapRef *struct {
			Name     string `json:"name"`
			Optional *bool  `json:"optional"`
		} `json:"configMapRef"`
		SecretRef json.RawMessage `json:"secretRef"`
	} `json:"envFrom"`
}

type kagentDeployment struct {
	Kind     string         `json:"kind"`
	Metadata kagentMetadata `json:"metadata"`
	Spec     struct {
		Replicas *int32 `json:"replicas"`
		Selector struct {
			MatchLabels      map[string]string `json:"matchLabels"`
			MatchExpressions []json.RawMessage `json:"matchExpressions"`
		} `json:"selector"`
		Template struct {
			Metadata kagentMetadata `json:"metadata"`
			Spec     struct {
				ServiceAccountName string            `json:"serviceAccountName"`
				Containers         []kagentContainer `json:"containers"`
			} `json:"spec"`
		} `json:"template"`
	} `json:"spec"`
	Status struct {
		ObservedGeneration  int64 `json:"observedGeneration"`
		Replicas            int32 `json:"replicas"`
		UpdatedReplicas     int32 `json:"updatedReplicas"`
		ReadyReplicas       int32 `json:"readyReplicas"`
		AvailableReplicas   int32 `json:"availableReplicas"`
		UnavailableReplicas int32 `json:"unavailableReplicas"`
	} `json:"status"`
}

type kagentWorkloadIdentity struct {
	DeploymentUID, DeploymentResourceVersion string
	DeploymentGeneration                     int64
	ServiceUID, ServiceResourceVersion       string
}

// kagentCapture is the only Kagent kubectl boundary. It pins context, bounds
// process and response size, accepts stdin for strict creates/raw A2A, and
// never propagates kubectl stderr because admission and transport errors may
// echo a prompt or manifest field.
func (a *App) kagentCapture(ctx context.Context, stdin []byte, args ...string) ([]byte, error) {
	return a.kagentCaptureWithTimeout(ctx, 15*time.Second, "10s", stdin, args...)
}

func (a *App) kagentCaptureWithTimeout(ctx context.Context, timeout time.Duration, requestTimeout string, stdin []byte, args ...string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	callCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	prepared := a.Command(append([]string{"--request-timeout=" + requestTimeout}, args...)...)
	cmd := exec.CommandContext(callCtx, prepared.Path, prepared.Args[1:]...)
	cmd.Env = prepared.Env
	cmd.WaitDelay = time.Second
	cmd.Stdin = bytes.NewReader(stdin)
	out := &orkaBoundedBuffer{remaining: 4 << 20}
	stderr := &orkaBoundedBuffer{remaining: 4 << 10}
	cmd.Stdout, cmd.Stderr = out, stderr
	if err := cmd.Run(); err != nil {
		if callCtx.Err() != nil {
			return nil, fmt.Errorf("kubectl request cancelled or timed out")
		}
		reason := strings.ToLower(stderr.buffer.String())
		switch {
		case strings.Contains(reason, "notfound"), strings.Contains(reason, "not found"):
			return nil, fmt.Errorf("kubectl request failed: requested Kubernetes resource not found")
		case strings.Contains(reason, "forbidden"), strings.Contains(reason, "unauthorized"):
			return nil, fmt.Errorf("kubectl request failed: access forbidden")
		default:
			return nil, fmt.Errorf("kubectl request failed; check permissions, prerequisites and the selected context")
		}
	}
	return append([]byte(nil), out.buffer.Bytes()...), nil
}

func (a *App) guardKagentCreate(ctx context.Context, opt CreateOptions) error {
	if a.guarded {
		return nil
	}
	raw, err := a.kagentCapture(ctx, nil, "config", "view", "-o", "json")
	if err != nil {
		return fmt.Errorf("cannot read context metadata for the Kagent mutation guard: %w", err)
	}
	cfg, err := guard.ParseKubeconfig(raw)
	if err != nil {
		return fmt.Errorf("cannot decode context metadata for the Kagent mutation guard")
	}
	// Do not reuse InvocationCommand here: it can contain the --task prompt.
	// The mutation guard must never echo that prompt into diagnostics.
	command := a.operationCommand("agent", "create", opt.Name, "--runtime", "kagent")
	action := "create Kagent ModelConfig and Agent in " + opt.Namespace
	if opt.Task != "" {
		action += "; send one A2A message; Kagent stores the full task prompt, history, and answer in its database, default upstream session retention is unlimited, and Kubernetes audit policy may capture Service-proxy request and response bodies"
	}
	if opt.DryRun {
		action = "server dry-run Kagent resources in " + opt.Namespace
	}
	if err := guard.CheckContext(ctx, cfg, guard.Request{
		Action: action, Context: a.Cfg.KubeContext, Source: a.Cfg.ContextSource,
		Namespaces: opt.Namespace, Confirm: a.Cfg.Confirm, Command: command,
	}, a.Err, a.Stdin); err != nil {
		return err
	}
	a.guarded = true
	return nil
}

func (a *App) createKagentStaged(ctx context.Context, opt CreateOptions, bundle *scaffold.KagentBundle, rendered agentruntime.RenderedBundle) (result agentruntime.DeployResult, err error) {
	if err := a.guardKagentCreate(ctx, opt); err != nil {
		return result, err
	}
	operationCtx := ctx
	ctx, cancelPreflight := a.waitContext(operationCtx, "kagent-preflight", kagentPreflightTimeout)
	defer cancelPreflight()
	service, err := a.preflightKagentInstallation(ctx, opt.KagentRuntime, opt.Namespace)
	if err != nil {
		return result, err
	}
	if opt.Task != "" && service.AuthMode == "trusted-proxy" {
		return result, fmt.Errorf("Kagent --task refuses controller AUTH_MODE trusted-proxy because this create path has no Kagent bearer credential; no Kubernetes resources were changed")
	}
	clusterUID := ""
	if !opt.DryRun {
		clusterUID, err = a.kagentClusterUID(ctx)
		if err != nil {
			return result, fmt.Errorf("%w; no Kubernetes resources were changed", err)
		}
	}
	writeDocs, err := kagentWriteDocuments(bundle, rendered)
	if err != nil {
		return result, err
	}
	modelSpec := bundle.ModelConfig["spec"].(map[string]any)
	secret, _ := modelSpec["apiKeySecret"].(string)
	key, _ := modelSpec["apiKeySecretKey"].(string)
	if secret == opt.Name {
		return result, fmt.Errorf("Kagent model Secret name must differ from Agent name %s because the controller would overwrite its same-name generated Secret; no Kubernetes resources were changed", opt.Name)
	}
	if err := a.kagentGeneratedChildrenAbsent(ctx, opt.Namespace, opt.Name); err != nil {
		return result, err
	}
	for _, doc := range []map[string]any{bundle.ModelConfig, bundle.Agent} {
		if err := a.kagentAbsent(ctx, opt.Namespace, doc); err != nil {
			return result, err
		}
	}
	if err := a.kagentSecretPresent(ctx, opt.Namespace, secret, key); err != nil {
		return result, err
	}
	toolSnapshots, err := a.snapshotKagentTools(ctx, bundle)
	if err != nil {
		return result, err
	}
	if err := a.reverifyKagentInstallation(ctx, service); err != nil {
		return result, fmt.Errorf("cannot reverify exact Kagent v0.10.2 before ModelConfig server dry-run: %w; no Kubernetes resources were changed", err)
	}
	admitted := make([]kagentObjectProof, 0, len(writeDocs))
	for _, doc := range writeDocs {
		if doc["kind"] == "Agent" {
			if err := a.reverifyKagentInstallation(ctx, service); err != nil {
				return result, fmt.Errorf("cannot reverify exact Kagent v0.10.2 before Agent server dry-run: %w; no Kubernetes resources were changed", err)
			}
		}
		body, err := json.Marshal(doc)
		if err != nil {
			return result, err
		}
		admissionCtx, cancelAdmission := a.waitContext(ctx, "kagent-admission", 0)
		raw, err := a.kagentCapture(admissionCtx, body, "-n", opt.Namespace, "create", "--dry-run=server", "--validate=strict", "-f", "-", "-o", "json")
		cancelAdmission()
		if err != nil {
			return result, fmt.Errorf("%s strict server create preflight failed; no Kubernetes resources were changed: %w", doc["kind"], err)
		}
		proof, err := validateKagentAdmission(raw, doc, opt.Namespace)
		if err != nil {
			return result, fmt.Errorf("%s strict server create preflight mutated the reviewed object; no Kubernetes resources were changed: %w", doc["kind"], err)
		}
		admitted = append(admitted, proof)
	}
	// Close collision/dependency races after every admission read and before
	// emitting the artifact. Create itself remains the final atomic collision
	// check; an ambiguous create is never retried or adopted.
	for _, doc := range []map[string]any{bundle.ModelConfig, bundle.Agent} {
		if err := a.kagentAbsent(ctx, opt.Namespace, doc); err != nil {
			return result, err
		}
	}
	if err := a.kagentGeneratedChildrenAbsent(ctx, opt.Namespace, opt.Name); err != nil {
		return result, err
	}
	if err := a.kagentSecretPresent(ctx, opt.Namespace, secret, key); err != nil {
		return result, err
	}
	if err := a.verifyKagentToolSnapshots(ctx, bundle, toolSnapshots); err != nil {
		return result, err
	}
	document, err := scaffold.KagentArtifact(rendered.Documents())
	if err != nil {
		return result, err
	}
	// Re-read the platform identity as the final cluster preflight. If the sole
	// labeled Service, exact version, or CRDs changed during admission checks,
	// no artifact or Kubernetes resource is written.
	if err := a.reverifyKagentInstallation(ctx, service); err != nil {
		return result, fmt.Errorf("%w; no Kubernetes resources were changed", err)
	}
	if opt.DryRun {
		if err := a.emitKagentArtifact(opt, document, false); err != nil {
			return result, err
		}
		a.notef("Kagent bundle passed exact-version, CRD, dependency, and strict server admission preflights; not applied and no task was sent.")
		return result, nil
	}
	// Artifact emission is the last preflight and happens before the first
	// actual create. A write failure here therefore cannot leave an unreviewed
	// cluster object behind.
	if err := a.emitKagentArtifact(opt, document, true); err != nil {
		return result, err
	}
	if err := ctx.Err(); err != nil {
		return result, fmt.Errorf("Kagent pre-create preflight timed out; no Kubernetes resources were changed: %w", err)
	}
	cancelPreflight()
	ctx = operationCtx
	var created []string
	defer func() {
		if err == nil {
			return
		}
		a.notef("Stopped; no later Kagent resources or messages were attempted and nothing was rolled back. Created: %s", strings.Join(created, ", "))
		err = fmt.Errorf("%w\nKagent create is create-only: rerunning will refuse any existing ModelConfig or Agent. Inspect the named resources and remove only what you intentionally want recreated", err)
	}()
	identities := make([]kagentIdentity, 0, 2)
	var workload kagentWorkloadIdentity
	var modelSecretHash string
	for i, doc := range writeDocs {
		if err := a.reverifyKagentPlatformAndCluster(ctx, service, clusterUID); err != nil {
			return result, fmt.Errorf("cannot reverify exact Kagent v0.10.2 immediately before %s create: %w", doc["kind"], err)
		}
		if doc["kind"] == "Agent" {
			if _, err := a.verifyKagentModelState(ctx, opt.Namespace, identities[0], modelSecretHash); err != nil {
				return result, fmt.Errorf("Kagent ModelConfig changed before Agent create: %w", err)
			}
			if err := a.kagentSecretPresent(ctx, opt.Namespace, secret, key); err != nil {
				return result, err
			}
			if err := a.verifyKagentToolSnapshots(ctx, bundle, toolSnapshots); err != nil {
				return result, err
			}
		}
		if err := a.kagentAbsent(ctx, opt.Namespace, doc); err != nil {
			return result, err
		}
		if err := a.kagentGeneratedChildrenAbsent(ctx, opt.Namespace, opt.Name); err != nil {
			return result, err
		}
		id, createErr := a.createKagentObject(ctx, opt.Namespace, doc, admitted[i])
		if createErr != nil {
			return result, createErr
		}
		created = append(created, id.Kind+"/"+id.Name+" UID "+id.UID)
		identities = append(identities, id)
		a.notef("Created %s/%s (UID %s); waiting for current-generation conditions.", id.Kind, id.Name, id.UID)
		readyCtx, cancelReady := a.waitContext(ctx, "kagent-readiness-"+id.Kind, kagentReadinessTimeout)
		readyErr := a.waitKagentReady(readyCtx, opt.Namespace, id)
		cancelReady()
		if readyErr != nil {
			return result, readyErr
		}
		if id.Kind == "ModelConfig" {
			modelSecretHash, err = a.verifyKagentModelState(ctx, opt.Namespace, id, "")
			if err != nil {
				return result, err
			}
		} else {
			workload, err = a.verifyKagentAgentWorkload(ctx, opt.Namespace, id, opt.KagentRuntime, nil)
			if err != nil {
				return result, err
			}
			if _, err := a.verifyKagentModelState(ctx, opt.Namespace, identities[0], modelSecretHash); err != nil {
				return result, fmt.Errorf("Kagent ModelConfig changed while Agent became Ready: %w", err)
			}
			if err := a.verifyKagentToolSnapshots(ctx, bundle, toolSnapshots); err != nil {
				return result, fmt.Errorf("Kagent tool dependency changed while Agent became Ready: %w", err)
			}
		}
	}
	if opt.Task != "" {
		agentID := identities[1]
		if err := a.reverifyKagentPlatformAndCluster(ctx, service, clusterUID); err != nil {
			return result, fmt.Errorf("cannot reverify exact Kagent v0.10.2 immediately before task send: %w", err)
		}
		cardCtx, cancelCard := a.waitContext(ctx, "kagent-agent-card", kagentAgentCardTimeout)
		cardErr := a.waitKagentAgentCard(cardCtx, service, opt.Namespace, opt.Name)
		cancelCard()
		if cardErr != nil {
			return result, cardErr
		}
		if err := a.reverifyKagentPlatformAndCluster(ctx, service, clusterUID); err != nil {
			return result, fmt.Errorf("exact Kagent v0.10.2 installation changed immediately before task send: %w", err)
		}
		if err := a.verifyKagentAgentCard(ctx, service, opt.Namespace, opt.Name); err != nil {
			return result, fmt.Errorf("Kagent A2A agent card changed immediately before task send: %w", err)
		}
		if _, err := a.verifyKagentAgentWorkload(ctx, opt.Namespace, agentID, opt.KagentRuntime, &workload); err != nil {
			return result, fmt.Errorf("Kagent Agent identity or workload changed immediately before task send: %w", err)
		}
		if _, err := a.verifyKagentModelState(ctx, opt.Namespace, identities[0], modelSecretHash); err != nil {
			return result, fmt.Errorf("Kagent ModelConfig changed immediately before task send: %w", err)
		}
		if err := a.verifyKagentToolSnapshots(ctx, bundle, toolSnapshots); err != nil {
			return result, fmt.Errorf("Kagent tool dependency changed immediately before task send: %w", err)
		}
		taskCtx, cancelTask := a.waitContext(ctx, "kagent-task", kagentTaskTimeout)
		answer, err := a.sendKagentTask(taskCtx, service, opt.Namespace, opt.Name, opt.Task)
		cancelTask()
		if err != nil {
			return result, err
		}
		if _, err := a.verifyKagentAgentWorkload(ctx, opt.Namespace, agentID, opt.KagentRuntime, &workload); err != nil {
			return result, fmt.Errorf("Kagent Agent identity or workload changed during task execution; the message was not retried: %w", err)
		}
		if err := a.verifyKagentAgentCard(ctx, service, opt.Namespace, opt.Name); err != nil {
			return result, fmt.Errorf("Kagent A2A agent card changed during task execution; the message was not retried: %w", err)
		}
		if err := a.reverifyKagentPlatformAndCluster(ctx, service, clusterUID); err != nil {
			return result, fmt.Errorf("exact Kagent v0.10.2 installation changed during task execution; the message was not retried: %w", err)
		}
		if _, err := a.verifyKagentModelState(ctx, opt.Namespace, identities[0], modelSecretHash); err != nil {
			return result, fmt.Errorf("Kagent ModelConfig changed during task execution; the message was not retried: %w", err)
		}
		if err := a.verifyKagentToolSnapshots(ctx, bundle, toolSnapshots); err != nil {
			return result, fmt.Errorf("Kagent tool dependency changed during task execution; the message was not retried: %w", err)
		}
		if _, err := fmt.Fprintln(a.Out, answer); err != nil {
			return result, fmt.Errorf("Kagent A2A message completed but its answer could not be printed; the message was not resent: %w", err)
		}
		a.notef("Kagent Agent is Ready and one A2A message completed; the message was not retried.")
	} else {
		a.notef("Kagent ModelConfig is Accepted and Agent is Accepted and Ready; no model response was tested.")
	}
	kubeContext := ""
	if a.Cfg != nil {
		kubeContext = a.Cfg.KubeContext
	}
	agentID := identities[1]
	result.Ref = agentruntime.AgentRef{Runtime: agentruntime.Kagent, Context: kubeContext,
		Namespace: opt.Namespace, Kind: "agents.kagent.dev", Name: agentID.Name, UID: agentID.UID}
	result.Receipt = agentruntime.DeployReceipt{
		Bundle: opt.Name, PortableDigest: rendered.PortableDigest(), RenderedDigest: rendered.RenderedDigest(),
		Target: agentruntime.DeployTarget{Runtime: agentruntime.Kagent, Context: kubeContext, Namespace: opt.Namespace},
	}
	for _, id := range identities {
		result.Receipt.Resources = append(result.Receipt.Resources, agentruntime.ResourceResult{
			Kind: id.Kind, Name: id.Name, Namespace: opt.Namespace, UID: id.UID,
			Generation: id.Generation, Outcome: agentruntime.ResourceCreated,
		})
	}
	if _, err := a.verifyKagentAgentWorkload(ctx, opt.Namespace, agentID, opt.KagentRuntime, &workload); err != nil {
		return agentruntime.DeployResult{}, fmt.Errorf("Kagent Agent identity or workload changed before deployment receipt: %w", err)
	}
	if _, err := a.verifyKagentModelState(ctx, opt.Namespace, identities[0], modelSecretHash); err != nil {
		return agentruntime.DeployResult{}, fmt.Errorf("Kagent ModelConfig changed before deployment receipt: %w", err)
	}
	if err := a.verifyKagentToolSnapshots(ctx, bundle, toolSnapshots); err != nil {
		return agentruntime.DeployResult{}, fmt.Errorf("Kagent tool dependency changed before deployment receipt: %w", err)
	}
	if err := a.reverifyKagentPlatformAndCluster(ctx, service, clusterUID); err != nil {
		return agentruntime.DeployResult{}, fmt.Errorf("exact Kagent v0.10.2 installation or destination cluster changed immediately before deployment receipt: %w", err)
	}
	if err := writeKagentCreateReceipt(kagentBundlePath(opt), clusterUID, result); err != nil {
		return agentruntime.DeployResult{}, fmt.Errorf("persist Kagent create receipt after deployment success: %w", err)
	}
	return result, nil
}

func kagentWriteDocuments(bundle *scaffold.KagentBundle, rendered agentruntime.RenderedBundle) ([]map[string]any, error) {
	marker := map[string]any{
		"kaimahi.dev/bundle":          kagentObjectName(bundle.Agent),
		"kaimahi.dev/portable-digest": rendered.PortableDigest(),
		"kaimahi.dev/rendered-digest": rendered.RenderedDigest(),
	}
	docs := make([]map[string]any, 0, 2)
	for _, renderedDoc := range []map[string]any{bundle.ModelConfig, bundle.Agent} {
		doc, err := cloneOrkaDoc(renderedDoc)
		if err != nil {
			return nil, fmt.Errorf("prepare Kagent %s write payload: %w", renderedDoc["kind"], err)
		}
		addOrkaMarker(doc, marker)
		docs = append(docs, doc)
	}
	return docs, nil
}

func (a *App) preflightKagentInstallation(ctx context.Context, runtimeName, targetNamespace string) (kagentControllerService, error) {
	service, err := a.inspectKagentInstallation(ctx, runtimeName, targetNamespace)
	if err != nil {
		return kagentControllerService{}, fmt.Errorf("%w; no Kubernetes resources were changed", err)
	}
	if err := a.verifyKagentControllerRBAC(ctx, service, targetNamespace); err != nil {
		return kagentControllerService{}, fmt.Errorf("%w; no Kubernetes resources were changed", err)
	}
	return service, nil
}

func (a *App) inspectKagentInstallation(ctx context.Context, runtimeName, targetNamespace string) (kagentControllerService, error) {
	raw, err := a.kagentCapture(ctx, nil, "get", "services", "--all-namespaces", "-l", "app.kubernetes.io/part-of=kagent,app.kubernetes.io/component=controller", "-o", "json")
	if err != nil {
		return kagentControllerService{}, fmt.Errorf("cannot detect the Kagent controller Service: %w", err)
	}
	var list struct {
		Items []struct {
			Metadata kagentMetadata `json:"metadata"`
			Spec     struct {
				Ports []struct {
					Name, Protocol string
					Port           int32 `json:"port"`
				} `json:"ports"`
				Selector map[string]string `json:"selector"`
			} `json:"spec"`
		} `json:"items"`
	}
	if err := json.Unmarshal(raw, &list); err != nil {
		return kagentControllerService{}, fmt.Errorf("Kagent controller Service discovery returned invalid JSON")
	}
	var candidates []int
	for i, item := range list.Items {
		if slices.ContainsFunc(item.Spec.Ports, func(port struct {
			Name, Protocol string
			Port           int32 `json:"port"`
		}) bool {
			return port.Name == "controller" && port.Protocol == "TCP" && port.Port == 8083
		}) {
			candidates = append(candidates, i)
		}
	}
	if len(candidates) != 1 {
		return kagentControllerService{}, fmt.Errorf("expected exactly one labeled Kagent Service with named controller TCP port 8083, found %d", len(candidates))
	}
	item := list.Items[candidates[0]]
	if item.Metadata.Name == "" || item.Metadata.Namespace == "" ||
		item.Metadata.UID == "" || item.Metadata.ResourceVersion == "" || item.Metadata.DeletionTimestamp != nil ||
		item.Metadata.Labels["app.kubernetes.io/part-of"] != "kagent" ||
		item.Metadata.Labels["app.kubernetes.io/component"] != "controller" {
		return kagentControllerService{}, fmt.Errorf("the discovered Kagent controller Service has an invalid identity or labels")
	}
	if err := validateKagentVersionLabels(item.Metadata.Labels); err != nil {
		return kagentControllerService{}, fmt.Errorf("Kagent controller Service labels: %w", err)
	}
	if len(item.Spec.Selector) == 0 {
		return kagentControllerService{}, fmt.Errorf("Kagent controller Service has no selector")
	}
	service := kagentControllerService{
		Name: item.Metadata.Name, Namespace: item.Metadata.Namespace,
		UID: item.Metadata.UID, ResourceVersion: item.Metadata.ResourceVersion,
		Selector: copyKagentLabels(item.Spec.Selector), Runtime: runtimeName, TargetNamespace: targetNamespace,
	}
	deployment, err := a.readKagentControllerDeployment(ctx, service)
	if err != nil {
		return kagentControllerService{}, err
	}
	service.DeploymentName = deployment.Metadata.Name
	service.DeploymentUID = deployment.Metadata.UID
	service.DeploymentResourceVersion = deployment.Metadata.ResourceVersion
	service.ControllerServiceAccount = deployment.Spec.Template.Spec.ServiceAccountName
	if service.ControllerServiceAccount == "" {
		return kagentControllerService{}, fmt.Errorf("Kagent controller Deployment has no explicit serviceAccountName")
	}
	controller, err := exactKagentContainer(deployment.Spec.Template.Spec.Containers, "controller")
	if err != nil {
		return kagentControllerService{}, fmt.Errorf("Kagent controller Deployment: %w", err)
	}
	if controller.Image != kagentControllerImage {
		return kagentControllerService{}, fmt.Errorf("Kagent controller Deployment does not use official image %s", kagentControllerImage)
	}
	if len(controller.Command) != 0 || len(controller.Args) != 0 {
		return kagentControllerService{}, fmt.Errorf("Kagent controller Deployment overrides the official image command or arguments")
	}
	service.AuthMode, err = kagentControllerAuthMode(controller)
	if err != nil {
		return kagentControllerService{}, err
	}
	configMapName, err := kagentControllerConfigMapName(controller)
	if err != nil {
		return kagentControllerService{}, err
	}
	if configMapName != service.Name {
		return kagentControllerService{}, fmt.Errorf("Kagent controller image ConfigMap is not bound to the selected controller Service and Deployment")
	}
	configMap, err := a.readKagentControllerConfigMap(ctx, service.Namespace, configMapName, runtimeName)
	if err != nil {
		return kagentControllerService{}, err
	}
	service.ConfigMapName = configMap.Metadata.Name
	service.ConfigMapUID = configMap.Metadata.UID
	service.ConfigMapResourceVersion = configMap.Metadata.ResourceVersion
	if err := validateKagentWatchNamespaces(configMap.Data["WATCH_NAMESPACES"], targetNamespace); err != nil {
		return kagentControllerService{}, err
	}
	if err := a.verifyKagentDeploymentPods(ctx, service.Namespace, deployment, "controller", kagentControllerImage, kagentControllerImageRepo,
		[]string{kagentControllerIndexDigest, kagentControllerAMD64Digest, kagentControllerARM64Digest}); err != nil {
		return kagentControllerService{}, fmt.Errorf("Kagent controller workload: %w", err)
	}
	versionPath := kagentServiceProxyPath(service, "/version")
	raw, err = a.kagentCapture(ctx, nil, "get", "--raw", versionPath)
	if err != nil {
		return kagentControllerService{}, fmt.Errorf("cannot verify the live Kagent controller version: %w", err)
	}
	var version struct {
		KagentVersion string `json:"kagent_version"`
		GitCommit     string `json:"git_commit"`
	}
	if err := json.Unmarshal(raw, &version); err != nil {
		return kagentControllerService{}, fmt.Errorf("Kagent controller /version returned invalid JSON")
	}
	if !validPinnedKagentVersion(version.KagentVersion, version.GitCommit) {
		return kagentControllerService{}, fmt.Errorf("live Kagent controller is not exact v0.10.2 commit %s", kagentPinnedCommit)
	}
	for _, identity := range []kagentCRDIdentity{
		{Name: "modelconfigs.kagent.dev", Kind: "ModelConfig", Plural: "modelconfigs", SchemaSHA256: kagentModelSchemaSHA256},
		{Name: "agents.kagent.dev", Kind: "Agent", Plural: "agents", SchemaSHA256: kagentAgentSchemaSHA256},
	} {
		raw, err := a.kagentCapture(ctx, nil, "get", "crd", identity.Name, "-o", "json")
		if err != nil {
			return kagentControllerService{}, fmt.Errorf("cannot read installed %s CRD: %w", identity.Name, err)
		}
		if err := validateKagentCRD(raw, identity); err != nil {
			return kagentControllerService{}, fmt.Errorf("installed %s CRD is incompatible with exact Kagent v0.10.2: %w", identity.Name, err)
		}
	}
	return service, nil
}

func (a *App) reverifyKagentInstallation(ctx context.Context, service kagentControllerService) error {
	current, err := a.inspectKagentInstallation(ctx, service.Runtime, service.TargetNamespace)
	if err != nil {
		return err
	}
	if current.Name != service.Name || current.Namespace != service.Namespace || current.UID != service.UID ||
		current.ResourceVersion != service.ResourceVersion || !equalKagentLabels(current.Selector, service.Selector) ||
		current.DeploymentName != service.DeploymentName || current.DeploymentUID != service.DeploymentUID ||
		current.DeploymentResourceVersion != service.DeploymentResourceVersion ||
		current.ControllerServiceAccount != service.ControllerServiceAccount || current.TargetNamespace != service.TargetNamespace ||
		current.ConfigMapName != service.ConfigMapName || current.ConfigMapUID != service.ConfigMapUID ||
		current.ConfigMapResourceVersion != service.ConfigMapResourceVersion || current.AuthMode != service.AuthMode {
		return fmt.Errorf("the exact Kagent controller Service, Deployment, or image ConfigMap changed during preflight")
	}
	return nil
}

func validateKagentWatchNamespaces(value, targetNamespace string) error {
	if value == "" {
		return nil
	}
	seen := map[string]bool{}
	for _, namespace := range strings.Split(value, ",") {
		if namespace == "" || namespace != strings.TrimSpace(namespace) || scaffold.ValidateNamespace(namespace) != nil || seen[namespace] {
			return fmt.Errorf("Kagent controller WATCH_NAMESPACES is not an exact comma-separated namespace list")
		}
		seen[namespace] = true
	}
	if !seen[targetNamespace] {
		return fmt.Errorf("Kagent controller does not watch target namespace %s", targetNamespace)
	}
	return nil
}

func (a *App) verifyKagentControllerRBAC(ctx context.Context, service kagentControllerService, targetNamespace string) error {
	as := "system:serviceaccount:" + service.Namespace + ":" + service.ControllerServiceAccount
	checks := []struct {
		resource string
		verbs    []string
	}{
		{"agents.kagent.dev", []string{"get", "list", "watch"}},
		{"agents.kagent.dev/status", []string{"get", "update", "patch"}},
		{"modelconfigs.kagent.dev", []string{"get", "list", "watch"}},
		{"modelconfigs.kagent.dev/status", []string{"get", "update", "patch"}},
		{"remotemcpservers.kagent.dev", []string{"get", "list", "watch"}},
		{"secrets", []string{"get", "list", "watch", "create", "update", "patch", "delete"}},
		{"configmaps", []string{"get", "list", "watch", "create", "update", "patch", "delete"}},
		{"serviceaccounts", []string{"get", "list", "watch", "create", "update", "patch", "delete"}},
		{"deployments.apps", []string{"get", "list", "watch", "create", "update", "patch", "delete"}},
		{"services", []string{"get", "list", "watch", "create", "update", "patch", "delete"}},
	}
	for _, check := range checks {
		for _, verb := range check.verbs {
			raw, err := a.kagentCapture(ctx, nil, "auth", "can-i", verb, check.resource, "--as="+as, "-n", targetNamespace)
			if err != nil {
				return fmt.Errorf("cannot prove Kagent controller RBAC for %s %s in namespace %s: %w", verb, check.resource, targetNamespace, err)
			}
			if string(bytes.TrimSpace(raw)) != "yes" {
				return fmt.Errorf("Kagent controller ServiceAccount %s is not allowed to %s %s in target namespace %s", service.ControllerServiceAccount, verb, check.resource, targetNamespace)
			}
		}
	}
	return nil
}

func (a *App) reverifyKagentPlatformAndCluster(ctx context.Context, service kagentControllerService, clusterUID string) error {
	if err := a.reverifyKagentInstallation(ctx, service); err != nil {
		return err
	}
	currentUID, err := a.kagentClusterUID(ctx)
	if err != nil {
		return err
	}
	if currentUID != clusterUID {
		return fmt.Errorf("the destination cluster identity changed during Kagent create")
	}
	return nil
}

func validateKagentVersionLabels(labels map[string]string) error {
	if version := labels["app.kubernetes.io/version"]; version != "" && version != "0.10.2" {
		return fmt.Errorf("app.kubernetes.io/version must equal 0.10.2 when present")
	}
	if chart := labels["helm.sh/chart"]; chart != "" && chart != "kagent-0.10.2" {
		return fmt.Errorf("helm.sh/chart must equal kagent-0.10.2 when present")
	}
	return nil
}

func copyKagentLabels(source map[string]string) map[string]string {
	out := make(map[string]string, len(source))
	for key, value := range source {
		out[key] = value
	}
	return out
}

func equalKagentLabels(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for key, value := range a {
		if b[key] != value {
			return false
		}
	}
	return true
}

func kagentSelectorArgument(selector map[string]string) string {
	keys := make([]string, 0, len(selector))
	for key := range selector {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		parts = append(parts, key+"="+selector[key])
	}
	return strings.Join(parts, ",")
}

func kagentLabelsContain(labels, selector map[string]string) bool {
	for key, value := range selector {
		if labels[key] != value {
			return false
		}
	}
	return true
}

func (a *App) readKagentControllerDeployment(ctx context.Context, service kagentControllerService) (kagentDeployment, error) {
	raw, err := a.kagentCapture(ctx, nil, "-n", service.Namespace, "get", "deployments", "-l", kagentSelectorArgument(service.Selector), "-o", "json")
	if err != nil {
		return kagentDeployment{}, fmt.Errorf("cannot bind the Kagent controller Service to its Deployment: %w", err)
	}
	var list struct {
		Items []kagentDeployment `json:"items"`
	}
	if json.Unmarshal(raw, &list) != nil || len(list.Items) != 1 {
		return kagentDeployment{}, fmt.Errorf("expected exactly one Kagent controller Deployment selected by the controller Service, found %d", len(list.Items))
	}
	deployment := list.Items[0]
	if deployment.Kind != "Deployment" || deployment.Metadata.Name == "" || deployment.Metadata.Name != service.Name || deployment.Metadata.Namespace != service.Namespace ||
		deployment.Metadata.UID == "" || deployment.Metadata.ResourceVersion == "" || deployment.Metadata.Generation < 1 ||
		deployment.Metadata.DeletionTimestamp != nil || !kagentLabelsContain(deployment.Metadata.Labels, service.Selector) ||
		!kagentLabelsContain(deployment.Spec.Template.Metadata.Labels, service.Selector) ||
		len(deployment.Spec.Selector.MatchExpressions) != 0 || !equalKagentLabels(deployment.Spec.Selector.MatchLabels, service.Selector) ||
		deployment.Metadata.Labels["app.kubernetes.io/component"] != "controller" {
		return kagentDeployment{}, fmt.Errorf("the selected Kagent controller Deployment has an invalid identity, selector, or labels")
	}
	if err := validateKagentVersionLabels(deployment.Metadata.Labels); err != nil {
		return kagentDeployment{}, fmt.Errorf("Kagent controller Deployment labels: %w", err)
	}
	return deployment, nil
}

func exactKagentContainer(containers []kagentContainer, name string) (kagentContainer, error) {
	var found []kagentContainer
	for _, container := range containers {
		if container.Name == name {
			found = append(found, container)
		}
	}
	if len(found) != 1 {
		return kagentContainer{}, fmt.Errorf("expected exactly one %s container, found %d", name, len(found))
	}
	return found[0], nil
}

func kagentControllerAuthMode(controller kagentContainer) (string, error) {
	var values []string
	for _, env := range controller.Env {
		if env.Name == "" {
			return "", fmt.Errorf("Kagent controller has an invalid unnamed environment entry")
		}
		if env.Name == "AUTH_MODE" {
			if len(bytes.TrimSpace(env.ValueFrom)) != 0 && string(bytes.TrimSpace(env.ValueFrom)) != "null" {
				return "", fmt.Errorf("Kagent controller AUTH_MODE must be a literal value")
			}
			values = append(values, env.Value)
		}
	}
	if len(values) != 1 || values[0] != "unsecure" && values[0] != "trusted-proxy" {
		return "", fmt.Errorf("Kagent controller AUTH_MODE must be exactly one literal unsecure or trusted-proxy value")
	}
	return values[0], nil
}

func kagentControllerConfigMapName(controller kagentContainer) (string, error) {
	if len(controller.EnvFrom) != 1 {
		return "", fmt.Errorf("Kagent controller must have exactly one ConfigMap envFrom source, found %d sources", len(controller.EnvFrom))
	}
	var names []string
	for _, source := range controller.EnvFrom {
		if len(bytes.TrimSpace(source.SecretRef)) != 0 && string(bytes.TrimSpace(source.SecretRef)) != "null" {
			return "", fmt.Errorf("Kagent controller image envFrom must not include a Secret source")
		}
		if source.ConfigMapRef != nil {
			if source.Prefix != "" || source.ConfigMapRef.Name == "" || source.ConfigMapRef.Optional != nil && *source.ConfigMapRef.Optional {
				return "", fmt.Errorf("Kagent controller image ConfigMap envFrom reference is not exact")
			}
			names = append(names, source.ConfigMapRef.Name)
		}
	}
	if len(names) != 1 {
		return "", fmt.Errorf("Kagent controller must have exactly one ConfigMap envFrom source, found %d", len(names))
	}
	for _, env := range controller.Env {
		if strings.HasPrefix(env.Name, "IMAGE_") || strings.HasPrefix(env.Name, "GO_IMAGE_") {
			return "", fmt.Errorf("Kagent controller image config must come only from its ConfigMap envFrom source")
		}
	}
	return names[0], nil
}

type kagentConfigMap struct {
	Kind     string            `json:"kind"`
	Metadata kagentMetadata    `json:"metadata"`
	Data     map[string]string `json:"data"`
}

func (a *App) readKagentControllerConfigMap(ctx context.Context, namespace, name, runtimeName string) (kagentConfigMap, error) {
	raw, err := a.kagentCapture(ctx, nil, "-n", namespace, "get", "configmap", name, "-o", "json")
	if err != nil {
		return kagentConfigMap{}, fmt.Errorf("cannot read Kagent controller image ConfigMap: %w", err)
	}
	var configMap kagentConfigMap
	if json.Unmarshal(raw, &configMap) != nil || configMap.Kind != "ConfigMap" || configMap.Metadata.Name != name ||
		configMap.Metadata.Namespace != namespace || configMap.Metadata.UID == "" || configMap.Metadata.ResourceVersion == "" ||
		configMap.Metadata.DeletionTimestamp != nil {
		return kagentConfigMap{}, fmt.Errorf("Kagent controller image ConfigMap returned an invalid identity")
	}
	if err := validateKagentVersionLabels(configMap.Metadata.Labels); err != nil {
		return kagentConfigMap{}, fmt.Errorf("Kagent controller image ConfigMap labels: %w", err)
	}
	prefix, repository := "IMAGE_", "kagent-dev/kagent/app"
	if runtimeName == "go" {
		prefix, repository = "GO_IMAGE_", "kagent-dev/kagent/golang-adk"
	} else if runtimeName != "python" {
		return kagentConfigMap{}, fmt.Errorf("unsupported Kagent declarative runtime")
	}
	if configMap.Data[prefix+"REGISTRY"] != "ghcr.io" || configMap.Data[prefix+"REPOSITORY"] != repository || configMap.Data[prefix+"TAG"] != "0.10.2" {
		return kagentConfigMap{}, fmt.Errorf("Kagent controller %s image config does not exactly select ghcr.io/%s:0.10.2", runtimeName, repository)
	}
	return configMap, nil
}

func (a *App) verifyKagentDeploymentPods(ctx context.Context, namespace string, deployment kagentDeployment, containerName, image, imageRepo string, digests []string) error {
	desired := int32(1)
	if deployment.Spec.Replicas != nil {
		desired = *deployment.Spec.Replicas
	}
	if desired < 1 || deployment.Status.ObservedGeneration != deployment.Metadata.Generation ||
		deployment.Status.Replicas != desired || deployment.Status.UpdatedReplicas != desired || deployment.Status.ReadyReplicas != desired ||
		deployment.Status.AvailableReplicas != desired || deployment.Status.UnavailableReplicas != 0 {
		return fmt.Errorf("Deployment is not a complete current-generation rollout")
	}
	selector := deployment.Spec.Selector.MatchLabels
	if len(selector) == 0 || len(deployment.Spec.Selector.MatchExpressions) != 0 || !kagentLabelsContain(deployment.Spec.Template.Metadata.Labels, selector) {
		return fmt.Errorf("Deployment has no exact matchLabels pod selector")
	}
	raw, err := a.kagentCapture(ctx, nil, "-n", namespace, "get", "pods", "-l", kagentSelectorArgument(selector), "-o", "json")
	if err != nil {
		return fmt.Errorf("cannot read Deployment pods: %w", err)
	}
	var list struct {
		Items []struct {
			Metadata kagentMetadata `json:"metadata"`
			Spec     struct {
				NodeName   string            `json:"nodeName"`
				Containers []kagentContainer `json:"containers"`
			} `json:"spec"`
			Status struct {
				Phase      string `json:"phase"`
				Conditions []struct {
					Type   string `json:"type"`
					Status string `json:"status"`
				} `json:"conditions"`
				ContainerStatuses []struct {
					Name    string `json:"name"`
					Image   string `json:"image"`
					ImageID string `json:"imageID"`
					Ready   bool   `json:"ready"`
				} `json:"containerStatuses"`
			} `json:"status"`
		} `json:"items"`
	}
	if json.Unmarshal(raw, &list) != nil {
		return fmt.Errorf("Deployment pod list returned invalid JSON")
	}
	readyPods := int32(0)
	for _, pod := range list.Items {
		if pod.Metadata.Name == "" || pod.Metadata.UID == "" || pod.Metadata.ResourceVersion == "" ||
			pod.Metadata.Namespace != namespace || pod.Metadata.DeletionTimestamp != nil || pod.Spec.NodeName == "" ||
			!kagentLabelsContain(pod.Metadata.Labels, selector) || pod.Status.Phase != "Running" {
			return fmt.Errorf("Deployment selected an invalid or non-running pod")
		}
		podReady := false
		for _, condition := range pod.Status.Conditions {
			if condition.Type == "Ready" && condition.Status == "True" {
				podReady = true
			}
		}
		container, err := exactKagentContainer(pod.Spec.Containers, containerName)
		if err != nil || container.Image != image {
			return fmt.Errorf("pod does not have exactly one %s container using %s", containerName, image)
		}
		var statuses []struct {
			Name    string `json:"name"`
			Image   string `json:"image"`
			ImageID string `json:"imageID"`
			Ready   bool   `json:"ready"`
		}
		for _, status := range pod.Status.ContainerStatuses {
			if status.Name == containerName {
				statuses = append(statuses, status)
			}
		}
		if !podReady || len(statuses) != 1 || !statuses[0].Ready || statuses[0].Image != image ||
			!validKagentImageID(statuses[0].ImageID, imageRepo, digests) {
			return fmt.Errorf("ready pod %s container image identity does not match the published v0.10.2 digest", containerName)
		}
		readyPods++
	}
	if readyPods != desired {
		return fmt.Errorf("Deployment has %d proven ready pods, want %d", readyPods, desired)
	}
	return nil
}

func validKagentImageID(imageID, repository string, digests []string) bool {
	if _, suffix, ok := strings.Cut(imageID, "://"); ok {
		imageID = suffix
	}
	if strings.HasPrefix(imageID, repository+"@") {
		imageID = strings.TrimPrefix(imageID, repository+"@")
	} else if strings.HasPrefix(imageID, "ghcr.io/"+repository+"@") {
		imageID = strings.TrimPrefix(imageID, "ghcr.io/"+repository+"@")
	} else if !strings.HasPrefix(imageID, "sha256:") {
		return false
	}
	parts := strings.Split(imageID, "@")
	if len(parts) == 0 {
		return false
	}
	for _, part := range parts {
		if !strings.HasPrefix(part, "sha256:") || !slices.Contains(digests, strings.TrimPrefix(part, "sha256:")) {
			return false
		}
	}
	return true
}

func validPinnedKagentVersion(version, commit string) bool {
	commit = strings.ToLower(commit)
	if len(commit) < 7 || len(commit) > len(kagentPinnedCommit) {
		return false
	}
	for _, ch := range commit {
		if ch < '0' || ch > '9' && ch < 'a' || ch > 'f' {
			return false
		}
	}
	return (version == "0.10.2" || version == scaffold.KagentVersion) && strings.HasPrefix(kagentPinnedCommit, commit)
}

type kagentCRDIdentity struct{ Name, Kind, Plural, SchemaSHA256 string }

func validateKagentCRD(raw []byte, want kagentCRDIdentity) error {
	var crd struct {
		Metadata struct {
			Name string `json:"name"`
		} `json:"metadata"`
		Spec struct {
			Group string `json:"group"`
			Scope string `json:"scope"`
			Names struct {
				Kind, Plural string
			} `json:"names"`
			Versions []struct {
				Name            string `json:"name"`
				Served, Storage bool
				Schema          struct {
					OpenAPIV3Schema json.RawMessage `json:"openAPIV3Schema"`
				} `json:"schema"`
			} `json:"versions"`
		} `json:"spec"`
	}
	if err := json.Unmarshal(raw, &crd); err != nil {
		return fmt.Errorf("invalid JSON")
	}
	if crd.Metadata.Name != want.Name || crd.Spec.Group != "kagent.dev" || crd.Spec.Scope != "Namespaced" ||
		crd.Spec.Names.Kind != want.Kind || crd.Spec.Names.Plural != want.Plural {
		return fmt.Errorf("group, kind, plural, scope, or metadata name differs")
	}
	var selected *json.RawMessage
	for i := range crd.Spec.Versions {
		version := &crd.Spec.Versions[i]
		if version.Name == "v1alpha2" {
			if selected != nil {
				return fmt.Errorf("v1alpha2 appears more than once")
			}
			if !version.Served || !version.Storage {
				return fmt.Errorf("v1alpha2 is not both served and storage")
			}
			selected = &version.Schema.OpenAPIV3Schema
			continue
		}
		if version.Storage {
			return fmt.Errorf("a version other than v1alpha2 is marked storage")
		}
	}
	if selected == nil {
		return fmt.Errorf("v1alpha2 is not both served and storage")
	}
	if len(*selected) == 0 {
		return fmt.Errorf("v1alpha2 has no openAPIV3Schema")
	}
	var schema any
	decoder := json.NewDecoder(bytes.NewReader(*selected))
	decoder.UseNumber()
	if decoder.Decode(&schema) != nil {
		return fmt.Errorf("v1alpha2 openAPIV3Schema is invalid JSON")
	}
	canonical, err := json.Marshal(schema)
	if err != nil {
		return fmt.Errorf("v1alpha2 openAPIV3Schema cannot be canonicalized")
	}
	digest := fmt.Sprintf("%x", sha256.Sum256(canonical))
	if digest != want.SchemaSHA256 {
		return fmt.Errorf("v1alpha2 openAPIV3Schema SHA-256 differs from the published v0.10.2 schema")
	}
	return nil
}

func kagentServiceProxyPath(service kagentControllerService, suffix string) string {
	return "/api/v1/namespaces/" + url.PathEscape(service.Namespace) + "/services/http:" +
		url.PathEscape(service.Name) + ":8083/proxy" + suffix
}

func kagentPlural(kind string) string {
	switch kind {
	case "Secret":
		return "secrets"
	case "ServiceAccount":
		return "serviceaccounts"
	case "Deployment":
		return "deployments.apps"
	case "Service":
		return "services"
	case "ModelConfig":
		return "modelconfigs.kagent.dev"
	case "Agent":
		return "agents.kagent.dev"
	default:
		return strings.ToLower(kind) + "s.kagent.dev"
	}
}

func (a *App) kagentGeneratedChildrenAbsent(ctx context.Context, namespace, name string) error {
	for _, kind := range []string{"Secret", "ServiceAccount", "Deployment", "Service"} {
		if err := a.kagentNamedResourceAbsent(ctx, namespace, kind, name); err != nil {
			return err
		}
	}
	return nil
}

func (a *App) kagentNamedResourceAbsent(ctx context.Context, namespace, kind, name string) error {
	template := "go-template={{if .metadata.uid}}present{{else}}invalid{{end}}"
	raw, err := a.kagentCapture(ctx, nil, "-n", namespace, "get", kagentPlural(kind), name, "--ignore-not-found=true", "-o", template)
	if err != nil {
		return fmt.Errorf("cannot establish %s/%s absence; no Kubernetes resources were changed: %w", kind, name, err)
	}
	switch string(bytes.TrimSpace(raw)) {
	case "":
		return nil
	case "present":
		return fmt.Errorf("%s/%s already exists; Kagent Agent creation would overwrite this same-name generated child, so create is refused", kind, name)
	default:
		return fmt.Errorf("cannot establish %s/%s absence because its sanitized read returned an invalid marker; no Kubernetes resources were changed", kind, name)
	}
}

func (a *App) kagentAbsent(ctx context.Context, namespace string, doc map[string]any) error {
	kind, name := doc["kind"].(string), kagentObjectName(doc)
	template := "go-template={{if and .metadata.uid .metadata.generation}}present{{else}}invalid{{end}}"
	raw, err := a.kagentCapture(ctx, nil, "-n", namespace, "get", kagentPlural(kind), name, "--ignore-not-found=true", "-o", template)
	if err != nil {
		return fmt.Errorf("cannot establish %s/%s absence; no Kubernetes resources were changed: %w", kind, name, err)
	}
	switch string(bytes.TrimSpace(raw)) {
	case "":
		return nil
	case "present":
		return fmt.Errorf("%s/%s already exists; Kagent create is create-only and refuses overwrite, adoption, or reconciliation", kind, name)
	default:
		return fmt.Errorf("cannot establish %s/%s absence because its sanitized read returned an invalid marker; no Kubernetes resources were changed", kind, name)
	}
}

func (a *App) kagentSecretPresent(ctx context.Context, namespace, secret, key string) error {
	if secret == "" || key == "" {
		return fmt.Errorf("Kagent model Secret reference is incomplete; no Kubernetes resources were changed")
	}
	template := "go-template=secret\n{{range $key, $_ := .data}}{{if eq $key " + fmt.Sprintf("%q", key) + "}}present{{end}}{{end}}"
	marker, err := a.kagentCapture(ctx, nil, "-n", namespace, "get", "secret", secret, "--ignore-not-found=true", "-o", template)
	if err != nil {
		return fmt.Errorf("cannot read Kagent model Secret key presence; no Kubernetes resources were changed: %w", err)
	}
	switch string(marker) {
	case "":
		return fmt.Errorf("Kagent model Secret %s/%s is missing; provision it separately, never create the skeleton", namespace, secret)
	case "secret\n":
		return fmt.Errorf("Kagent model Secret exists but its referenced key is missing; provision that key separately")
	case "secret\npresent":
		return nil
	default:
		return fmt.Errorf("Kagent model Secret key check returned an invalid presence marker")
	}
}

type kagentToolSnapshot struct {
	Name, Namespace, UID, SpecDigest, SecretHash string
	Generation                                   int64
	DiscoveredTools                              []string
}

func (a *App) snapshotKagentTools(ctx context.Context, bundle *scaffold.KagentBundle) (map[string]kagentToolSnapshot, error) {
	snapshots := map[string]kagentToolSnapshot{}
	declarative := bundle.Agent["spec"].(map[string]any)["declarative"].(map[string]any)
	rawTools, ok := declarative["tools"].([]any)
	if !ok || len(rawTools) == 0 {
		return snapshots, nil
	}
	for _, rawTool := range rawTools {
		server := rawTool.(map[string]any)["mcpServer"].(map[string]any)
		serverName := server["name"].(string)
		names := server["toolNames"].([]any)
		snapshot, err := a.readKagentToolSnapshot(ctx, kagentObjectNamespace(bundle.Agent), serverName)
		if err != nil {
			return nil, fmt.Errorf("%w; no Kubernetes resources were changed", err)
		}
		var missing []string
		for _, rawName := range names {
			name := rawName.(string)
			if !slices.Contains(snapshot.DiscoveredTools, name) {
				missing = append(missing, name)
			}
		}
		if len(missing) > 0 {
			return nil, fmt.Errorf("RemoteMCPServer %s has not discovered every allowlisted tool (%s); no Kubernetes resources were changed", serverName, strings.Join(missing, ", "))
		}
		snapshots[serverName] = snapshot
	}
	return snapshots, nil
}

func (a *App) verifyKagentToolSnapshots(ctx context.Context, bundle *scaffold.KagentBundle, snapshots map[string]kagentToolSnapshot) error {
	for name, expected := range snapshots {
		current, err := a.readKagentToolSnapshot(ctx, kagentObjectNamespace(bundle.Agent), name)
		if err != nil {
			return err
		}
		if !reflect.DeepEqual(current, expected) {
			return fmt.Errorf("RemoteMCPServer %s was replaced or its generation, spec, Secret hash, or discovered tool set changed", name)
		}
	}
	return nil
}

func (a *App) readKagentToolSnapshot(ctx context.Context, namespace, name string) (kagentToolSnapshot, error) {
	snapshot := kagentToolSnapshot{Name: name, Namespace: namespace}
	raw, err := a.kagentCapture(ctx, nil, "-n", namespace, "get", "remotemcpservers.kagent.dev", name, "-o", "json")
	if err != nil {
		return snapshot, fmt.Errorf("cannot read Kagent RemoteMCPServer %s: %w", name, err)
	}
	var object struct {
		APIVersion string          `json:"apiVersion"`
		Kind       string          `json:"kind"`
		Metadata   kagentMetadata  `json:"metadata"`
		Spec       json.RawMessage `json:"spec"`
		Status     struct {
			ObservedGeneration int64             `json:"observedGeneration"`
			Conditions         []serverCondition `json:"conditions"`
			SecretHash         string            `json:"secretHash"`
			DiscoveredTools    []struct {
				Name string `json:"name"`
			} `json:"discoveredTools"`
		} `json:"status"`
	}
	if err := json.Unmarshal(raw, &object); err != nil || object.APIVersion != scaffold.KagentAPIVersion ||
		object.Kind != scaffold.KagentRemoteMCPServerKind || object.Metadata.Name != name || object.Metadata.Namespace != namespace ||
		object.Metadata.UID == "" || object.Metadata.Generation < 1 || object.Metadata.DeletionTimestamp != nil || len(object.Spec) == 0 {
		return snapshot, fmt.Errorf("RemoteMCPServer %s returned an invalid or terminating identity", name)
	}
	if object.Status.ObservedGeneration != object.Metadata.Generation || !kagentConditionTrue(object.Status.Conditions, "Accepted", object.Metadata.Generation) {
		return snapshot, fmt.Errorf("RemoteMCPServer %s is not current-generation Accepted", name)
	}
	snapshot.UID = object.Metadata.UID
	snapshot.Generation = object.Metadata.Generation
	snapshot.SecretHash = object.Status.SecretHash
	value, err := canonicalKagentJSONValue(object.Spec)
	if err != nil {
		return snapshot, fmt.Errorf("RemoteMCPServer %s returned an invalid spec", name)
	}
	canonical, err := json.Marshal(value)
	if err != nil {
		return snapshot, fmt.Errorf("RemoteMCPServer %s spec cannot be canonicalized", name)
	}
	snapshot.SpecDigest = fmt.Sprintf("%x", sha256.Sum256(canonical))
	seen := map[string]bool{}
	for _, tool := range object.Status.DiscoveredTools {
		if scaffold.ValidateKagentToolName(tool.Name) != nil || seen[tool.Name] {
			return snapshot, fmt.Errorf("RemoteMCPServer %s returned an invalid discovered tool set", name)
		}
		seen[tool.Name] = true
		snapshot.DiscoveredTools = append(snapshot.DiscoveredTools, tool.Name)
	}
	sort.Strings(snapshot.DiscoveredTools)
	return snapshot, nil
}

type kagentIdentity struct {
	Kind, Name, UID string
	Generation      int64
	Proof           kagentObjectProof
}

type kagentObjectProof struct {
	APIVersion, Kind, Name, Namespace string
	Spec                              any
	Markers                           map[string]string
}

type kagentObject struct {
	APIVersion string          `json:"apiVersion"`
	Kind       string          `json:"kind"`
	Metadata   kagentMetadata  `json:"metadata"`
	Spec       json.RawMessage `json:"spec"`
	Status     struct {
		ObservedGeneration int64             `json:"observedGeneration"`
		Conditions         []serverCondition `json:"conditions"`
		SecretHash         string            `json:"secretHash"`
	} `json:"status"`
}

func validateKagentAdmission(raw []byte, desired map[string]any, namespace string) (kagentObjectProof, error) {
	proof, err := expectedKagentObjectProof(desired, namespace)
	if err != nil {
		return proof, err
	}
	var admitted struct {
		APIVersion string `json:"apiVersion"`
		Kind       string `json:"kind"`
		Metadata   struct {
			Name, Namespace, GenerateName string
			Annotations                   map[string]string `json:"annotations"`
			Labels                        map[string]string `json:"labels"`
			Finalizers                    []string          `json:"finalizers"`
			OwnerReferences               []json.RawMessage `json:"ownerReferences"`
			DeletionTimestamp             *time.Time        `json:"deletionTimestamp"`
		} `json:"metadata"`
		Spec json.RawMessage `json:"spec"`
	}
	if err := json.Unmarshal(raw, &admitted); err != nil {
		return proof, fmt.Errorf("server returned invalid JSON")
	}
	metadata := desired["metadata"].(map[string]any)
	wantAnnotations, err := kagentStringMap(metadata["annotations"])
	if err != nil {
		return proof, err
	}
	wantLabels, err := kagentStringMap(metadata["labels"])
	if err != nil {
		return proof, err
	}
	if admitted.APIVersion != proof.APIVersion || admitted.Kind != proof.Kind || admitted.Metadata.Name != proof.Name ||
		admitted.Metadata.Namespace != proof.Namespace || admitted.Metadata.GenerateName != "" || admitted.Metadata.DeletionTimestamp != nil ||
		len(admitted.Metadata.Finalizers) != 0 || len(admitted.Metadata.OwnerReferences) != 0 ||
		!reflect.DeepEqual(admitted.Metadata.Annotations, wantAnnotations) || !reflect.DeepEqual(admitted.Metadata.Labels, wantLabels) {
		return proof, fmt.Errorf("server returned a different identity or mutated reviewed metadata")
	}
	if err := validateKagentProofSpecAndMarkers(admitted.Spec, admitted.Metadata.Annotations, proof); err != nil {
		return proof, err
	}
	return proof, nil
}

func expectedKagentObjectProof(desired map[string]any, namespace string) (kagentObjectProof, error) {
	proof := kagentObjectProof{
		APIVersion: fmt.Sprint(desired["apiVersion"]), Kind: fmt.Sprint(desired["kind"]),
		Name: kagentObjectName(desired), Namespace: namespace,
	}
	metadata, ok := desired["metadata"].(map[string]any)
	if !ok || metadata["namespace"] != namespace || proof.APIVersion != scaffold.KagentAPIVersion ||
		(proof.Kind != "ModelConfig" && proof.Kind != "Agent") || proof.Name == "" {
		return proof, fmt.Errorf("reviewed object has an invalid identity")
	}
	annotations, err := kagentStringMap(metadata["annotations"])
	if err != nil {
		return proof, err
	}
	proof.Markers = map[string]string{}
	for _, key := range []string{orkaBundleMarker, orkaPortableMarker, orkaRenderedMarker} {
		value := annotations[key]
		if value == "" || key != orkaBundleMarker && !validOrkaMarkerDigest(value) {
			return proof, fmt.Errorf("reviewed object lacks an exact KMX marker annotation")
		}
		proof.Markers[key] = value
	}
	spec, ok := desired["spec"].(map[string]any)
	if !ok {
		return proof, fmt.Errorf("reviewed object has no spec")
	}
	normalized, err := cloneOrkaDoc(spec)
	if err != nil {
		return proof, err
	}
	if proof.Kind == "ModelConfig" && normalized["provider"] == "OpenAI" {
		openAI, ok := normalized["openAI"].(map[string]any)
		if !ok {
			return proof, fmt.Errorf("reviewed OpenAI ModelConfig has no openAI configuration")
		}
		if _, present := openAI["apiFormat"]; present {
			return proof, fmt.Errorf("reviewed OpenAI ModelConfig unexpectedly sets apiFormat")
		}
		openAI["apiFormat"] = "chatCompletions"
	}
	proof.Spec, err = canonicalKagentJSONValue(normalized)
	return proof, err
}

func kagentStringMap(value any) (map[string]string, error) {
	if value == nil {
		return nil, nil
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	var result map[string]string
	if err := json.Unmarshal(raw, &result); err != nil {
		return nil, fmt.Errorf("reviewed metadata contains a non-string map")
	}
	return result, nil
}

func canonicalKagentJSONValue(value any) (any, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var result any
	if err := decoder.Decode(&result); err != nil || requireKagentJSONEOF(decoder) != nil {
		return nil, fmt.Errorf("invalid JSON value")
	}
	return result, nil
}

func validateKagentProofSpecAndMarkers(rawSpec json.RawMessage, annotations map[string]string, proof kagentObjectProof) error {
	actual, err := canonicalKagentJSONValue(json.RawMessage(rawSpec))
	if err != nil || !reflect.DeepEqual(actual, proof.Spec) {
		return fmt.Errorf("server-mutated spec differs from exact v0.10.2 normalization")
	}
	for key, value := range proof.Markers {
		if annotations[key] != value {
			return fmt.Errorf("KMX marker annotations changed")
		}
	}
	return nil
}

func (a *App) verifyKagentModelState(ctx context.Context, namespace string, id kagentIdentity, expectedSecretHash string) (string, error) {
	if id.Kind != "ModelConfig" {
		return "", fmt.Errorf("expected a ModelConfig identity")
	}
	object, err := a.readKagentObject(ctx, namespace, id)
	if err != nil {
		return "", err
	}
	if object.Status.ObservedGeneration != id.Generation || !kagentObjectConditionTrue(object.Status.Conditions, "Accepted", id) {
		return "", fmt.Errorf("ModelConfig/%s UID %s is no longer current-generation Accepted", id.Name, id.UID)
	}
	if object.Status.SecretHash == "" {
		return "", fmt.Errorf("ModelConfig/%s reports no Secret hash", id.Name)
	}
	if expectedSecretHash != "" && object.Status.SecretHash != expectedSecretHash {
		return "", fmt.Errorf("ModelConfig/%s Secret hash changed", id.Name)
	}
	return object.Status.SecretHash, nil
}

func (a *App) createKagentObject(ctx context.Context, namespace string, doc map[string]any, proof kagentObjectProof) (kagentIdentity, error) {
	id := kagentIdentity{Kind: doc["kind"].(string), Name: kagentObjectName(doc), Proof: proof}
	body, err := json.Marshal(doc)
	if err != nil {
		return id, err
	}
	raw, err := a.kagentCapture(ctx, body, "-n", namespace, "create", "--validate=strict", "-f", "-", "-o", "json")
	if err != nil {
		return id, fmt.Errorf("create %s/%s failed or was ambiguous; it may exist and was not retried, adopted, or deleted: %w", id.Kind, id.Name, err)
	}
	var object kagentObject
	if err := json.Unmarshal(raw, &object); err != nil || object.Kind != id.Kind || object.Metadata.Name != id.Name ||
		object.APIVersion != proof.APIVersion || object.Metadata.Namespace != namespace || object.Metadata.UID == "" ||
		object.Metadata.Generation < 1 || object.Metadata.DeletionTimestamp != nil {
		return id, fmt.Errorf("create %s/%s returned no valid identity; it may exist and was not retried", id.Kind, id.Name)
	}
	if err := validateKagentProofSpecAndMarkers(object.Spec, object.Metadata.Annotations, proof); err != nil {
		return id, fmt.Errorf("create %s/%s returned a mutated admitted object; it may exist and no task or receipt was attempted: %w", id.Kind, id.Name, err)
	}
	id.UID, id.Generation = object.Metadata.UID, object.Metadata.Generation
	return id, nil
}

func (a *App) readKagentObject(ctx context.Context, namespace string, id kagentIdentity) (*kagentObject, error) {
	raw, err := a.kagentCapture(ctx, nil, "-n", namespace, "get", kagentPlural(id.Kind), id.Name, "-o", "json")
	if err != nil {
		return nil, fmt.Errorf("cannot read %s/%s UID %s; it may have disappeared: %w", id.Kind, id.Name, id.UID, err)
	}
	var object kagentObject
	if err := json.Unmarshal(raw, &object); err != nil {
		return nil, fmt.Errorf("invalid %s/%s status response", id.Kind, id.Name)
	}
	if object.Kind != id.Kind || object.Metadata.Name != id.Name || object.Metadata.Namespace != namespace ||
		object.APIVersion != id.Proof.APIVersion || object.Metadata.UID != id.UID || object.Metadata.Generation != id.Generation {
		return nil, fmt.Errorf("%s/%s UID %s was replaced or its spec generation changed; refusing stale state", id.Kind, id.Name, id.UID)
	}
	if object.Metadata.DeletionTimestamp != nil {
		return nil, fmt.Errorf("%s/%s UID %s is terminating; refusing stale state", id.Kind, id.Name, id.UID)
	}
	if err := validateKagentProofSpecAndMarkers(object.Spec, object.Metadata.Annotations, id.Proof); err != nil {
		return nil, fmt.Errorf("%s/%s UID %s no longer retains its admitted spec and KMX markers: %w", id.Kind, id.Name, id.UID, err)
	}
	return &object, nil
}

func (a *App) waitKagentReady(ctx context.Context, namespace string, id kagentIdentity) error {
	for {
		object, err := a.readKagentObject(ctx, namespace, id)
		if err != nil {
			return err
		}
		accepted := object.Status.ObservedGeneration == id.Generation && kagentObjectConditionTrue(object.Status.Conditions, "Accepted", id)
		ready := id.Kind != "Agent" || object.Status.ObservedGeneration == id.Generation && kagentReadyConditionTrue(object.Status.Conditions, id.Generation)
		if accepted && ready {
			return nil
		}
		for _, condition := range object.Status.Conditions {
			generationMatches := condition.ObservedGeneration == id.Generation || id.Kind == "ModelConfig" && condition.ObservedGeneration == 0
			if generationMatches && condition.Status == "False" && condition.Type == "Accepted" {
				return fmt.Errorf("%s/%s UID %s was refused at its current generation: %s=False", id.Kind, id.Name, id.UID, condition.Type)
			}
		}
		if err := a.pause(ctx, time.Second); err != nil {
			return fmt.Errorf("waiting for %s/%s UID %s current-generation readiness: %w", id.Kind, id.Name, id.UID, err)
		}
	}
}

func kagentControllerOwner(metadata kagentMetadata, id kagentIdentity) bool {
	controllers := 0
	matched := false
	for _, owner := range metadata.OwnerReferences {
		if owner.Controller != nil && *owner.Controller {
			controllers++
			matched = owner.APIVersion == scaffold.KagentAPIVersion && owner.Kind == "Agent" && owner.Name == id.Name && owner.UID == id.UID
		}
	}
	return controllers == 1 && matched
}

func kagentRuntimeImage(runtimeName string) (string, string, []string, error) {
	switch runtimeName {
	case "go":
		return kagentGoImage, kagentGoImageRepo, []string{kagentGoIndexDigest, kagentGoAMD64Digest, kagentGoARM64Digest}, nil
	case "python":
		return kagentPythonImage, kagentPythonImageRepo, []string{kagentPythonIndexDigest, kagentPythonAMD64Digest, kagentPythonARM64Digest}, nil
	default:
		return "", "", nil, fmt.Errorf("unsupported Kagent declarative runtime")
	}
}

func (a *App) verifyKagentAgentWorkload(ctx context.Context, namespace string, id kagentIdentity, runtimeName string, previous *kagentWorkloadIdentity) (kagentWorkloadIdentity, error) {
	identity := kagentWorkloadIdentity{}
	object, err := a.readKagentObject(ctx, namespace, id)
	if err != nil {
		return identity, err
	}
	if object.Status.ObservedGeneration != id.Generation || !kagentObjectConditionTrue(object.Status.Conditions, "Accepted", id) ||
		!kagentReadyConditionTrue(object.Status.Conditions, id.Generation) {
		return identity, fmt.Errorf("Agent/%s UID %s is no longer current-generation Accepted and Ready", id.Name, id.UID)
	}
	raw, err := a.kagentCapture(ctx, nil, "-n", namespace, "get", "deployment", id.Name, "-o", "json")
	if err != nil {
		return identity, fmt.Errorf("cannot read generated Kagent Agent Deployment: %w", err)
	}
	var deployment kagentDeployment
	if json.Unmarshal(raw, &deployment) != nil || deployment.Kind != "Deployment" || deployment.Metadata.Name != id.Name ||
		deployment.Metadata.Namespace != namespace || deployment.Metadata.UID == "" || deployment.Metadata.ResourceVersion == "" ||
		deployment.Metadata.Generation < 1 || deployment.Metadata.DeletionTimestamp != nil || !kagentControllerOwner(deployment.Metadata, id) {
		return identity, fmt.Errorf("generated Kagent Agent Deployment has an invalid identity or is not controller-owned by Agent UID %s", id.UID)
	}
	image, repository, digests, err := kagentRuntimeImage(runtimeName)
	if err != nil {
		return identity, err
	}
	container, err := exactKagentContainer(deployment.Spec.Template.Spec.Containers, "kagent")
	if err != nil || container.Image != image {
		return identity, fmt.Errorf("generated Kagent Agent Deployment does not use exactly one kagent container with image %s", image)
	}
	if err := a.verifyKagentDeploymentPods(ctx, namespace, deployment, "kagent", image, repository, digests); err != nil {
		return identity, fmt.Errorf("generated Kagent Agent Deployment is not a proven current rollout: %w", err)
	}
	raw, err = a.kagentCapture(ctx, nil, "-n", namespace, "get", "service", id.Name, "-o", "json")
	if err != nil {
		return identity, fmt.Errorf("cannot read generated Kagent Agent Service: %w", err)
	}
	var service struct {
		Kind     string         `json:"kind"`
		Metadata kagentMetadata `json:"metadata"`
		Spec     struct {
			Selector map[string]string `json:"selector"`
			Ports    []struct {
				Name, Protocol string
				Port           int32 `json:"port"`
			} `json:"ports"`
		} `json:"spec"`
	}
	if json.Unmarshal(raw, &service) != nil || service.Kind != "Service" || service.Metadata.Name != id.Name ||
		service.Metadata.Namespace != namespace || service.Metadata.UID == "" || service.Metadata.ResourceVersion == "" ||
		service.Metadata.DeletionTimestamp != nil || !kagentControllerOwner(service.Metadata, id) ||
		len(service.Spec.Selector) == 0 || !equalKagentLabels(service.Spec.Selector, deployment.Spec.Selector.MatchLabels) ||
		!slices.ContainsFunc(service.Spec.Ports, func(port struct {
			Name, Protocol string
			Port           int32 `json:"port"`
		}) bool {
			return port.Protocol == "TCP" && port.Port == 8080
		}) {
		return identity, fmt.Errorf("generated Kagent Agent Service has an invalid identity, owner, selector, or port 8080")
	}
	identity = kagentWorkloadIdentity{
		DeploymentUID: deployment.Metadata.UID, DeploymentResourceVersion: deployment.Metadata.ResourceVersion,
		DeploymentGeneration: deployment.Metadata.Generation,
		ServiceUID:           service.Metadata.UID, ServiceResourceVersion: service.Metadata.ResourceVersion,
	}
	if previous != nil && identity != *previous {
		return identity, fmt.Errorf("generated Kagent Agent Deployment or Service was replaced or changed")
	}
	return identity, nil
}

func (a *App) waitKagentAgentCard(ctx context.Context, service kagentControllerService, namespace, agent string) error {
	for {
		if err := a.verifyKagentAgentCard(ctx, service, namespace, agent); err == nil {
			return nil
		}
		if err := a.pause(ctx, kagentPollInterval); err != nil {
			return fmt.Errorf("waiting for the controller-proxied A2A agent card for Agent/%s: %w", agent, err)
		}
	}
}

func (a *App) verifyKagentAgentCard(ctx context.Context, service kagentControllerService, namespace, agent string) error {
	path := kagentServiceProxyPath(service, "/api/a2a/"+url.PathEscape(namespace)+"/"+url.PathEscape(agent)+"/.well-known/agent-card.json")
	raw, err := a.kagentCapture(ctx, nil, "get", "--raw", path)
	if err != nil {
		return err
	}
	var card struct {
		Name               string   `json:"name"`
		DefaultInputModes  []string `json:"defaultInputModes"`
		DefaultOutputModes []string `json:"defaultOutputModes"`
		Capabilities       *struct {
			Streaming bool `json:"streaming"`
		} `json:"capabilities"`
	}
	if json.Unmarshal(raw, &card) != nil || card.Name != strings.ReplaceAll(agent, "-", "_") ||
		card.Capabilities == nil || !slices.Contains(card.DefaultInputModes, "text") || !slices.Contains(card.DefaultOutputModes, "text") {
		return fmt.Errorf("controller-proxied A2A agent card is invalid or did not name the expected Agent")
	}
	return nil
}

// v0.10.2 stamps Agent conditions individually. Its ModelConfig controller
// instead stamps status.observedGeneration and writes Accepted in the same
// status update without condition.observedGeneration, so zero is valid only
// for that kind and only after the top-level generation check above succeeds.
func kagentObjectConditionTrue(conditions []serverCondition, conditionType string, id kagentIdentity) bool {
	for _, condition := range conditions {
		generationMatches := condition.ObservedGeneration == id.Generation || id.Kind == "ModelConfig" && condition.ObservedGeneration == 0
		if condition.Type == conditionType && condition.Status == "True" && generationMatches {
			return true
		}
	}
	return false
}

func kagentConditionTrue(conditions []serverCondition, conditionType string, generation int64) bool {
	for _, condition := range conditions {
		if condition.Type == conditionType && condition.Status == "True" && condition.ObservedGeneration == generation {
			return true
		}
	}
	return false
}

func kagentReadyConditionTrue(conditions []serverCondition, generation int64) bool {
	for _, condition := range conditions {
		if condition.Type == "Ready" && condition.Status == "True" &&
			condition.ObservedGeneration == generation && condition.Reason == "DeploymentReady" {
			return true
		}
	}
	return false
}

func (a *App) sendKagentTask(ctx context.Context, service kagentControllerService, namespace, agent, prompt string) (string, error) {
	messageID, err := randomHex(16)
	if err != nil {
		return "", err
	}
	rpcID, err := randomHex(16)
	if err != nil {
		return "", err
	}
	body, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": rpcID, "method": "message/send",
		"params": map[string]any{"message": map[string]any{
			"kind": "message", "messageId": messageID, "role": "user",
			"parts": []any{map[string]any{"kind": "text", "text": prompt}},
		}},
	})
	if err != nil {
		return "", fmt.Errorf("cannot encode the Kagent A2A request")
	}
	path := kagentServiceProxyPath(service, "/api/a2a/"+url.PathEscape(namespace)+"/"+url.PathEscape(agent))
	raw, err := a.kagentCaptureWithTimeout(ctx, 5*time.Minute, "5m", body, "create", "--raw", path, "-f", "-")
	if err != nil {
		return "", fmt.Errorf("Kagent A2A message/send failed or was ambiguous; it may have executed and was not retried: %w", err)
	}
	answer, err := completedKagentAnswer(raw, rpcID)
	if err != nil {
		return "", fmt.Errorf("Kagent A2A message/send was accepted but no completed answer could be proven; it may have executed and was not retried: %w", err)
	}
	return answer, nil
}

func completedKagentAnswer(raw []byte, rpcID string) (string, error) {
	var response struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      json.RawMessage `json:"id"`
		Result  json.RawMessage `json:"result"`
		Error   json.RawMessage `json:"error"`
	}
	if strings.TrimSpace(rpcID) == "" {
		return "", fmt.Errorf("expected JSON-RPC id is empty")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&response); err != nil || response.JSONRPC != "2.0" {
		return "", fmt.Errorf("controller returned an invalid JSON-RPC response")
	}
	if err := requireKagentJSONEOF(decoder); err != nil {
		return "", fmt.Errorf("controller returned an invalid JSON-RPC response")
	}
	var returnedID string
	if err := json.Unmarshal(response.ID, &returnedID); err != nil || returnedID != rpcID {
		return "", fmt.Errorf("controller returned a response for a different RPC id")
	}
	if len(response.Error) != 0 && string(response.Error) != "null" {
		return "", fmt.Errorf("controller returned a JSON-RPC error")
	}
	if len(response.Error) != 0 && string(response.Error) == "null" {
		response.Error = nil
	}
	if len(response.Result) == 0 || string(response.Result) == "null" {
		return "", fmt.Errorf("controller returned no JSON-RPC result")
	}
	var result struct {
		Kind      string `json:"kind"`
		ID        string `json:"id"`
		ContextID string `json:"contextId"`
		MessageID string `json:"messageId"`
		Role      string `json:"role"`
		Status    struct {
			State   string `json:"state"`
			Message *struct {
				Role      string          `json:"role"`
				MessageID string          `json:"messageId"`
				ContextID string          `json:"contextId"`
				Parts     []kagentA2APart `json:"parts"`
			} `json:"message"`
		} `json:"status"`
		Artifacts []json.RawMessage `json:"artifacts"`
		History   []struct {
			Role      string          `json:"role"`
			MessageID string          `json:"messageId"`
			ContextID string          `json:"contextId"`
			Parts     []kagentA2APart `json:"parts"`
		} `json:"history"`
		Parts []kagentA2APart `json:"parts"`
	}
	resultDecoder := json.NewDecoder(bytes.NewReader(response.Result))
	if err := resultDecoder.Decode(&result); err != nil || requireKagentJSONEOF(resultDecoder) != nil {
		return "", fmt.Errorf("controller returned an invalid JSON-RPC result")
	}
	var answer string
	var answerErr error
	switch result.Kind {
	case "message":
		if !validKagentA2AID(result.MessageID) || result.Role != "agent" {
			return "", fmt.Errorf("direct message result lacks an exact agent identity")
		}
		answer, answerErr = kagentText(result.Parts)
	case "task":
		if !validKagentA2AID(result.ID) || !validKagentA2AID(result.ContextID) {
			return "", fmt.Errorf("task result lacks a task or context identity")
		}
		if result.Status.State != "completed" {
			return "", fmt.Errorf("task did not reach the required completed state")
		}
		// v0.10.2 artifacts have no agent role/message/context identity, so
		// only status or history messages can supply a task answer.
		if result.Status.Message != nil {
			statusAnswer, statusErr := kagentText(result.Status.Message.Parts)
			if statusErr != nil {
				return "", statusErr
			}
			if statusAnswer != "" && (result.Status.Message.Role != "agent" || !validKagentA2AID(result.Status.Message.MessageID) || result.Status.Message.ContextID != result.ContextID) {
				return "", fmt.Errorf("task status answer lacks a matching agent message identity")
			}
			answer = statusAnswer
		}
		if answer == "" {
			for i := len(result.History) - 1; i >= 0; i-- {
				text, textErr := kagentText(result.History[i].Parts)
				if textErr != nil {
					return "", textErr
				}
				if text == "" {
					continue
				}
				if result.History[i].Role != "agent" || !validKagentA2AID(result.History[i].MessageID) || result.History[i].ContextID != result.ContextID {
					return "", fmt.Errorf("task history answer lacks a matching agent message identity")
				}
				answer = text
				break
			}
		}
	default:
		return "", fmt.Errorf("controller returned neither a completed task nor a message")
	}
	if answerErr != nil {
		return "", answerErr
	}
	answer = strings.TrimSpace(safeTerminal(answer))
	if answer == "" {
		return "", fmt.Errorf("completed response contained no nonblank agent text answer")
	}
	return answer, nil
}

func requireKagentJSONEOF(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return fmt.Errorf("extra JSON value")
	}
	return nil
}

func validKagentA2AID(value string) bool {
	return value != "" && strings.TrimSpace(value) == value
}

func kagentText(parts []kagentA2APart) (string, error) {
	var text []string
	for _, part := range parts {
		if strings.TrimSpace(part.Text) == "" {
			continue
		}
		if part.Kind != "text" {
			return "", fmt.Errorf("answer text part does not explicitly declare kind text")
		}
		text = append(text, part.Text)
	}
	return strings.TrimSpace(strings.Join(text, "\n")), nil
}

type kagentA2APart struct {
	Kind string `json:"kind"`
	Text string `json:"text"`
}

type kagentCreateReceipt struct {
	APIVersion string                     `json:"apiVersion"`
	Kind       string                     `json:"kind"`
	ClusterUID string                     `json:"clusterUID"`
	AgentRef   agentruntime.AgentRef      `json:"agentRef"`
	Deploy     agentruntime.DeployReceipt `json:"deploy"`
}

func (a *App) kagentClusterUID(ctx context.Context) (string, error) {
	raw, err := a.kagentCapture(ctx, nil, "get", "namespace", "kube-system", "-o", "json")
	if err != nil {
		return "", fmt.Errorf("cannot establish Kagent create cluster identity (kube-system UID): %w", err)
	}
	var namespace struct {
		Kind     string `json:"kind"`
		Metadata struct {
			Name, UID string
		} `json:"metadata"`
	}
	if json.Unmarshal(raw, &namespace) != nil || namespace.Kind != "Namespace" || namespace.Metadata.Name != "kube-system" || namespace.Metadata.UID == "" {
		return "", fmt.Errorf("kube-system namespace returned no valid Kagent create cluster UID")
	}
	return namespace.Metadata.UID, nil
}

func writeKagentCreateReceipt(bundle, clusterUID string, result agentruntime.DeployResult) error {
	if bundle == "" || clusterUID == "" || result.Ref.UID == "" || result.Receipt.PortableDigest == "" || result.Receipt.RenderedDigest == "" {
		return fmt.Errorf("Kagent create receipt is incomplete")
	}
	dir := filepath.Join(bundle, "receipts")
	if err := os.Mkdir(dir, 0o700); err != nil {
		if !os.IsExist(err) {
			return err
		}
		if err := checkLiftReceiptsDir(bundle); err != nil {
			return err
		}
	}
	if err := checkLiftReceiptsDir(bundle); err != nil {
		return err
	}
	receipt := kagentCreateReceipt{
		APIVersion: "kmx.kaimahi.dev/v1alpha1", Kind: "KagentCreateReceipt", ClusterUID: clusterUID,
		AgentRef: result.Ref, Deploy: result.Receipt,
	}
	raw, err := json.MarshalIndent(receipt, "", "  ")
	if err != nil {
		return err
	}
	key := sha256.Sum256([]byte(result.Ref.Context + "\x00" + result.Ref.Namespace + "\x00" + clusterUID + "\x00" + result.Ref.UID))
	path := filepath.Join(dir, fmt.Sprintf("create-%x.json", key))
	if _, err := os.Lstat(path); err == nil {
		return fmt.Errorf("Kagent create receipt already exists; refusing to overwrite it")
	} else if !os.IsNotExist(err) {
		return err
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	complete := false
	closed := false
	defer func() {
		if !closed {
			_ = file.Close()
		}
		if !complete {
			_ = os.Remove(path)
		}
	}()
	if _, err := file.Write(append(raw, '\n')); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	closed = true
	complete = true
	return nil
}

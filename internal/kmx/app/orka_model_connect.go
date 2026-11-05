package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"runtime"
	"strings"
	"time"

	"github.com/google/uuid"
)

type modelTarget struct{ cluster, namespace, runtime, arch string }

func (a *App) modelTarget(ctx context.Context, configure bool) (modelTarget, error) {
	o := toolObserver{a: a, ctx: ctx}
	target := modelTarget{cluster: o.namespace("kube-system"), namespace: o.namespace(OrkaNamespace)}
	if target.cluster == "" || target.namespace == "" {
		return target, fmt.Errorf("model target identity unavailable")
	}
	if !configure {
		return target, nil
	}
	observed, ok := o.runtime(OrkaNamespace)
	if !ok {
		return target, fmt.Errorf("model runtime configuration unavailable")
	}
	var sa struct {
		APIVersion, Kind string
		Metadata         struct{ Name, Namespace, UID string }
	}
	if !o.get("/api/v1/namespaces/orka-system/serviceaccounts/default", &sa) || sa.APIVersion != "v1" || sa.Kind != "ServiceAccount" || sa.Metadata.Name != "default" || sa.Metadata.Namespace != OrkaNamespace || !verificationIdentity(sa.Metadata.UID) {
		return target, fmt.Errorf("model service account identity unavailable")
	}
	raw, _ := json.Marshal([]any{observed, sa.Metadata.UID})
	sum := sha256.Sum256(raw)
	target.runtime = hex.EncodeToString(sum[:])
	var nodes struct {
		APIVersion, Kind string
		Metadata         struct{ Continue string }
		Items            []struct {
			Status struct{ NodeInfo struct{ Architecture string } }
		}
	}
	if !o.get("/api/v1/nodes?limit=64", &nodes) || nodes.APIVersion != "v1" || nodes.Kind != "NodeList" || nodes.Metadata.Continue != "" || len(nodes.Items) == 0 || len(nodes.Items) > 64 {
		return target, fmt.Errorf("model node architecture unavailable")
	}
	for _, node := range nodes.Items {
		arch := node.Status.NodeInfo.Architecture
		if arch != "amd64" && arch != "arm64" || target.arch != "" && target.arch != arch {
			return target, fmt.Errorf("unsupported model node architecture")
		}
		target.arch = arch
	}
	return target, nil
}
func (t modelTarget) key() string {
	sum := sha256.Sum256([]byte(t.cluster + "\x00" + t.namespace))
	return hex.EncodeToString(sum[:])
}
func (a *App) modelWorker() *App {
	worker, cfg, runner := *a, *a.Cfg, *a.Run
	worker.Cfg, worker.Run = &cfg, &runner
	runner.Env = append(append([]string(nil), runner.Env...), "KUBERC=off", "KUBECTL_KUBERC=false")
	runner.Unset = append(append([]string(nil), runner.Unset...), "KUBERC", "KUBECTL_KUBERC")
	runner.Context = a.operationContext()
	runner.Echo = false
	return &worker
}
func modelFailure(code string, err error) error {
	if errors.Is(err, context.Canceled) {
		code = "cancelled"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		code = "deadline_exceeded"
	}
	return &OrkaModelError{code: code, cause: err}
}
func modelConnectionResult(r *modelRecord, state string) OrkaModelResult {
	if r == nil || !r.anchored {
		return OrkaModelResult{}
	}
	c := r.state.Connection
	c.State = state
	return OrkaModelResult{Connection: &c, RemoteState: "unknown"}
}
func (a *App) modelSameTarget(ctx context.Context, expected modelTarget) error {
	current, err := a.modelTarget(ctx, true)
	if err != nil || current != expected {
		return fmt.Errorf("model target or runtime changed")
	}
	return nil
}
func (a *App) modelAbsent(ctx context.Context, r modelOwnedResource) error {
	api, ok := modelAPIFor(r.Kind)
	if !ok {
		return fmt.Errorf("invalid model kind")
	}
	resource := api.plural
	if group, _, grouped := strings.Cut(api.version, "/"); grouped {
		resource += "." + group
	}
	raw, err := a.orkaCapture(ctx, nil, "-n", OrkaNamespace, "get", resource, r.Name, "--ignore-not-found=true", "-o", "jsonpath={.metadata.uid}")
	if err != nil {
		return err
	}
	if len(raw) != 0 {
		return fmt.Errorf("model resource name is occupied")
	}
	return nil
}
func (a *App) modelPodAdmission(ctx context.Context, dep map[string]any) error {
	template := modelMap(modelMap(dep["spec"])["template"])
	pod := map[string]any{"apiVersion": "v1", "kind": "Pod", "metadata": map[string]any{"name": modelText(modelMap(dep["metadata"])["name"]) + "-preflight", "namespace": OrkaNamespace, "labels": modelMap(template["metadata"])["labels"], "annotations": modelMap(template["metadata"])["annotations"]}, "spec": template["spec"]}
	if err := a.annotateCreatedByVersion(pod); err != nil {
		return err
	}
	raw, _ := json.Marshal(pod)
	reply, err := a.orkaCapture(ctx, raw, "-n", OrkaNamespace, "create", "--dry-run=server", "-f", "-", "-o", "json")
	if err != nil {
		return err
	}
	var admitted map[string]any
	if modelJSON(reply, &admitted) != nil || admitted["kind"] != "Pod" || admitted["apiVersion"] != "v1" || modelMap(admitted["metadata"])["namespace"] != OrkaNamespace {
		return fmt.Errorf("model Pod admission unavailable")
	}
	var normalized map[string]any
	modelJSON(raw, &normalized)
	if !modelSpecMatches(normalized["spec"], admitted["spec"], ".template.spec", "Deployment") {
		return fmt.Errorf("model Pod admission changed execution")
	}
	return nil
}

// ConnectOrkaModel configures only the owned gateway and native Provider.
// Acknowledgment loss never enters a recovery CREATE or ownership adoption.
func (a *App) ConnectOrkaModel(opt OrkaModelConnectOptions) (result OrkaModelResult, err error) {
	if a == nil || a.Cfg == nil || a.Run == nil || a.Cfg.KubeContext == "" || !modelUUID(opt.OperationID) || !modelUUID(opt.CredentialGeneration) || opt.Namespace != OrkaNamespace || opt.Route != "copilot" || !modelResourceName(opt.Provider) || len(opt.Provider) > 45 || !verificationIdentity(opt.Model) || strings.EqualFold(opt.Model, "auto") {
		return result, modelFailure("invalid_arguments", nil)
	}
	if runtime.GOOS != "linux" || a.Cfg.ContainerEngine != "docker" {
		return result, modelFailure("unsupported", nil)
	}
	bounded, cancel := a.waitContext(a.operationContext(), "orka-model-operation", 2*time.Minute)
	defer cancel()
	if err = bounded.Err(); err != nil {
		return result, modelFailure("cancelled", err)
	}
	a, err = a.modelWorker().withRunContext(bounded).offlineTarget()
	if err != nil {
		return result, modelFailure("dependency_unavailable", err)
	}
	ctx := a.operationContext()
	if _, err = a.verifyOfflineKubeconfig(true); err != nil {
		return result, modelFailure("permission_denied", err)
	}
	stage := "preflight"
	var record *modelRecord
	defer func() {
		if err != nil && ctx.Err() != nil {
			err = modelFailure("cancelled", ctx.Err())
		}
		if err != nil {
			a.modelProgress(stage, "failed", err)
		}
		if err != nil && record != nil {
			result = modelConnectionResult(record, "unknown")
		}
		if err != nil {
			var safe *OrkaModelError
			if !errors.As(err, &safe) {
				err = modelFailure("execution_unknown", err)
			}
		}
		if record != nil {
			record.close()
		}
	}()
	if err = a.modelCredentialInput(); err != nil {
		return result, modelFailure("credential_unavailable", err)
	}
	if a.Cfg.ContainerEngine != "docker" {
		return result, modelFailure("unsupported", nil)
	}
	if err = a.checkModelNodeMounts(); err != nil {
		return result, err
	}
	target, err := a.modelTarget(ctx, true)
	if err != nil {
		return result, modelFailure("dependency_unavailable", err)
	}
	if err = a.guardOrkaMutation(ctx, CreateOptions{Namespace: OrkaNamespace}, "configure Orka model gateway and Provider"); err != nil {
		return result, modelFailure("permission_denied", err)
	}
	record, err = openModelRecord(ctx, target.key(), true)
	if err != nil {
		return result, err
	}
	if record.anchored {
		c := record.state.Connection
		if record.state.Complete || record.state.Pending != nil || len(record.state.Resources) != 4 || record.state.RuntimeDigest != target.runtime || c.Route != opt.Route || c.Provider != opt.Provider || c.Model != opt.Model || c.CredentialGeneration != opt.CredentialGeneration {
			return result, fmt.Errorf("retained model connection requires explicit disconnect")
		}
		for _, root := range record.state.Resources {
			_, present, e := a.readModelObject(ctx, OrkaNamespace, root)
			if e != nil || !present {
				return result, fmt.Errorf("retained model ownership unavailable")
			}
		}
		a.modelProgress("preflight", "done", nil)
		stage = "model"
		a.modelProgress(stage, "active", nil)
		children, e := a.modelCheckGateway(ctx, c, record.state.Resources)
		if e != nil {
			return result, e
		}
		updated := record.state
		updated.GatewayChildren = mergeModelChildren(updated.GatewayChildren, children)
		if !sameModelChildren(updated.GatewayChildren, record.state.GatewayChildren) {
			if err = record.checkpoint(ctx, updated); err != nil {
				return result, err
			}
		}
		if err = a.modelSameTarget(ctx, target); err != nil {
			return result, err
		}
		a.modelProgress(stage, "done", nil)
		result = modelConnectionResult(record, "model_ready")
		result.RemoteState = "may_continue"
		return result, nil
	}
	docs, err := modelDocuments(opt.Provider, opt.Model, opt.OperationID, target.arch)
	if err != nil {
		return result, err
	}
	for _, doc := range docs {
		b, _ := json.Marshal(doc)
		var normalized map[string]any
		modelJSON(b, &normalized)
		r, e := modelDesired(normalized)
		if e != nil {
			return result, e
		}
		if err = a.modelAbsent(ctx, r); err != nil {
			return result, err
		}
	}
	if err = a.modelPodAdmission(ctx, docs[1]); err != nil {
		return result, err
	}
	a.modelProgress("preflight", "done", nil)
	state := modelCheckpoint{Version: 1, ClusterUID: target.cluster, NamespaceUID: target.namespace, RuntimeDigest: target.runtime, Connection: OrkaModelConnection{ID: uuid.NewString(), Route: opt.Route, Provider: opt.Provider, Model: opt.Model, CredentialGeneration: opt.CredentialGeneration, State: "unknown"}}
	if err = record.checkpoint(ctx, state); err != nil {
		return result, err
	}
	stage = "gateway"
	a.modelProgress(stage, "active", nil)
	for _, doc := range docs {
		if doc["kind"] == "Provider" {
			a.modelProgress(stage, "done", nil)
			stage = "provider"
			a.modelProgress(stage, "active", nil)
		}
		if err = a.modelSameTarget(ctx, target); err != nil {
			return result, err
		}
		b, _ := json.Marshal(doc)
		var normalized map[string]any
		modelJSON(b, &normalized)
		desired, e := modelDesired(normalized)
		if e != nil {
			return result, e
		}
		state.Pending = &modelPendingAction{OperationID: opt.OperationID, Verb: "create", Resource: desired}
		if err = record.checkpoint(ctx, state); err != nil {
			return result, err
		}
		observed, e := a.createModelObjectObserved(ctx, OrkaNamespace, doc)
		if e != nil {
			return result, e
		}
		state.Resources = append(append([]modelOwnedResource(nil), state.Resources...), observed)
		state.Pending = nil
		if err = record.checkpoint(ctx, state); err != nil {
			return result, err
		}
	}
	a.modelProgress(stage, "done", nil)
	stage = "model"
	a.modelProgress(stage, "active", nil)
	children, e := a.modelCheckGateway(ctx, state.Connection, state.Resources)
	if e != nil {
		return result, e
	}
	state.GatewayChildren = children
	if err = a.modelSameTarget(ctx, target); err != nil {
		return result, err
	}
	state.Connection.State = "model_ready"
	if err = record.checkpoint(ctx, state); err != nil {
		return result, err
	}
	a.modelProgress(stage, "done", nil)
	result = modelConnectionResult(record, "model_ready")
	result.RemoteState = "may_continue"
	return result, nil
}

func (a *App) modelProgress(stage, status string, err error) {
	if a.operationProgress != nil {
		a.operationProgress("Model "+stage, status, err)
	}
}

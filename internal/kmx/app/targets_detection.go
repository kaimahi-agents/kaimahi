package app

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
	"unicode"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentsessions"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/secretshapes"
	"google.golang.org/grpc/status"
)

func unreadableTarget(scope, code, message, action string) TargetDetection {
	return TargetDetection{State: "unreadable", Scope: scope, Detail: "requested observation failed; no absence or fallback inferred", Error: &TargetDetectionError{Code: code, Message: message, Action: action}}
}

func (a *App) detectOrkaTarget() TargetDetection {
	const scope = "controller Deployments in orka-system"
	if a.Cfg == nil || a.Cfg.KubeContext == "" || a.Run == nil {
		return unreadableTarget(scope, "configuration_unreadable", "Kubernetes context configuration unavailable", "check --context, KUBE_CTX or the saved kmx ctx selection")
	}
	contextName := a.Cfg.KubeContext
	if len(contextName) > 256 || secretshapes.Match(contextName) != nil {
		return unreadableTarget(scope, "invalid_context", "unsafe Kubernetes context label refused", "select a non-credential context name")
	}
	for _, c := range contextName {
		if unicode.IsControl(c) {
			return unreadableTarget(scope, "invalid_context", "unsafe Kubernetes context label refused", "select a printable context name")
		}
	}
	data, err := a.orkaCapture(a.operationContext(), nil, "-n", OrkaNamespace, "get", "deploy", "-o", "json")
	if err != nil {
		return unreadableTarget(scope, "read_failed", "cannot read controller Deployments", "check kubectl, selected context, network access and Deployment-list RBAC")
	}
	// A successful but empty/malformed API response is not an empty collection.
	var list struct {
		Kind  string            `json:"kind"`
		Items *[]orkaDeployment `json:"items"`
	}
	if err := json.Unmarshal(data, &list); err != nil || list.Kind != "DeploymentList" || list.Items == nil {
		return unreadableTarget(scope, "malformed", "invalid DeploymentList response", "inspect the selected Kubernetes API and kubectl transport")
	}
	var candidates []orkaDeployment
	for _, d := range *list.Items {
		if d.Metadata.Name == "" {
			return unreadableTarget(scope, "malformed", "Deployment identity unavailable", "inspect the selected Kubernetes API response")
		}
		labels := d.Metadata.Labels
		if labels["app.kubernetes.io/name"] == "orka" && (labels["app.kubernetes.io/component"] == "controller" || labels["control-plane"] == "controller-manager") {
			candidates = append(candidates, d)
		}
	}
	if len(candidates) == 0 {
		return TargetDetection{State: "absent", Scope: scope, Detail: fmt.Sprintf("no labelled Orka controller in context %s; other namespaces and runtime resources are not inspected", contextName)}
	}
	controller, _, err := selectOrkaController(candidates)
	if err != nil {
		return unreadableTarget(scope, "ambiguous", "multiple Orka controller identities", "resolve controller ambiguity in the detection namespace; no runtime selected")
	}
	result := TargetDetection{State: "present", Scope: scope, Detail: fmt.Sprintf("labelled Orka controller in context %s; image version unknown; readiness and workload success not checked", contextName)}
	controllers := 0
	image := ""
	for _, container := range controller.Spec.Template.Spec.Containers {
		if container.Name == "controller" {
			controllers++
			image = container.Image
		}
	}
	if controllers == 1 && image == "ghcr.io/orka-agents/orka@"+orkaControllerDigest {
		result.Version = OrkaVersion
		result.Detail = fmt.Sprintf("controller image matches the pinned %s digest in context %s; readiness and workload success not checked", OrkaVersion, contextName)
	}
	return result
}

func (a *App) detectSessionsTarget(opt TargetsOptions) TargetDetection {
	const action = "check the explicit endpoint, verified TLS/CA and Sessions read authorization; no registry or harness support inferred"
	scope := "Sessions API at " + opt.Sessions
	client, err := agentsessions.Dial(agentsessions.Options{Address: opt.Sessions, CAFile: opt.SessionsCA})
	if err != nil {
		return unreadableTarget(scope, status.Code(err).String(), "Sessions transport configuration unreadable", action)
	}
	defer client.Close()
	ctx, cancel := context.WithTimeout(a.operationContext(), 5*time.Second)
	defer cancel()
	if err := client.Probe(ctx); err != nil {
		return unreadableTarget(scope, status.Code(err).String(), "Sessions API read failed", action)
	}
	return TargetDetection{State: "present", Scope: scope, Detail: "Sessions API readable; host version, chat harness and model unknown; HarnessRegistry not probed or qualified"}
}

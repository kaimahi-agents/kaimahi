package app

import (
	"context"
	"fmt"
	"runtime"
	"time"
)

func (a *App) modelWaitDeleted(ctx context.Context, root modelOwnedResource, children []modelChild) error {
	ctx, cancel := a.waitContext(ctx, "orka-model-delete", 30*time.Second)
	defer cancel()
	for {
		_, exists, err := a.readModelObject(ctx, OrkaNamespace, root)
		if err != nil {
			return err
		}
		gone, err := a.modelChildrenGone(ctx, OrkaNamespace, children)
		if err != nil {
			return err
		}
		if !exists && gone {
			return nil
		}
		if err := a.pause(ctx, 250*time.Millisecond); err != nil {
			return err
		}
	}
}

// DisconnectOrkaModel is always explicit. Observation of an intact prior
// ambiguous DELETE authorizes only this new operation's fresh conditional send.
func (a *App) DisconnectOrkaModel(opt OrkaModelDisconnectOptions) (result OrkaModelResult, err error) {
	if a == nil || a.Cfg == nil || a.Run == nil || a.Cfg.KubeContext == "" || !modelUUID(opt.OperationID) || !modelUUID(opt.Connection) || opt.Namespace != OrkaNamespace || !modelResourceName(opt.Provider) {
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
	target, err := a.modelTarget(ctx, false)
	if err != nil {
		return result, modelFailure("dependency_unavailable", err)
	}
	if err = a.guardOrkaMutation(ctx, CreateOptions{Namespace: OrkaNamespace}, "disconnect retained Orka model gateway and Provider"); err != nil {
		return result, modelFailure("permission_denied", err)
	}
	record, err := openModelRecord(ctx, target.key(), false)
	if err != nil {
		return result, modelStateError(err)
	}
	defer record.close()
	stage := "preflight"
	gatewayStopped := false
	defer func() {
		if err != nil {
			if ctx.Err() != nil {
				err = modelFailure("cancelled", ctx.Err())
			}
			a.modelProgress(stage, "failed", err)
			result = modelConnectionResult(record, "unknown")
			if gatewayStopped {
				result.RemoteState = "stopped"
			}
			err = modelFailure("execution_unknown", err)
		}
	}()
	state := record.state
	if state.Connection.ID != opt.Connection || state.Connection.Provider != opt.Provider || state.ClusterUID != target.cluster || state.NamespaceUID != target.namespace {
		return result, fmt.Errorf("retained model connection mismatch")
	}
	if state.Complete {
		a.modelProgress("preflight", "done", nil)
		stage = "finalize"
		a.modelProgress(stage, "active", nil)
		if err = record.finish(ctx); err != nil {
			return result, err
		}
		a.modelProgress(stage, "done", nil)
		result = modelConnectionResult(record, "gateway_stopped")
		result.RemoteState = "stopped"
		return result, nil
	}
	if state.Pending != nil && (state.Pending.Verb == "create" || state.Pending.OperationID == opt.OperationID) {
		return result, fmt.Errorf("unresolved model mutation requires a new explicit recovery operation")
	}
	_, gatewayErr := modelGatewayRoot(state.Resources)
	gatewayStopped = gatewayErr != nil
	for _, root := range state.Resources {
		current, exists, e := a.readModelObject(ctx, OrkaNamespace, root)
		if e != nil {
			return result, e
		}
		if !exists && root.Kind == "Deployment" && state.Pending == nil && len(state.GatewayChildren) == 0 {
			return result, fmt.Errorf("missing gateway has no retained process closure")
		}
		if exists {
			if _, e = a.modelChildren(ctx, OrkaNamespace, current); e != nil {
				return result, e
			}
		}
	}
	a.modelProgress("preflight", "done", nil)
	stage = "gateway"
	a.modelProgress(stage, "active", nil)
	for _, kind := range []string{"Deployment", "Provider", "Service", "Secret"} {
		if kind == "Provider" {
			a.modelProgress(stage, "done", nil)
			stage = "provider"
			a.modelProgress(stage, "active", nil)
		}
		for index := 0; index < len(state.Resources); index++ {
			root := state.Resources[index]
			if root.Kind != kind {
				continue
			}
			current, exists, e := a.readModelObject(ctx, OrkaNamespace, root)
			if e != nil {
				return result, e
			}
			var children []modelChild
			if root.Kind == "Deployment" {
				children = append(children, state.GatewayChildren...)
			}
			var observed []modelChild
			if state.Pending != nil {
				if state.Pending.Verb != "delete" || state.Pending.Resource.UID != root.UID {
					return result, fmt.Errorf("pending model cleanup identity mismatch")
				}
				children = mergeModelChildren(children, state.Pending.Children)
			}
			if exists {
				observed, e = a.modelChildren(ctx, OrkaNamespace, current)
				if e != nil {
					return result, e
				}
				for _, child := range observed {
					seen := false
					for _, prior := range children {
						if prior == child {
							seen = true
						}
					}
					if !seen {
						children = append(children, child)
					}
				}
				// Validate retained children before granting another conditional send.
				if _, e = a.modelChildrenGone(ctx, OrkaNamespace, children); e != nil {
					return result, e
				}
				now, e := a.modelTarget(ctx, false)
				if e != nil || now.cluster != target.cluster || now.namespace != target.namespace {
					return result, fmt.Errorf("model cleanup target changed")
				}
				state.Pending = &modelPendingAction{OperationID: opt.OperationID, Verb: "delete", Resource: current, Children: children}
				if err = record.checkpoint(ctx, state); err != nil {
					return result, err
				}
				if err = a.modelDeleteOwned(ctx, OrkaNamespace, current, observed...); err != nil {
					return result, err
				}
			}
			if err = a.modelWaitDeleted(ctx, root, children); err != nil {
				return result, err
			}
			// Also bracket post-delete closure inspection: unseen descendants must not
			// be omitted merely because the root vanished before a later invocation.
			remaining, e := a.modelChildren(ctx, OrkaNamespace, root)
			if e != nil {
				return result, e
			}
			if len(remaining) != 0 {
				return result, fmt.Errorf("model child closure remains")
			}
			if root.Kind == "Deployment" {
				if err = a.modelGatewayStopped(ctx, root, children); err != nil {
					return result, err
				}
				state.GatewayChildren = nil
				gatewayStopped = true
			}
			state.Resources = append(append([]modelOwnedResource(nil), state.Resources[:index]...), state.Resources[index+1:]...)
			index--
			state.Pending = nil
			if err = record.checkpoint(ctx, state); err != nil {
				return result, err
			}
		}
	}
	if len(state.Resources) != 0 || state.Pending != nil {
		return result, fmt.Errorf("unrecognized retained model effects")
	}
	a.modelProgress(stage, "done", nil)
	stage = "finalize"
	a.modelProgress(stage, "active", nil)
	state.Complete = true
	state.Connection.State = "gateway_stopped"
	if err = record.checkpoint(ctx, state); err != nil {
		return result, err
	}
	if err = record.finish(ctx); err != nil {
		return result, err
	}
	a.modelProgress(stage, "done", nil)
	result = modelConnectionResult(record, "gateway_stopped")
	result.RemoteState = "stopped"
	return result, nil
}

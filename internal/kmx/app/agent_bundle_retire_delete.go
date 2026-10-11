package app

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

// deleteRetireObject issues an atomic UID- and resourceVersion-conditional DELETE
// against the explicitly selected context, without a local proxy or listener.
func (a *App) deleteRetireObject(ctx context.Context, namespace string, d retireDecision) error {
	if d.uid == "" || d.version == "" {
		return fmt.Errorf("conditional deletion requires both UID and resourceVersion")
	}
	config, err := a.retireClientConfig()
	if err != nil {
		return err
	}
	client, err := dynamic.NewForConfig(config)
	if err != nil {
		return fmt.Errorf("cannot prepare conditional Kubernetes deletion")
	}
	uid := types.UID(d.uid)
	deleteCtx, cancel := a.waitContext(ctx, "retire-delete-request", 20*time.Second)
	defer cancel()
	err = client.Resource(schema.GroupVersionResource{Group: "core.orka.ai", Version: "v1alpha1", Resource: strings.ToLower(d.kind) + "s"}).Namespace(namespace).Delete(deleteCtx, d.name, metav1.DeleteOptions{
		Preconditions: &metav1.Preconditions{UID: &uid, ResourceVersion: &d.version},
	})
	if err != nil {
		// API status messages and transport errors may include server-controlled
		// text or credentials; never return or log the raw error.
		switch {
		case apierrors.IsConflict(err):
			return fmt.Errorf("conditional deletion conflicted; inspect object version before retrying")
		case apierrors.IsNotFound(err):
			return fmt.Errorf("conditional deletion refused: object not found; inspect target before retrying")
		default:
			return fmt.Errorf("conditional deletion failed; inspect target and permissions before retrying")
		}
	}
	waitCtx, stop := a.waitContext(ctx, "retire-deletion", 8*time.Second)
	defer stop()
	for {
		raw, err := a.orkaCapture(waitCtx, nil, "-n", namespace, "get", orkaPlural(d.kind), d.name, "--ignore-not-found=true", "-o", "json")
		if err != nil {
			return fmt.Errorf("cannot confirm %s/%s was deleted; inspect target before retrying", d.kind, d.name)
		}
		if len(raw) == 0 {
			return nil
		}
		var live struct {
			Metadata struct {
				UID string `json:"uid"`
			} `json:"metadata"`
		}
		if json.Unmarshal(raw, &live) != nil || live.Metadata.UID != d.uid {
			return fmt.Errorf("%s/%s changed while waiting for deletion", d.kind, d.name)
		}
		if err := a.pause(waitCtx, 250*time.Millisecond); err != nil {
			return fmt.Errorf("%s/%s deletion is still pending; retirement remains incomplete", d.kind, d.name)
		}
	}
}

// retireClientConfig mirrors the runner's effective KUBECONFIG, including an
// override used for saved Agent locations, while overriding current-context.
func (a *App) retireClientConfig() (*rest.Config, error) {
	rules := clientcmd.NewDefaultClientConfigLoadingRules()
	if a.Run != nil {
		unset := false
		for _, key := range a.Run.Unset {
			if key == "KUBECONFIG" {
				unset = true
			}
		}
		if unset {
			rules.Precedence = []string{clientcmd.RecommendedHomeFile}
		}
		for _, entry := range a.Run.Env {
			if value, ok := strings.CutPrefix(entry, "KUBECONFIG="); ok {
				if value == "" {
					rules.Precedence = []string{clientcmd.RecommendedHomeFile}
				} else {
					rules.Precedence = filepath.SplitList(value)
				}
			}
		}
	}
	config, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(rules, &clientcmd.ConfigOverrides{
		CurrentContext: a.Cfg.KubeContext,
	}).ClientConfig()
	if err != nil {
		return nil, fmt.Errorf("cannot load selected Kubernetes context for conditional deletion")
	}
	return config, nil
}

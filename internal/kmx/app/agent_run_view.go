package app

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/runview"
	runorka "github.com/kaimahi-agents/kaimahi/internal/kmx/runview/orka"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/scaffold"
)

// ReadOrkaRun reads one root and its verified descendants without changing
// Tasks. The caller's configured Kubernetes context supplies every CRD read.
func (a *App) ReadOrkaRun(ctx context.Context, namespace, rootName, rootUID string) (runview.Run, error) {
	if a.Cfg == nil || a.Run == nil {
		return runview.Run{}, fmt.Errorf("run view requires a configured Kubernetes caller")
	}
	if err := scaffold.ValidateNamespace(namespace); err != nil {
		return runview.Run{}, err
	}
	if err := scaffold.ValidateObjectName(rootName); err != nil {
		return runview.Run{}, err
	}
	if rootUID == "" {
		return runview.Run{}, fmt.Errorf("root Task UID is required")
	}
	if err := a.preflight(depKubectl); err != nil {
		return runview.Run{}, err
	}
	cluster, err := a.liftClusterUID(ctx)
	if err != nil {
		return runview.Run{}, err
	}
	// Check the root using the caller's own credentials before minting the
	// namespaced Task-get reader used for the events/trace HTTP API. The
	// bearer retains the selected ServiceAccount's full effective authority.
	source := orkaRunSource{app: a}
	raw, err := source.Task(ctx, namespace, rootName)
	if err != nil {
		return runview.Run{}, fmt.Errorf("root Task read: %w", err)
	}
	var root struct {
		Metadata struct {
			UID string `json:"uid"`
		} `json:"metadata"`
	}
	if json.Unmarshal(raw, &root) != nil || root.Metadata.UID != rootUID {
		return runview.Run{}, fmt.Errorf("root Task UID changed or missing")
	}
	bounded, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	session, sessionErr := a.openOrkaResultSession(bounded, CreateOptions{Namespace: namespace, ResultServiceAccount: orkaResultAccount, ResultPort: "19180"})
	if session != nil {
		defer session.close()
	}
	source.session, source.sessionErr = session, sessionErr
	return runorka.NewReader(source).Read(bounded, cluster, namespace, rootName, rootUID)
}

type orkaRunSource struct {
	app        *App
	session    *orkaResultSession
	sessionErr error
}

func (s orkaRunSource) Task(ctx context.Context, ns, name string) ([]byte, error) {
	raw, err := s.app.orkaCapture(ctx, nil, "-n", ns, "get", orkaPlural("Task"), name, "-o", "json")
	return raw, orkaRunReadError(err)
}
func orkaRunReadError(err error) error {
	if err != nil && strings.Contains(err.Error(), "access forbidden") {
		return runorka.ErrDenied
	}
	if err != nil && strings.Contains(err.Error(), "cancelled or timed out") {
		return runorka.ErrConnectionLost
	}
	return err
}
func (s orkaRunSource) Children(ctx context.Context, ns, parent string) ([][]byte, error) {
	raw, err := s.app.orkaCapture(ctx, nil, "-n", ns, "get", orkaPlural("Task"), "-l", "orka.ai/parent-task="+runorka.ParentSelector(parent), "-o", "json")
	if err != nil {
		return nil, orkaRunReadError(err)
	}
	var list struct {
		Items []json.RawMessage `json:"items"`
	}
	if json.Unmarshal(raw, &list) != nil || list.Items == nil {
		return nil, fmt.Errorf("invalid Task list")
	}
	out := make([][]byte, 0, len(list.Items))
	for _, item := range list.Items {
		out = append(out, item)
	}
	return out, nil
}
func orkaRunHelpers(raw []byte, namespace, name string) (runorka.HelperPolicy, error) {
	var agent struct {
		Kind     string                                `json:"kind"`
		Metadata struct{ Name, Namespace, UID string } `json:"metadata"`
		Spec     struct {
			Coordination *struct {
				Enabled       bool                               `json:"enabled"`
				AllowedAgents []struct{ Name, Namespace string } `json:"allowedAgents"`
			} `json:"coordination"`
		} `json:"spec"`
	}
	if json.Unmarshal(raw, &agent) != nil || agent.Kind != "Agent" || agent.Metadata.Name != name || agent.Metadata.Namespace != namespace || agent.Metadata.UID == "" {
		return runorka.HelperPolicy{}, fmt.Errorf("invalid Agent coordination response")
	}
	if agent.Spec.Coordination == nil {
		return runorka.HelperPolicy{State: "not configured"}, nil
	}
	if !agent.Spec.Coordination.Enabled {
		return runorka.HelperPolicy{State: "disabled"}, nil
	}
	if len(agent.Spec.Coordination.AllowedAgents) == 0 {
		return runorka.HelperPolicy{State: "unbounded"}, nil
	}
	policy := runorka.HelperPolicy{State: "enabled"}
	for _, ref := range agent.Spec.Coordination.AllowedAgents {
		if scaffold.ValidateObjectName(ref.Name) != nil || (ref.Namespace != "" && scaffold.ValidateNamespace(ref.Namespace) != nil) {
			return runorka.HelperPolicy{}, fmt.Errorf("invalid Agent helper reference")
		}
		policy.Helpers = append(policy.Helpers, runview.Agent{Name: ref.Name, Namespace: ref.Namespace})
	}
	return policy, nil
}
func (s orkaRunSource) Helpers(ctx context.Context, ns, name string) (runorka.HelperPolicy, error) {
	if name == "" {
		return runorka.HelperPolicy{}, fmt.Errorf("root has no Agent reference")
	}
	raw, err := s.app.orkaCapture(ctx, nil, "-n", ns, "get", orkaPlural("Agent"), name, "-o", "json")
	if err != nil {
		return runorka.HelperPolicy{}, orkaRunReadError(err)
	}
	return orkaRunHelpers(raw, ns, name)
}
func (s orkaRunSource) Events(ctx context.Context, ns, name string, after int64, limit int) ([]byte, int, error) {
	if s.session == nil {
		return nil, 0, s.sessionErr
	}
	status, body, err := s.session.getTaskResource(ctx, ns, name, "events", url.Values{"after": {fmt.Sprint(after)}, "limit": {fmt.Sprint(limit)}})
	if err != nil {
		if status != 0 && status != http.StatusOK {
			return nil, status, nil
		}
		if strings.Contains(err.Error(), "size limit") {
			return nil, status, runorka.ErrPageTooLarge
		}
		if status == 0 || strings.Contains(err.Error(), "timed out") {
			return nil, status, runorka.ErrConnectionLost
		}
		return nil, status, runorka.ErrInvalidResponse
	}
	if status != http.StatusOK {
		return nil, status, nil
	}
	raw, err := json.Marshal(body)
	return raw, status, err
}
func (s orkaRunSource) Trace(ctx context.Context, ns, name string) (int, error) {
	if s.session == nil {
		return 0, s.sessionErr
	}
	code, _, err := s.session.getTaskResource(ctx, ns, name, "trace", nil)
	if err != nil {
		if code != 0 && code != http.StatusOK {
			return code, nil
		}
		if strings.Contains(err.Error(), "size limit") {
			return code, runorka.ErrPageTooLarge
		}
		if code == 0 || strings.Contains(err.Error(), "timed out") {
			return code, runorka.ErrConnectionLost
		}
		return code, runorka.ErrInvalidResponse
	}
	return code, nil
}

var _ runorka.Source = orkaRunSource{}

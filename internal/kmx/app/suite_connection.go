package app

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/admin"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/config"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/imagelift"
)

func ReadWorkspaceDeployments(root, name string) ([]WorkspaceDeployment, error) {
	dirs, err := os.ReadDir(filepath.Join(root, ".kmx"))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	out := []WorkspaceDeployment{}
	for _, file := range dirs {
		if file.IsDir() || !strings.HasPrefix(file.Name(), name+"-") || !strings.HasSuffix(file.Name(), "-deployment.json") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(root, ".kmx", file.Name()))
		if err != nil {
			return nil, err
		}
		var record WorkspaceDeployment
		if err := json.Unmarshal(data, &record); err != nil {
			return nil, errors.New("invalid workspace deployment record")
		}
		if err := record.Environment.Validate(); err != nil {
			return nil, err
		}
		if record.WorkspaceName != name {
			continue
		}
		out = append(out, record)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Environment.Name < out[j].Environment.Name })
	return out, nil
}

type SuiteMemberStatus struct {
	Agent string `json:"agent"`
	Name  string `json:"name"`
	Image string `json:"image"`
	Model string `json:"model"`
	UID   string `json:"uid"`
	Ready bool   `json:"ready"`
}

func (a *App) suiteTarget(ctx context.Context, env imagelift.Environment) (*App, error) {
	if err := env.Validate(); err != nil {
		return nil, err
	}
	worker := *a.withRunContext(ctx)
	cfg := *a.Cfg
	cfg.KubeContext, cfg.ContextSource = env.Context, config.SourceFlag
	worker.Cfg = &cfg
	cluster := imageLiftCluster{app: &worker}
	if err := imagelift.CheckTarget(ctx, cluster, imagelift.Plan{ClusterUID: env.ClusterUID, Namespace: env.Namespace, Platform: env.Platform}); err != nil {
		return nil, err
	}
	return &worker, nil
}

// SuiteDeploymentStatus verifies the observed workload identity and binding
// against the stored receipt before offering a connection to that deployment.
func (a *App) SuiteDeploymentStatus(ctx context.Context, record WorkspaceDeployment) ([]SuiteMemberStatus, error) {
	worker, err := a.suiteTarget(ctx, record.Environment)
	if err != nil {
		return nil, err
	}
	cluster := imageLiftCluster{app: worker}
	var out []SuiteMemberStatus
	if record.Receipt.Plan.Target.Context != record.Environment.Context || record.Receipt.Plan.Target.ClusterUID != record.Environment.ClusterUID || record.Receipt.Plan.Target.Namespace != record.Environment.Namespace {
		return nil, errors.New("deployment receipt target differs from saved environment")
	}
	for id, binding := range record.Environment.Members {
		raw, err := cluster.Call(ctx, nil, "-n", record.Environment.Namespace, "get", "deployment", binding.Name, "-o", "json")
		if err != nil {
			return out, err
		}
		var obj struct {
			Metadata struct {
				UID                 string
				Generation          int64
				Labels, Annotations map[string]string
			}
			Spec struct {
				Template struct {
					Metadata struct{ Annotations map[string]string }
					Spec     struct {
						Containers []struct {
							Image         string
							Command, Args []string
						}
					}
				}
			}
			Status struct {
				ObservedGeneration                 int64
				UpdatedReplicas, AvailableReplicas int
			}
		}
		if err := json.Unmarshal(raw, &obj); err != nil {
			return out, err
		}
		uid, digest, configurationDigest, configurationUID := "", "", "", ""
		for _, member := range record.Receipt.Members {
			if member.Agent == id {
				for _, resource := range member.Resources {
					if resource.Kind == "Deployment" {
						uid = resource.UID
					}
					if resource.Kind == "ConfigMap" {
						configurationUID = resource.UID
					}
				}
			}
		}
		for _, member := range record.Receipt.Plan.Members {
			if member.Agent == id {
				digest = member.Digest
				configurationDigest = member.ConfigurationDigest
			}
		}
		if uid == "" || obj.Metadata.UID != uid || obj.Metadata.Labels[imagelift.OwnerLabel] != binding.Name || obj.Metadata.Annotations[imagelift.PlanAnnotation] != digest || len(obj.Spec.Template.Spec.Containers) != 1 || obj.Spec.Template.Spec.Containers[0].Image != binding.Image {
			return out, errors.New("deployment identity changed or has no complete receipt; inspect and replan")
		}
		if len(obj.Spec.Template.Spec.Containers[0].Command) != 0 || len(obj.Spec.Template.Spec.Containers[0].Args) != 0 {
			return out, errors.New("deployment overrides the image execution contract")
		}
		if binding.Inference != nil {
			raw, err := cluster.Call(ctx, nil, "-n", record.Environment.Namespace, "get", "configmap", binding.Name, "-o", "json")
			if err != nil {
				return out, err
			}
			var cm struct {
				Metadata struct{ UID string }
				Data     map[string]string
			}
			if json.Unmarshal(raw, &cm) != nil || configurationDigest == "" || configurationUID == "" || cm.Metadata.UID != configurationUID || fmt.Sprintf("%x", sha256.Sum256([]byte(cm.Data["agent.json"]))) != configurationDigest || obj.Spec.Template.Metadata.Annotations["kaimahi.dev/config"] != configurationDigest {
				return out, errors.New("deployment runtime configuration differs from its receipt; replan before connecting")
			}
		}
		model := ""
		if binding.Inference != nil {
			model = binding.Inference.Model
		}
		out = append(out, SuiteMemberStatus{Agent: id, Name: binding.Name, Image: binding.Image, Model: model, UID: uid, Ready: obj.Status.ObservedGeneration >= obj.Metadata.Generation && obj.Status.UpdatedReplicas == 1 && obj.Status.AvailableReplicas == 1})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Agent < out[j].Agent })
	return out, nil
}

type suiteForwardKube struct{ app *App }

func (k suiteForwardKube) Command(args ...string) *exec.Cmd {
	return k.app.Run.Command("kubectl", k.app.kubectl(args...)...)
}
func (k suiteForwardKube) Capture(args ...string) (string, error) {
	data, err := (imageLiftCluster{app: k.app}).Call(k.app.operationContext(), nil, args...)
	return string(data), err
}

// RunSuiteDeployment invokes the deployed image over an owned loopback forward.
// The model credential stays in the cluster; only endpoint authentication is
// read into this process, never persisted or passed in argv.
func (a *App) RunSuiteDeployment(ctx context.Context, record WorkspaceDeployment, member, prompt string) (string, error) {
	if prompt == "" || len(prompt) > 64<<10 {
		return "", errors.New("prompt must contain 1–65536 bytes")
	}
	statuses, err := a.SuiteDeploymentStatus(ctx, record)
	if err != nil {
		return "", err
	}
	binding, ok := record.Environment.Members[member]
	if !ok {
		return "", errors.New("unknown suite member")
	}
	ready := false
	for _, status := range statuses {
		if status.Agent == member {
			ready = status.Ready
		}
	}
	if !ready {
		return "", errors.New("selected deployment is not ready")
	}
	worker, err := a.suiteTarget(ctx, record.Environment)
	if err != nil {
		return "", err
	}
	auth := binding.Inputs["agent-auth"].SecretRef
	if auth == nil {
		return "", errors.New("deployment has no agent-auth Secret reference")
	}
	raw, err := (imageLiftCluster{app: worker}).Call(ctx, nil, "-n", record.Environment.Namespace, "get", "secret", auth.Name, "-o", fmt.Sprintf("go-template={{index .data %q}}", auth.Key))
	if err != nil {
		return "", errors.New("cannot read deployment endpoint authentication")
	}
	token, err := base64.StdEncoding.DecodeString(string(raw))
	if err != nil || len(token) == 0 {
		return "", errors.New("invalid endpoint authentication")
	}
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return "", err
	}
	port := fmt.Sprint(listener.Addr().(*net.TCPAddr).Port)
	listener.Close()
	forward, err := admin.StartForward(suiteForwardKube{worker}, record.Environment.Namespace, "deployment/"+binding.Name, port, "8080")
	if err != nil {
		return "", errors.New("deployment connection could not be established")
	}
	defer forward.Close()
	callCtx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	go func() {
		select {
		case <-forward.Done():
			cancel()
		case <-callCtx.Done():
		}
	}()
	model := "agent"
	if binding.Inference != nil {
		model = binding.Inference.Model
	}
	body, _ := json.Marshal(map[string]any{"model": model, "messages": []map[string]string{{"role": "user", "content": prompt}}})
	req, err := http.NewRequestWithContext(callCtx, "POST", "http://127.0.0.1:"+port+"/v1/chat/completions", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+string(token))
	transport := &http.Transport{Proxy: nil}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	response, err := client.Do(req)
	if err != nil {
		return "", errors.New("deployment invocation failed")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return "", fmt.Errorf("deployment invocation returned HTTP %d", response.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, (1<<20)+1))
	if err != nil || len(data) > 1<<20 {
		return "", errors.New("deployment answer unreadable or exceeds limit")
	}
	var answer struct {
		Choices []struct{ Message struct{ Content string } }
	}
	if json.Unmarshal(data, &answer) != nil || len(answer.Choices) != 1 {
		return "", errors.New("invalid deployment answer")
	}
	return answer.Choices[0].Message.Content, nil
}

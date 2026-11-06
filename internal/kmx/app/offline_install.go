package app

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"reflect"
	"slices"
	"strings"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/fileaccess"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/guard"
	"go.yaml.in/yaml/v3"
)

type offlineImagePin struct {
	Repository  string                    `json:"repository"`
	IndexDigest string                    `json:"indexDigest"`
	Platforms   map[string]offlineNodePin `json:"platforms"`
}
type offlineInstallProfile struct {
	Chart struct {
		SHA256     string `json:"sha256"`
		Version    string `json:"version"`
		AppVersion string `json:"appVersion"`
		Source     *struct {
			Commit    string `json:"commit"`
			Directory string `json:"directory"`
			GitTree   string `json:"gitTree"`
		} `json:"source,omitempty"`
	} `json:"chart"`
	Artifacts struct {
		SHA256 string `json:"sha256"`
		Kind   string `json:"kind"`
	} `json:"artifacts"`
	Images map[string]offlineImagePin `json:"images"`
	Values map[string]offlineValues   `json:"values"`
}
type offlineEnabled struct {
	Enabled bool `json:"enabled"`
}
type offlineValueImage struct {
	Repository string `json:"repository"`
	Digest     string `json:"digest"`
	Tag        string `json:"tag"`
	PullPolicy string `json:"pullPolicy,omitempty"`
}
type offlineValueWorker struct {
	Image offlineValueImage `json:"image"`
}
type offlineValues struct {
	Labels           map[string]string `json:"labels"`
	FullnameOverride string            `json:"fullnameOverride"`
	KMX              *struct {
		Profile string `json:"offlineProfileSha256"`
	} `json:"kmx,omitempty"`
	Controller struct {
		Mode  string            `json:"mode"`
		Image offlineValueImage `json:"image"`
		// CRDs are verified locally and established before Helm. Pin the chart's
		// lookup override so local rendering and the installed arguments agree.
		Gateway struct {
			CRDsReadyOverride bool `json:"crdsReadyOverride"`
		} `json:"gateway"`
		ACPRuntime struct {
			Codex     string         `json:"codexImage"`
			Claude    string         `json:"claudeImage"`
			Copilot   string         `json:"copilotImage"`
			Opencode  string         `json:"opencodeImage"`
			Toolboxes offlineEnabled `json:"toolboxes"`
		} `json:"acpRuntime"`
		ExecutionWorkspace struct {
			DispatchEnabled bool `json:"dispatchEnabled"`
		} `json:"executionWorkspace"`
		Substrate struct {
			MCPToolsEnabled bool `json:"mcpToolsEnabled"`
		} `json:"substrate"`
		Connectors offlineEnabled `json:"connectors"`
		Dashboard  offlineEnabled `json:"dashboard"`
		AgentHosts struct {
			Enabled bool           `json:"enabled"`
			Github  offlineEnabled `json:"github"`
		} `json:"agentHosts"`
	} `json:"controller"`
	Workers struct {
		AI struct {
			Image       offlineValueImage `json:"image"`
			RBACProfile string            `json:"rbacProfile,omitempty"`
		} `json:"ai"`
		General       offlineValueWorker `json:"general"`
		SessionLoader offlineValueWorker `json:"sessionLoader"`
	} `json:"workers"`
	Publisher struct {
		Enabled bool              `json:"enabled"`
		Image   offlineValueImage `json:"image"`
	} `json:"publisher"`
	Store struct {
		Persistence offlineEnabled `json:"persistence"`
	} `json:"store"`
	SCMEgressProxy offlineEnabled `json:"scmEgressProxy"`
	ProviderProxy  offlineEnabled `json:"providerProxy"`
	Ingress        offlineEnabled `json:"ingress"`
}
type offlineInstallBundle struct {
	chart, values       []byte
	version, appVersion string
	images              []offlineImageRow
	diffIDs             map[string][]string
	workloads           map[string]offlineWorkload
}

func loadOfflineInstall(root *fileaccess.Directory, m offlineManifest, p offlineProfile) (*offlineInstallBundle, error) {
	pin := p.Install
	if pin == nil || !slices.Contains(p.Capabilities, "install") {
		return nil, fmt.Errorf("offline profile does not authorize install")
	}
	if m.Chart.SHA256 != pin.Chart.SHA256 || m.Artifacts.SHA256 != pin.Artifacts.SHA256 {
		return nil, fmt.Errorf("chart/artifacts do not equal compiled offline profile")
	}
	chart, err := readOfflineBounded(root, m.Chart.Path, 64<<20)
	if err != nil {
		return nil, err
	}
	if offlineHash(chart) != pin.Chart.SHA256 {
		return nil, fmt.Errorf("chart bytes differ from compiled offline profile")
	}
	if err = offlineChartIdentity(chart, pin.Chart.Version, pin.Chart.AppVersion); err != nil {
		return nil, err
	}
	crds, _, err := orkaChartCRDs(chart)
	if err != nil {
		return nil, err
	}
	dec := yaml.NewDecoder(bytes.NewReader(crds))
	for {
		var obj struct {
			Kind     string
			Metadata struct{ Name string }
		}
		err = dec.Decode(&obj)
		if err == io.EOF {
			break
		}
		if err == nil && obj.Kind == "" && obj.Metadata.Name == "" {
			continue
		}
		if err != nil || obj.Kind != "CustomResourceDefinition" || obj.Metadata.Name == "" {
			return nil, fmt.Errorf("invalid offline chart CRDs")
		}
	}
	raw, err := readOfflineFile(root, m.Artifacts.Path)
	if err != nil {
		return nil, err
	}
	if offlineHash(raw) != pin.Artifacts.SHA256 || pin.Artifacts.Kind != "images.json" {
		return nil, fmt.Errorf("artifact bytes/kind differ from compiled offline profile")
	}
	var descriptor struct {
		SourceSHA string            `json:"sourceSHA"`
		Images    map[string]string `json:"images"`
	}
	if offlineJSON(raw, &descriptor, true) != nil || descriptor.SourceSHA != p.Orka.Identity || len(descriptor.Images) != 8 {
		return nil, fmt.Errorf("offline artifact source/image map mismatch")
	}
	for _, name := range strings.Fields("acp-codex-runtime acp-claude-runtime acp-copilot-runtime acp-opencode-runtime") {
		ref := descriptor.Images[name]
		prefix := "ghcr.io/orka-agents/orka/" + name + "@"
		if !strings.HasPrefix(ref, prefix) || !offlineDigest(strings.TrimPrefix(ref, prefix)) {
			return nil, fmt.Errorf("offline disabled artifact provenance mismatch")
		}
	}
	bundle := &offlineInstallBundle{chart: chart, version: pin.Chart.Version, appVersion: pin.Chart.AppVersion, images: m.Images, diffIDs: map[string][]string{}}
	for _, row := range m.Images {
		image, ok := pin.Images[row.Component]
		child := image.Platforms[m.Platform.Architecture]
		if !ok || row.Repository != image.Repository || row.IndexDigest == nil || *row.IndexDigest != image.IndexDigest || row.ManifestDigest != child.ManifestDigest || row.ConfigDigest != child.ConfigDigest {
			return nil, fmt.Errorf("image %s differs from compiled offline profile", row.Component)
		}
		key := row.Component
		if key == "publisher" {
			key = "workspace-publisher"
		}
		if key != "session-loader" && descriptor.Images[key] != image.Repository+"@"+image.IndexDigest {
			return nil, fmt.Errorf("image %s artifact parent mismatch", row.Component)
		}
		index, e := readOfflineFile(root, *row.IndexPath)
		if e != nil {
			return nil, fmt.Errorf("image %s index: %w", key, e)
		}
		manifest, e := readOfflineFile(root, row.ManifestPath)
		if e != nil {
			return nil, fmt.Errorf("image %s manifest: %w", key, e)
		}
		config, e := readOfflineFile(root, row.ConfigPath)
		if e != nil {
			return nil, fmt.Errorf("image %s config: %w", key, e)
		}
		ids, e := validateOfflineOCI(offlineNodeRow{IndexDigest: row.IndexDigest, ManifestDigest: row.ManifestDigest, ConfigDigest: row.ConfigDigest}, m.Platform, index, manifest, config)
		if e != nil {
			return nil, fmt.Errorf("image %s: %w", key, e)
		}
		bundle.diffIDs[row.Component] = ids
	}
	values, ok := pin.Values[m.Platform.Architecture]
	if !ok {
		return nil, fmt.Errorf("offline values platform missing")
	}
	// The marker is added only after hashing catalog bytes: never a hash cycle.
	values.KMX = &struct {
		Profile string `json:"offlineProfileSha256"`
	}{m.ProfileSHA256}
	bundle.values, err = json.Marshal(values)
	return bundle, err
}

func offlineChartIdentity(chart []byte, version, appVersion string) error {
	zr, err := gzip.NewReader(bytes.NewReader(chart))
	if err != nil {
		return fmt.Errorf("invalid offline chart")
	}
	defer zr.Close()
	tr := tar.NewReader(zr)
	count := 0
	var total int64
	for {
		h, e := tr.Next()
		if e == io.EOF {
			break
		}
		if e != nil {
			return fmt.Errorf("invalid offline chart archive")
		}
		total += h.Size
		if h.Size < 0 || total > 64<<20 {
			return fmt.Errorf("offline chart archive exceeds limit")
		}
		if h.Name != "orka/Chart.yaml" {
			continue
		}
		count++
		var meta struct {
			Name         string `yaml:"name"`
			Version      string `yaml:"version"`
			AppVersion   string `yaml:"appVersion"`
			Dependencies []any  `yaml:"dependencies"`
		}
		if h.Typeflag != tar.TypeReg || h.Size > offlineJSONLimit || yaml.NewDecoder(io.LimitReader(tr, h.Size)).Decode(&meta) != nil || meta.Name != "orka" || meta.Version != version || meta.AppVersion != appVersion || len(meta.Dependencies) != 0 {
			return fmt.Errorf("offline chart metadata/dependencies mismatch")
		}
	}
	if count != 1 {
		return fmt.Errorf("offline chart metadata missing or duplicate")
	}
	return nil
}

func (a *App) installOfflineOrka(opt OrkaOptions) error {
	started := a.timeNow()
	if err := a.operationContext().Err(); err != nil {
		return err
	}
	if err := a.validateKindTarget(); err != nil {
		return err
	}
	resolve := runningOfflineIdentity
	if a.offlineIdentity != nil {
		resolve = a.offlineIdentity
	}
	id, err := resolve()
	if err != nil {
		return err
	}
	b, err := loadOfflineInstallBundle(opt.OfflineBundle, id)
	if err != nil {
		return err
	}
	a, err = a.offlineTarget()
	if err != nil {
		return err
	}
	a = a.offlineHelmTarget()
	for _, dep := range []dependency{depKind, depKubectl, a.engineDependency(), depHelm} {
		if _, err := exec.LookPath(dep.name); err != nil {
			return fmt.Errorf("offline install: %s is not on PATH", dep.name)
		}
		if _, err := a.offlineCapture(dep.name, dep.probe...); err != nil {
			return err
		}
	}
	if err := a.verifyOfflineNodeImage(b, b.node.LocalRef); err != nil {
		return err
	}
	if err := a.renderOfflineChart(b.install); err != nil {
		return err
	}
	if opt.NoApply {
		a.notef("--no-apply: local artifacts, render and outer cache verified; target cache, ownership and readiness unchecked. Nothing written.")
		return nil
	}
	if err := a.offlineInstallCache(b); err != nil {
		return err
	}
	if opt.DryRun {
		if _, err := a.orkaInstallStateFor(b.install); err != nil {
			return err
		}
		crds, _, err := orkaChartCRDs(b.install.chart)
		if err != nil {
			return err
		}
		if err := a.applyOrkaCRDsFor(crds, true, true); err != nil {
			return err
		}
		a.notef("Validated CRDs only; no Helm install, readiness check or result-reader reconciliation. Nothing written.")
		return nil
	}
	if err := a.applyOrkaChartFor(b.install.chart, b.install); err != nil {
		return err
	}
	// Retain the existing grant manifest, but suppress child-controlled output.
	local, runner := *a, *a.Run
	runner.Echo = false
	runner.Stdout, runner.Stderr = io.Discard, io.Discard
	local.Run = &runner
	if err := local.orkaResultReader(); err != nil {
		return err
	}
	a.complete("Native offline Orka is running", started)
	return nil
}

// Remove both inherited and explicitly copied competitors; credentials and
// KUBECONFIG still come from the already selected normal kubeconfig loader.
func (a *App) offlineHelmTarget() *App {
	clear := strings.Fields("HELM_KUBECONTEXT HELM_NAMESPACE HELM_KUBEAPISERVER HELM_KUBECAFILE HELM_KUBEINSECURE_SKIP_TLS_VERIFY HELM_KUBETLS_SERVER_NAME HELM_KUBETOKEN HELM_KUBEASUSER HELM_KUBEASGROUPS HELM_DRIVER HELM_DRIVER_SQL_CONNECTION_STRING")
	local, runner := *a, *a.Run
	runner.Env = nil
	for _, entry := range a.Run.Env {
		key, _, _ := strings.Cut(entry, "=")
		if !slices.Contains(clear, key) {
			runner.Env = append(runner.Env, entry)
		}
	}
	runner.Unset = append(append([]string(nil), a.Run.Unset...), clear...)
	runner.Env = append(runner.Env, "HELM_DRIVER=secret")
	local.Run = &runner
	return &local
}

func (a *App) offlineInput(input []byte, tool string, args ...string) ([]byte, error) {
	if err := a.operationContext().Err(); err != nil {
		return nil, err
	}
	cmd := a.Run.Command(tool, args...)
	cmd.Stdin = bytes.NewReader(input)
	var out offlineOutput
	cmd.Stdout = &out
	cmd.Stderr = io.Discard
	if err := cmd.Run(); err != nil {
		if cancelled := a.operationContext().Err(); cancelled != nil {
			return nil, cancelled
		}
		return nil, fmt.Errorf("%s offline phase refused; existing effects retained (inspect target privately)", tool)
	}
	return out.buffer.Bytes(), nil
}

func offlineChartFile(chart []byte) (string, func(), error) {
	file, err := os.CreateTemp("", "kmx-offline-orka-*.tgz")
	if err != nil {
		return "", nil, err
	}
	cleanup := func() { _ = os.Remove(file.Name()) }
	if _, err = file.Write(chart); err != nil {
		file.Close()
		cleanup()
		return "", nil, err
	}
	if err = file.Close(); err != nil {
		cleanup()
		return "", nil, err
	}
	return file.Name(), cleanup, nil
}
func (a *App) renderOfflineChart(b *offlineInstallBundle) error {
	path, cleanup, err := offlineChartFile(b.chart)
	if err != nil {
		return err
	}
	defer cleanup()
	raw, err := a.offlineInput(b.values, "helm", "template", orkaRelease, path, "-n", OrkaNamespace, "--kube-context", a.Cfg.KubeContext, "-f", "-")
	if err != nil {
		return fmt.Errorf("offline chart render: %w", err)
	}
	b.workloads, err = offlineRenderAudit(raw, b.images)
	return err
}
func offlineRenderAudit(raw []byte, images []offlineImageRow) (map[string]offlineWorkload, error) {
	expected := map[string]struct{ container, component string }{"orka-api-controller": {"controller", "controller"}, "orka-api-workspace-publisher": {"publisher", "publisher"}, "orka-api-scm-egress-proxy": {"proxy", "controller"}}
	refs := map[string]string{}
	for _, r := range images {
		refs[r.Component] = r.Repository + "@" + r.ManifestDigest
	}
	result := map[string]offlineWorkload{}
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	for {
		var obj map[string]any
		err := dec.Decode(&obj)
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("offline chart render is invalid")
		}
		if len(obj) == 0 {
			continue
		}
		// An image in any unexpected object, including init/helper workloads, refuses.
		var imageCount func(any) int
		imageCount = func(v any) int {
			n := 0
			switch x := v.(type) {
			case map[string]any:
				for k, v := range x {
					if k == "image" {
						n++
					}
					n += imageCount(v)
				}
			case []any:
				for _, v := range x {
					n += imageCount(v)
				}
			}
			return n
		}
		count := imageCount(obj)
		if count == 0 {
			continue
		}
		if count != 1 {
			return nil, fmt.Errorf("offline render has unaccounted images")
		}
		body, _ := json.Marshal(obj)
		var d struct {
			Kind     string
			Metadata struct{ Name, Namespace string }
			Spec     struct {
				Template struct{ Spec offlineWorkload }
			}
		}
		if json.Unmarshal(body, &d) != nil {
			return nil, fmt.Errorf("offline workload decode refused")
		}
		exp, ok := expected[d.Metadata.Name]
		if !ok || d.Kind != "Deployment" || d.Metadata.Namespace != "" && d.Metadata.Namespace != OrkaNamespace || result[d.Metadata.Name].Containers != nil || len(d.Spec.Template.Spec.Containers) != 1 || len(d.Spec.Template.Spec.InitContainers) != 0 {
			return nil, fmt.Errorf("offline render has unexpected image-bearing workload")
		}
		c := d.Spec.Template.Spec.Containers[0]
		if c.Name != exp.container || c.Image != refs[exp.component] || c.PullPolicy != "IfNotPresent" || len(c.EnvFrom) != 0 {
			return nil, fmt.Errorf("offline render image/container/policy mismatch")
		}
		if exp.component == "controller" && c.Name == "controller" {
			for _, component := range []string{"ai-worker", "general-worker", "session-loader"} {
				count := 0
				prefix := component + "-image"
				for _, arg := range c.Args {
					flag := strings.TrimLeft(arg, "-")
					if flag == prefix || strings.HasPrefix(flag, prefix+"=") {
						count++
						if arg != "--"+prefix+"="+refs[component] {
							return nil, fmt.Errorf("offline render dynamic image mismatch")
						}
					}
				}
				if count != 1 {
					return nil, fmt.Errorf("offline render requires unique %s image flag", component)
				}
			}
			for _, arg := range c.Args {
				flag := strings.TrimLeft(arg, "-")
				for _, disabled := range strings.Fields("acp-toolboxes-enabled acp-workspace-dispatch-enabled substrate-mcp-tools-enabled connectors-enabled dashboard-enabled enable-agent-hosts agent-host-agent-image acp-provider-proxy-base-url") {
					if flag == disabled || strings.HasPrefix(flag, disabled+"=") {
						return nil, fmt.Errorf("offline render enables disabled component")
					}
				}
			}
			for _, name := range []string{"codex", "claude", "copilot", "opencode"} {
				for _, arg := range c.Args {
					if strings.HasPrefix(strings.TrimLeft(arg, "-"), "acp-"+name+"-runtime-image") {
						return nil, fmt.Errorf("offline render enables disabled ACP runtime")
					}
				}
				for _, env := range c.Env {
					entry, _ := env.(map[string]any)
					key, _ := entry["name"].(string)
					if key == "ORKA_ACP_"+strings.ToUpper(name)+"_RUNTIME_IMAGE" {
						if entry["valueFrom"] != nil || entry["value"] != nil && entry["value"] != "" {
							return nil, fmt.Errorf("offline render enables disabled ACP environment")
						}
					}
				}
			}
		}
		result[d.Metadata.Name] = d.Spec.Template.Spec
	}
	if len(result) != len(expected) {
		return nil, fmt.Errorf("offline render workload set is incomplete")
	}
	return result, nil
}

// Consent uses the proven context but never the cluster-creation allowance.
func (a *App) offlineInstallGuard() error {
	cfg, err := a.offlineKubeconfig()
	if err != nil {
		return err
	}
	command := a.operationCommand("cluster", "orka", "install")
	if a.InvocationCommand != "" {
		command = a.InvocationCommand
	}
	return guard.CheckContext(a.operationContext(), cfg, guard.Request{Action: "install native offline Orka", Context: a.Cfg.KubeContext, Source: a.Cfg.ContextSource, Namespaces: OrkaNamespace, Confirm: a.Cfg.Confirm, Command: command, MustBeKnown: true}, a.Err, a.Stdin)
}

// The projection excludes generated Secret bytes and compares only execution
// inputs. Kubernetes defaults outside this projection do not change identity.
type offlineContainer struct {
	Name         string   `json:"name" yaml:"name"`
	Image        string   `json:"image" yaml:"image"`
	PullPolicy   string   `json:"imagePullPolicy" yaml:"imagePullPolicy"`
	Command      []string `json:"command,omitempty" yaml:"command,omitempty"`
	Args         []string `json:"args,omitempty" yaml:"args,omitempty"`
	Env          []any    `json:"env,omitempty" yaml:"env,omitempty"`
	EnvFrom      []any    `json:"envFrom,omitempty" yaml:"envFrom,omitempty"`
	VolumeMounts []any    `json:"volumeMounts,omitempty" yaml:"volumeMounts,omitempty"`
}
type offlineWorkload struct {
	Containers         []offlineContainer `json:"containers" yaml:"containers"`
	InitContainers     []offlineContainer `json:"initContainers,omitempty" yaml:"initContainers,omitempty"`
	Volumes            []any              `json:"volumes,omitempty" yaml:"volumes,omitempty"`
	ServiceAccountName string             `json:"serviceAccountName" yaml:"serviceAccountName"`
}

func offlineSameValues(raw, expected []byte) bool {
	var got, want any
	return offlineJSON(raw, &got, false) == nil && json.Unmarshal(expected, &want) == nil && reflect.DeepEqual(got, want)
}

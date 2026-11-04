package app

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"go.yaml.in/yaml/v3"
)

const (
	orkaRelease    = "orka"
	orkaChartLabel = "kmx.kaimahi.dev/installed"
	// Digests are from v0.3.0 candidate.json, not tag-to-digest guesses.
	orkaControllerDigest    = "sha256:84cdbd635c61169f46acef1b7b4003e253c4f7cd920519577634e834776535c5"
	orkaAIWorkerDigest      = "sha256:7777385848bf8382bcc66b570ac23414f39be375e4c7f2b4f194de079ae82bb4"
	orkaGeneralWorkerDigest = "sha256:5134787670e53d283d22e448e4a3ab5f6c67460e95b152673d6d764247709274"
	orkaPublisherDigest     = "sha256:f8414ac9fb528784eaf40791b5ae49f93a77082bc85b705d50bb8c1b8fa01a25"
	orkaCodexImage          = "ghcr.io/orka-agents/orka/acp-codex-runtime@sha256:549486a886249d4fcb7ee37664fa94b400fd6ec9b9d90a174f125eaaf30bdf39"
	orkaClaudeImage         = "ghcr.io/orka-agents/orka/acp-claude-runtime@sha256:96a683ff62ec527ea886b7fa6389d16ecc528f9b94a00fb898d52ee7f92a01e0"
	orkaCopilotImage        = "ghcr.io/orka-agents/orka/acp-copilot-runtime@sha256:d921c217d9a58a888dab70422fbbfd85408efaf97c59493acdaa6b105bc461e1"
	orkaOpencodeImage       = "ghcr.io/orka-agents/orka/acp-opencode-runtime@sha256:ec94fbf8e257400c148f0c64b29ed501723456c66e1d4d46abe253e085358414"
)

type orkaHelmImage struct {
	Digest string `json:"digest"`
}
type orkaHelmWorker struct {
	Image orkaHelmImage `json:"image"`
}

type orkaHelmValues struct {
	Labels           map[string]string `json:"labels"`
	FullnameOverride string            `json:"fullnameOverride"`
	Controller       struct {
		Mode       string        `json:"mode"`
		Image      orkaHelmImage `json:"image"`
		ACPRuntime struct {
			CodexImage    string `json:"codexImage"`
			ClaudeImage   string `json:"claudeImage"`
			CopilotImage  string `json:"copilotImage"`
			OpencodeImage string `json:"opencodeImage"`
		} `json:"acpRuntime"`
	} `json:"controller"`
	Workers struct {
		AI      orkaHelmWorker `json:"ai"`
		General orkaHelmWorker `json:"general"`
	} `json:"workers"`
	Publisher orkaHelmWorker `json:"publisher"`
}

// Installation refusals remain actionable without modifying cluster state.
// `local down` is for a disposable local kind cluster, never a production shortcut.
func (a *App) orkaInstallRecovery() string {
	return "Local kind cluster you own: `" + a.operationCommand("cluster", "down") + "` then `" + a.operationCommand("cluster", "up") + "` replaces it and loses Tasks, Secrets, PVC-backed volumes, model data and the plane ledger. " +
		"AKS: back up Orka resources, Secrets, volumes/PVCs and any snapshot key without printing it; verify the recovery plan before an operator-managed fresh install (docs/orka.md)."
}

// Helm does not upgrade CRDs. Use only CRDs extracted from the verified chart,
// before installing it, so Gateway discovery sees the matching Task schema.
func orkaChartCRDs(chart []byte) ([]byte, int, error) {
	zr, err := gzip.NewReader(bytes.NewReader(chart))
	if err != nil {
		return nil, 0, fmt.Errorf("read Orka chart: %w", err)
	}
	defer zr.Close()
	tr := tar.NewReader(zr)
	var crds [][]byte
	var total int64
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, 0, fmt.Errorf("read Orka chart: %w", err)
		}
		if !strings.HasPrefix(h.Name, "orka/crds/") || !strings.HasSuffix(h.Name, ".yaml") {
			continue
		}
		if h.Typeflag != tar.TypeReg || h.Size <= 0 || h.Size > 8<<20 || total+h.Size > 32<<20 {
			return nil, 0, fmt.Errorf("invalid Orka chart CRD %q", h.Name)
		}
		body, err := io.ReadAll(io.LimitReader(tr, h.Size+1))
		if err != nil || int64(len(body)) != h.Size {
			return nil, 0, fmt.Errorf("read Orka chart CRD %q: %v", h.Name, err)
		}
		crds = append(crds, body)
		total += h.Size
	}
	if len(crds) == 0 {
		return nil, 0, fmt.Errorf("verified Orka chart contains no CRDs")
	}
	return bytes.Join(crds, []byte("\n---\n")), len(crds), nil
}

// Oversized chart CRDs cannot carry client-side apply's last-applied annotation.
// Apply separately so a conflict identifies its CRD without forcing ownership.
func (a *App) applyOrkaCRDs(crds []byte, dryRun bool) error {
	return a.applyOrkaCRDsFor(crds, dryRun, false)
}
func (a *App) applyOrkaCRDsFor(crds []byte, dryRun, offline bool) error {
	decoder := yaml.NewDecoder(bytes.NewReader(crds))
	quiet := *a.Run
	quiet.Echo = false
	if dryRun {
		quiet.Stdout = io.Discard
	}
	for {
		var document yaml.Node
		if err := decoder.Decode(&document); err == io.EOF {
			return nil
		} else if err != nil {
			return fmt.Errorf("read Orka chart CRD: %w", err)
		}
		if len(document.Content) == 0 || document.Content[0].Tag == "!!null" {
			continue
		}
		var identity struct {
			Metadata struct {
				Name string `yaml:"name"`
			} `yaml:"metadata"`
		}
		if err := document.Decode(&identity); err != nil || identity.Metadata.Name == "" {
			return fmt.Errorf("Orka chart CRD has no readable name")
		}
		body, err := yaml.Marshal(&document)
		if err != nil {
			return fmt.Errorf("encode Orka CRD %s: %w", identity.Metadata.Name, err)
		}
		args := []string{"apply", "--server-side", "--field-manager=kmx"}
		if dryRun {
			args = append(args, "--dry-run=server")
		}
		args = append(args, "-f", "-")
		var applyErr error
		if offline {
			_, applyErr = a.offlineInput(body, "kubectl", a.kubectl(args...)...)
		} else {
			applyErr = quiet.RunStdin(body, "kubectl", a.kubectl(args...)...)
		}
		if err := applyErr; err != nil {
			return fmt.Errorf("applying Orka CRD %s refused: %w. %s", identity.Metadata.Name, err, a.orkaInstallRecovery())
		}
	}
}

// A tag alone does not prove kmx owns a release. Only its chart version,
// release status, explicit harness mode and marker together allow a repeat.
func (a *App) orkaInstallState() (bool, error) { return a.orkaInstallStateFor(nil) }
func (a *App) orkaInstallStateFor(offline *offlineInstallBundle) (bool, error) {
	capture := a.Run.Capture
	if offline != nil {
		capture = func(tool string, args ...string) (string, error) {
			raw, err := a.offlineCapture(tool, args...)
			return string(raw), err
		}
	}
	kubecapture := func(args ...string) (string, error) { return capture("kubectl", a.kubectl(args...)...) }
	listArgs := []string{"list", "-n", OrkaNamespace, "--kube-context", a.Cfg.KubeContext,
		"-o", "json", "-f", "^" + orkaRelease + "$",
		// Helm 3's --all was removed in Helm 4. These filters are shared by
		// both versions and keep partial/history releases visible.
		"--deployed", "--failed", "--pending", "--uninstalling", "--superseded", "--uninstalled"}
	raw, err := capture("helm", listArgs...)
	if err != nil {
		return false, fmt.Errorf("cannot inspect Orka Helm releases: %w", err)
	}
	var releases []struct {
		Name       string `json:"name"`
		Chart      string `json:"chart"`
		AppVersion string `json:"app_version"`
		Status     string `json:"status"`
	}
	if offline != nil && (!strings.HasPrefix(strings.TrimSpace(raw), "[") || offlineJSON([]byte(raw), &releases, false) != nil) {
		return false, fmt.Errorf("offline Orka Helm release list is unknown or ambiguous; refusing to guess absence")
	}
	if err := json.Unmarshal([]byte(raw), &releases); err != nil {
		return false, fmt.Errorf("invalid Orka Helm release list: %w", err)
	}
	if offline != nil && len(releases) != 0 {
		if len(releases) != 1 || releases[0].Name != orkaRelease || releases[0].Chart != "orka-"+offline.version || releases[0].AppVersion != offline.appVersion || releases[0].Status != "deployed" {
			return false, fmt.Errorf("offline Orka release identity/status differs; refusing adoption, overwrite or partial retry")
		}
		values, err := capture("helm", "get", "values", orkaRelease, "-n", OrkaNamespace, "--kube-context", a.Cfg.KubeContext, "-o", "json")
		if err != nil || !offlineSameValues([]byte(values), offline.values) {
			return false, fmt.Errorf("offline Orka custom values/profile differ; refusing overwrite")
		}
		if err := a.verifyOfflineWorkloads(offline, false); err != nil {
			return false, err
		}
		return true, nil
	}
	if len(releases) == 1 && releases[0].Name == orkaRelease && releases[0].Chart == "orka-0.3.0" && strings.TrimPrefix(releases[0].AppVersion, "v") == "0.3.0" {
		values, err := capture("helm", "get", "values", orkaRelease, "-n", OrkaNamespace, "--kube-context", a.Cfg.KubeContext, "-o", "json")
		if err != nil {
			// Helm stderr can include chart-controlled text. Refuse without
			// repeating it or guessing whether this release is kmx-owned.
			return false, fmt.Errorf("cannot inspect existing Orka chart values; refusing to adopt or retry the release. Inspect `helm --kube-context %s -n %s status %s` before cleanup. %s", shellArg(a.Cfg.KubeContext), OrkaNamespace, orkaRelease, a.orkaInstallRecovery())
		}
		var installed orkaHelmValues
		if err := json.Unmarshal([]byte(values), &installed); err != nil {
			return false, fmt.Errorf("invalid Orka chart values: %w", err)
		}
		if installed.Labels[orkaChartLabel] == "true" && installed.Controller.Mode == "harness-v2" && installed.FullnameOverride == "orka-api" {
			for _, image := range []struct{ value, pinned string }{
				{installed.Controller.Image.Digest, orkaControllerDigest},
				{installed.Workers.AI.Image.Digest, orkaAIWorkerDigest},
				{installed.Workers.General.Image.Digest, orkaGeneralWorkerDigest},
				{installed.Publisher.Image.Digest, orkaPublisherDigest},
				{installed.Controller.ACPRuntime.CodexImage, orkaCodexImage},
				{installed.Controller.ACPRuntime.ClaudeImage, orkaClaudeImage},
				{installed.Controller.ACPRuntime.CopilotImage, orkaCopilotImage},
				{installed.Controller.ACPRuntime.OpencodeImage, orkaOpencodeImage},
			} {
				if image.value != image.pinned {
					return false, fmt.Errorf("kmx-owned Orka Helm release has changed or missing pinned image values; refusing to adopt or overwrite it. %s", a.orkaInstallRecovery())
				}
			}
			if releases[0].Status == "deployed" {
				deployments, err := a.orkaDeployments(a.operationContext())
				if err != nil {
					return false, fmt.Errorf("cannot verify the live Orka controller image: %w", err)
				}
				controller, _, err := selectOrkaController(deployments)
				if err != nil {
					return false, fmt.Errorf("cannot verify the live Orka controller image: %w", err)
				}
				matches := 0
				for _, container := range controller.Spec.Template.Spec.Containers {
					if container.Name != "controller" {
						continue
					}
					matches++
					if container.Image != "ghcr.io/orka-agents/orka@"+orkaControllerDigest {
						return false, fmt.Errorf("live Orka controller image differs from kmx's pinned digest; refusing to adopt the release. %s", a.orkaInstallRecovery())
					}
				}
				if matches != 1 {
					return false, fmt.Errorf("live Orka controller image cannot be identified; refusing to adopt the release. %s", a.orkaInstallRecovery())
				}
				return true, nil
			}
			status := releases[0].Status
			switch status {
			case "failed", "pending-install", "pending-upgrade", "pending-rollback", "uninstalling":
			default:
				status = "not deployed"
			}
			return false, fmt.Errorf("kmx-owned Orka Helm release is %s; refusing automatic retry over a partial installation. Inspect `helm --kube-context %s -n %s status %s` and Pod events. After resolving any pending operation, an operator may explicitly retry the same SHA-256-verified chart with `helm --kube-context %s -n %s upgrade %s <verified-orka-0.3.0.tgz> --reuse-values --wait` (never --force), or clean the target before retrying kmx. %s", status, shellArg(a.Cfg.KubeContext), OrkaNamespace, orkaRelease, shellArg(a.Cfg.KubeContext), OrkaNamespace, orkaRelease, a.orkaInstallRecovery())
		}
		status := releases[0].Status
		switch status {
		case "deployed", "failed", "pending-install", "pending-upgrade", "pending-rollback", "uninstalling", "superseded", "uninstalled":
		default:
			status = "an unrecognized state"
		}
		return false, fmt.Errorf("existing Orka %s Helm release is %s but lacks KMX's exact ownership marker or harness-v2/fullname configuration; refusing to adopt or overwrite it. Inspect `helm --kube-context %s -n %s get values %s` and `helm --kube-context %s -n %s status %s` before choosing an operator-managed repair or cleanup. %s", OrkaVersion, status, shellArg(a.Cfg.KubeContext), OrkaNamespace, orkaRelease, shellArg(a.Cfg.KubeContext), OrkaNamespace, orkaRelease, a.orkaInstallRecovery())
	}
	if len(releases) != 0 {
		return false, fmt.Errorf("existing Orka Helm release is not kmx's %s harness-v2 chart; refusing to overwrite it. %s", OrkaVersion, a.orkaInstallRecovery())
	}
	// An old kubectl install has no Helm release. Check both namespaced
	// resources and cluster-scoped CRDs; an empty Deployment list alone is
	// not evidence of a fresh installation.
	refuse := func(name, found string, err error) error {
		if err != nil {
			return fmt.Errorf("cannot inspect existing Orka resources: %w", err)
		}
		if strings.TrimSpace(found) != "" {
			return fmt.Errorf("existing Orka resources (%s) without a kmx-owned Helm release; refusing to overwrite v0.1.3 or a foreign installation. %s", name, a.orkaInstallRecovery())
		}
		return nil
	}
	deploy, err := kubecapture("-n", OrkaNamespace, "get", "deployment", "--ignore-not-found=true", "-o", "name")
	if isNotFound(err) {
		err = nil
	} // namespace absent
	if err := refuse("deployments", deploy, err); err != nil {
		return false, err
	}
	crds, err := kubecapture("get", "crd", "-o", "jsonpath={range .items[*]}{.metadata.name}{\" \"}{end}")
	if err != nil {
		return false, fmt.Errorf("cannot inspect existing cluster CRDs: %w", err)
	}
	for _, name := range strings.Fields(crds) {
		if strings.HasSuffix(name, ".orka.ai") {
			if offline != nil {
				return false, fmt.Errorf("existing Orka CRDs without an owned offline release; refusing orphan adoption or overwrite")
			}
			return false, fmt.Errorf("existing Orka CRDs (%s) without a kmx-owned Helm release; refusing to overwrite v0.1.3 or a foreign installation. %s", name, a.orkaInstallRecovery())
		}
	}
	return false, nil
}

func (a *App) applyOrkaChart(chart []byte) error { return a.applyOrkaChartFor(chart, nil) }
func (a *App) applyOrkaChartFor(chart []byte, offline *offlineInstallBundle) error {
	known, err := a.orkaInstallStateFor(offline)
	if err != nil {
		return err
	}
	if known {
		if offline != nil {
			return a.verifyOfflineWorkloads(offline, true)
		}
		if err := a.OrkaReady(); err != nil {
			return fmt.Errorf("kmx-owned Orka %s is not ready; no reinstall attempted: %w", OrkaVersion, err)
		}
		a.notef("Orka %s harness-v2 is already installed by kmx; keeping its chart, data and snapshot key.", OrkaVersion)
		return nil
	}
	crds, count, err := orkaChartCRDs(chart)
	if err != nil {
		return err
	}
	file, err := os.CreateTemp("", "kmx-orka-*.tgz")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	if err := file.Chmod(0o600); err != nil {
		file.Close()
		return err
	}
	if _, err := file.Write(chart); err != nil {
		file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	quiet := *a.Run
	quiet.Echo = false
	fmt.Fprintf(a.Err, "kubectl --context %s apply --server-side --field-manager=kmx -f - # (%d CRDs, applied separately)\n", a.Cfg.KubeContext, count)
	if err := a.applyOrkaCRDsFor(crds, false, offline != nil); err != nil {
		return err
	}
	var waitErr error
	if offline != nil {
		_, waitErr = a.offlineInput(crds, "kubectl", a.kubectl("wait", "--for=condition=Established", "--timeout=120s", "-f", "-")...)
	} else {
		waitErr = quiet.RunStdin(crds, "kubectl", a.kubectl("wait", "--for=condition=Established", "--timeout=120s", "-f", "-")...)
	}
	if err := waitErr; err != nil {
		return fmt.Errorf("waiting for Orka CRDs before Gateway discovery: %w", err)
	}
	// Helm output is intentionally suppressed: chart hooks and NOTES are not
	// an appropriate place to expose generated Secret values in kmx logs.
	quiet.Stdout, quiet.Stderr = io.Discard, io.Discard
	fmt.Fprintf(a.Err, "helm install orka <verified chart> -n %s --kube-context %s --wait # (harness-v2)\n", OrkaNamespace, a.Cfg.KubeContext)
	args := []string{"install", orkaRelease, file.Name(), "-n", OrkaNamespace,
		"--kube-context", a.Cfg.KubeContext, "--create-namespace", "--wait", "--timeout", "10m",
		"--set", "controller.mode=harness-v2", "--set", "fullnameOverride=orka-api",
		"--set-string", "labels.kmx\\.kaimahi\\.dev/installed=true",
		"--set-string", "controller.image.digest=" + orkaControllerDigest,
		"--set-string", "workers.ai.image.digest=" + orkaAIWorkerDigest,
		"--set-string", "workers.general.image.digest=" + orkaGeneralWorkerDigest,
		"--set-string", "publisher.image.digest=" + orkaPublisherDigest,
		"--set-string", "controller.acpRuntime.codexImage=" + orkaCodexImage,
		"--set-string", "controller.acpRuntime.claudeImage=" + orkaClaudeImage,
		"--set-string", "controller.acpRuntime.copilotImage=" + orkaCopilotImage,
		"--set-string", "controller.acpRuntime.opencodeImage=" + orkaOpencodeImage}
	var installErr error
	if offline != nil {
		_, installErr = a.offlineInput(offline.values, "helm", "install", orkaRelease, file.Name(), "-n", OrkaNamespace, "--kube-context", a.Cfg.KubeContext, "--create-namespace", "--wait", "--timeout", "10m", "-f", "-")
	} else {
		installErr = quiet.Run("helm", args...)
	}
	if err := installErr; err != nil {
		return fmt.Errorf("installing pinned Orka chart: %w\n  A failed release and CRDs may remain; kmx refuses to silently reinstall over them. Inspect without exposing Secrets:\n  helm --kube-context %s -n %s status %s\n  kubectl --context %s -n %s get pods,events", err, shellArg(a.Cfg.KubeContext), OrkaNamespace, orkaRelease, shellArg(a.Cfg.KubeContext), OrkaNamespace)
	}
	if offline != nil {
		return a.verifyOfflineWorkloads(offline, true)
	}
	return nil
}

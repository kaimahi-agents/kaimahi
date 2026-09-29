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
)

const (
	orkaRelease    = "orka"
	orkaChartLabel = "kmx.kaimahi.ai/installed"
	// Digests are from v0.2.0 candidate.json, not tag-to-digest guesses.
	orkaControllerDigest    = "sha256:7c1727f92d5c0cf05d464c6eb9e70b8342cb5a7a87c2e35338f051d7b85aa370"
	orkaAIWorkerDigest      = "sha256:814d7149308e62730df59759c5137667cad04520c94cc50ff56e5028fdc8bcd4"
	orkaGeneralWorkerDigest = "sha256:931ad167b916ee83ba1a2d4ba2ca275fd57922bc45a944e8ca84f136525a37fb"
	orkaPublisherDigest     = "sha256:596a074bac827e65c8d8813a6f4ac1ebac2bc130632bf0a185db6d521a1b1243"
	orkaCodexImage          = "ghcr.io/orka-agents/orka/acp-codex-runtime@sha256:1f3f52eaa2c17403219f595f99bfad2e4d2d66f861921357fbd939825fc113ea"
	orkaClaudeImage         = "ghcr.io/orka-agents/orka/acp-claude-runtime@sha256:ec8b51083626c14d1dd206fc6a57ecd6f5aeabf10522fc665903fdb205543e92"
	orkaCopilotImage        = "ghcr.io/orka-agents/orka/acp-copilot-runtime@sha256:32983da321bb03ef57eb80508454117a0142485782e28e935297dcd9234133ab"
	orkaOpencodeImage       = "ghcr.io/orka-agents/orka/acp-opencode-runtime@sha256:3d8e84b811834d768785055fef63ea1f1179eb7cb0b0bc1856bbf81023c48f9a"
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
// `down` is for a disposable local kind cluster, never a production shortcut.
func (a *App) orkaInstallRecovery() string {
	return "Local kind cluster you own: `kmx down` then `kmx up` replaces it and loses Tasks, Secrets, PVC-backed volumes, model data and the plane ledger. " +
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

// A tag alone does not prove kmx owns a release. Only its chart version,
// release status, explicit harness mode and marker together allow a repeat.
func (a *App) orkaInstallState() (bool, error) {
	raw, err := a.Run.Capture("helm", "list", "-n", OrkaNamespace, "--kube-context", a.Cfg.KubeContext,
		"-o", "json", "-f", "^"+orkaRelease+"$", "--all")
	if err != nil {
		return false, fmt.Errorf("cannot inspect Orka Helm releases: %w", err)
	}
	var releases []struct {
		Name       string `json:"name"`
		Chart      string `json:"chart"`
		AppVersion string `json:"app_version"`
		Status     string `json:"status"`
	}
	if err := json.Unmarshal([]byte(raw), &releases); err != nil {
		return false, fmt.Errorf("invalid Orka Helm release list: %w", err)
	}
	if len(releases) == 1 && releases[0].Name == orkaRelease && releases[0].Chart == "orka-0.2.0" && strings.TrimPrefix(releases[0].AppVersion, "v") == "0.2.0" {
		values, err := a.Run.Capture("helm", "get", "values", orkaRelease, "-n", OrkaNamespace, "--kube-context", a.Cfg.KubeContext, "-o", "json")
		if err != nil {
			// Helm stderr can include chart-controlled text. Refuse without
			// repeating it or guessing whether this release is kmx-owned.
			return false, fmt.Errorf("cannot inspect existing Orka chart values; refusing to adopt or retry the release. Inspect `helm --kube-context %s -n %s status %s` before cleanup. %s", a.Cfg.KubeContext, OrkaNamespace, orkaRelease, a.orkaInstallRecovery())
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
				return true, nil
			}
			status := releases[0].Status
			switch status {
			case "failed", "pending-install", "pending-upgrade", "pending-rollback", "uninstalling":
			default:
				status = "not deployed"
			}
			return false, fmt.Errorf("kmx-owned Orka Helm release is %s; refusing automatic retry over a partial installation. Inspect `helm --kube-context %s -n %s status %s` and Pod events. After resolving any pending operation, an operator may explicitly retry the same SHA-256-verified chart with `helm --kube-context %s -n %s upgrade %s <verified-orka-0.2.0.tgz> --reuse-values --wait` (never --force), or clean the target before retrying kmx. %s", status, a.Cfg.KubeContext, OrkaNamespace, orkaRelease, a.Cfg.KubeContext, OrkaNamespace, orkaRelease, a.orkaInstallRecovery())
		}
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
	deploy, err := a.kubectlCapture("-n", OrkaNamespace, "get", "deployment", "--ignore-not-found=true", "-o", "name")
	if isNotFound(err) {
		err = nil
	} // namespace absent
	if err := refuse("deployments", deploy, err); err != nil {
		return false, err
	}
	crds, err := a.kubectlCapture("get", "crd", "-o", "jsonpath={range .items[*]}{.metadata.name}{\" \"}{end}")
	if err != nil {
		return false, fmt.Errorf("cannot inspect existing cluster CRDs: %w", err)
	}
	for _, name := range strings.Fields(crds) {
		if strings.HasSuffix(name, ".orka.ai") {
			return false, fmt.Errorf("existing Orka CRDs (%s) without a kmx-owned Helm release; refusing to overwrite v0.1.3 or a foreign installation. %s", name, a.orkaInstallRecovery())
		}
	}
	return false, nil
}

func (a *App) applyOrkaChart(chart []byte) error {
	known, err := a.orkaInstallState()
	if err != nil {
		return err
	}
	if known {
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
	fmt.Fprintf(a.Err, "kubectl --context %s apply -f - # (%d CRDs from verified Orka chart)\n", a.Cfg.KubeContext, count)
	if err := quiet.RunStdin(crds, "kubectl", a.kubectl("apply", "-f", "-")...); err != nil {
		return fmt.Errorf("applying Orka CRDs before chart: %w", err)
	}
	if err := quiet.RunStdin(crds, "kubectl", a.kubectl("wait", "--for=condition=Established", "--timeout=120s", "-f", "-")...); err != nil {
		return fmt.Errorf("waiting for Orka CRDs before Gateway discovery: %w", err)
	}
	// Helm output is intentionally suppressed: chart hooks and NOTES are not
	// an appropriate place to expose generated Secret values in kmx logs.
	quiet.Stdout, quiet.Stderr = io.Discard, io.Discard
	fmt.Fprintf(a.Err, "helm install orka <verified chart> -n %s --kube-context %s --wait # (harness-v2)\n", OrkaNamespace, a.Cfg.KubeContext)
	if err := quiet.Run("helm", "install", orkaRelease, file.Name(), "-n", OrkaNamespace,
		"--kube-context", a.Cfg.KubeContext, "--create-namespace", "--wait", "--timeout", "10m",
		"--set", "controller.mode=harness-v2", "--set", "fullnameOverride=orka-api",
		"--set-string", "labels.kmx\\.kaimahi\\.ai/installed=true",
		"--set-string", "controller.image.digest="+orkaControllerDigest,
		"--set-string", "workers.ai.image.digest="+orkaAIWorkerDigest,
		"--set-string", "workers.general.image.digest="+orkaGeneralWorkerDigest,
		"--set-string", "publisher.image.digest="+orkaPublisherDigest,
		"--set-string", "controller.acpRuntime.codexImage="+orkaCodexImage,
		"--set-string", "controller.acpRuntime.claudeImage="+orkaClaudeImage,
		"--set-string", "controller.acpRuntime.copilotImage="+orkaCopilotImage,
		"--set-string", "controller.acpRuntime.opencodeImage="+orkaOpencodeImage); err != nil {
		return fmt.Errorf("installing pinned Orka chart: %w\n  A failed release and CRDs may remain; kmx refuses to silently reinstall over them. Inspect without exposing Secrets:\n  helm --kube-context %s -n %s status %s\n  kubectl --context %s -n %s get pods,events", err, a.Cfg.KubeContext, OrkaNamespace, orkaRelease, a.Cfg.KubeContext, OrkaNamespace)
	}
	return nil
}

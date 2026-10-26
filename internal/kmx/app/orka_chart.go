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
	orkaChartLabel = "kmx.kaimahi.dev/installed"
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
	Repository string `json:"repository" yaml:"repository"`
	Digest     string `json:"digest" yaml:"digest"`
}
type orkaHelmWorker struct {
	Image orkaHelmImage `json:"image" yaml:"image"`
}

type orkaHelmValues struct {
	InstallerSHA256  string            `json:"kmxInstallerSHA256" yaml:"kmxInstallerSHA256"`
	Labels           map[string]string `json:"labels"`
	FullnameOverride string            `json:"fullnameOverride"`
	Controller       struct {
		Mode           string        `json:"mode" yaml:"mode"`
		WatchNamespace string        `json:"watchNamespace" yaml:"watchNamespace"`
		Image          orkaHelmImage `json:"image" yaml:"image"`
		ACPRuntime     struct {
			CodexImage    string `json:"codexImage" yaml:"codexImage"`
			ClaudeImage   string `json:"claudeImage" yaml:"claudeImage"`
			CopilotImage  string `json:"copilotImage" yaml:"copilotImage"`
			OpencodeImage string `json:"opencodeImage" yaml:"opencodeImage"`
		} `json:"acpRuntime" yaml:"acpRuntime"`
	} `json:"controller" yaml:"controller"`
	Workers struct {
		AI      orkaHelmWorker `json:"ai" yaml:"ai"`
		General orkaHelmWorker `json:"general" yaml:"general"`
	} `json:"workers" yaml:"workers"`
	Publisher   orkaHelmWorker `json:"publisher" yaml:"publisher"`
	Enforcement struct {
		Enabled *bool  `json:"enabled" yaml:"enabled"`
		Image   string `json:"image" yaml:"image"`
	} `json:"enforcement" yaml:"enforcement"`
}

// Installation refusals remain actionable without modifying cluster state.
// `local down` is for a disposable local kind cluster, never a production shortcut.
func (a *App) orkaInstallRecovery() string {
	return "Local kind cluster you own: `" + a.operationCommand("local", "down") + "` then `" + a.operationCommand("local", "up") + "` replaces it and loses Tasks, Secrets, PVC-backed volumes, model data and the plane ledger. " +
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
	return a.orkaInstallStateFor(publishedOrkaInstallSpec())
}

func (a *App) orkaInstallStateFor(expected orkaInstallSpec) (bool, error) {
	listArgs := []string{"list", "-n", OrkaNamespace, "--kube-context", a.Cfg.KubeContext,
		"-o", "json", "-f", "^" + orkaRelease + "$",
		// Helm 3's --all was removed in Helm 4. These filters are shared by
		// both versions and keep partial/history releases visible.
		"--deployed", "--failed", "--pending", "--uninstalling", "--superseded", "--uninstalled"}
	raw, err := a.Run.Capture("helm", listArgs...)
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
	if len(releases) == 1 && releases[0].Name == orkaRelease && releases[0].Chart == "orka-"+expected.chartVersion && strings.TrimPrefix(releases[0].AppVersion, "v") == strings.TrimPrefix(expected.appVersion, "v") {
		values, err := a.Run.Capture("helm", "get", "values", orkaRelease, "-n", OrkaNamespace, "--kube-context", a.Cfg.KubeContext, "-o", "json")
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
			if installed.InstallerSHA256 != expected.archiveDigest {
				return false, fmt.Errorf("existing Orka release has a different local installer identity; refusing to adopt or overwrite it. %s", a.orkaInstallRecovery())
			}
			if expected.archiveDigest != "" && installed.Controller.WatchNamespace != expected.taskNamespace {
				return false, fmt.Errorf("existing Orka release watches a different Task namespace; refusing to adopt or overwrite it")
			}
			actualImages := orkaInstallImageValues(installed)
			for key, pinned := range expected.images {
				if actualImages[key] != pinned {
					return false, fmt.Errorf("kmx-owned Orka Helm release has changed or missing pinned image values; refusing to adopt or overwrite it. %s", a.orkaInstallRecovery())
				}
				if expected.enforcementBundled && (installed.Enforcement.Enabled == nil || installed.Enforcement.Image != expected.enforcementImage) {
					return false, fmt.Errorf("existing bundled enforcement enrollment or image identity is missing or changed; refusing to adopt or overwrite it")
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
				if expected.archiveDigest != "" {
					if controller.Metadata.Namespace != OrkaNamespace || controller.Metadata.UID == "" || controller.Metadata.DeletionTimestamp != nil {
						return false, fmt.Errorf("local candidate controller has an invalid or terminating identity")
					}
					if err := orkaControllerWatches(controller, expected.taskNamespace); err != nil {
						return false, err
					}
				}
				matches := 0
				for _, container := range controller.Spec.Template.Spec.Containers {
					if container.Name != "controller" {
						continue
					}
					matches++
					if container.Image != expected.controllerImage() {
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
			return false, fmt.Errorf("kmx-owned Orka Helm release is %s; refusing automatic retry over a partial installation. Inspect `helm --kube-context %s -n %s status %s` and Pod events. After resolving any pending operation, an operator may explicitly retry the same SHA-256-verified chart with `helm --kube-context %s -n %s upgrade %s <verified-orka-chart.tgz> --reuse-values --wait` (never --force), or clean the target before retrying kmx. %s", status, shellArg(a.Cfg.KubeContext), OrkaNamespace, orkaRelease, shellArg(a.Cfg.KubeContext), OrkaNamespace, orkaRelease, a.orkaInstallRecovery())
		}
		status := releases[0].Status
		switch status {
		case "deployed", "failed", "pending-install", "pending-upgrade", "pending-rollback", "uninstalling", "superseded", "uninstalled":
		default:
			status = "an unrecognized state"
		}
		return false, fmt.Errorf("existing Orka %s Helm release is %s but lacks KMX's exact ownership marker or harness-v2/fullname configuration; refusing to adopt or overwrite it. Inspect `helm --kube-context %s -n %s get values %s` and `helm --kube-context %s -n %s status %s` before choosing an operator-managed repair or cleanup. %s", expected.appVersion, status, shellArg(a.Cfg.KubeContext), OrkaNamespace, orkaRelease, shellArg(a.Cfg.KubeContext), OrkaNamespace, orkaRelease, a.orkaInstallRecovery())
	}
	if len(releases) != 0 {
		return false, fmt.Errorf("existing Orka Helm release is not kmx's requested %s harness-v2 chart; refusing to overwrite it. %s", expected.appVersion, a.orkaInstallRecovery())
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
	return a.applyOrkaChartFor(chart, publishedOrkaInstallSpec())
}

func (a *App) applyOrkaChartFor(chart []byte, expected orkaInstallSpec) error {
	known, err := a.orkaInstallStateFor(expected)
	if err != nil {
		return err
	}
	if known {
		if expected.enrollment != nil {
			if err := a.setOrkaEnrollment(chart, expected); err != nil {
				return err
			}
		}
		if err := a.OrkaReady(); err != nil {
			return fmt.Errorf("kmx-owned Orka %s is not ready; no reinstall attempted: %w", expected.appVersion, err)
		}
		a.notef("Orka %s harness-v2 is already installed by kmx; keeping its chart, data and snapshot key.", expected.appVersion)
		return nil
	}
	crds, count, err := orkaChartCRDs(chart)
	if err != nil {
		return err
	}
	filename, err := writeOrkaChartArchive(chart)
	if err != nil {
		return err
	}
	defer os.Remove(filename)
	quiet := *a.Run
	quiet.Echo = false
	fmt.Fprintf(a.Err, "kubectl --context %s apply --server-side --field-manager=kmx-orka -f - # (%d CRDs from verified Orka chart)\n", a.Cfg.KubeContext, count)
	if err := quiet.RunStdin(crds, "kubectl", a.kubectl("apply", "--server-side", "--field-manager=kmx-orka", "-f", "-")...); err != nil {
		return fmt.Errorf("applying Orka CRDs before chart: %w", err)
	}
	if err := quiet.RunStdin(crds, "kubectl", a.kubectl("wait", "--for=condition=Established", "--timeout=120s", "-f", "-")...); err != nil {
		return fmt.Errorf("waiting for Orka CRDs before Gateway discovery: %w", err)
	}
	// Helm output is intentionally suppressed: chart hooks and NOTES are not
	// an appropriate place to expose generated Secret values in kmx logs.
	quiet.Stdout, quiet.Stderr = io.Discard, io.Discard
	fmt.Fprintf(a.Err, "helm install orka <verified chart> -n %s --kube-context %s --wait # (harness-v2)\n", OrkaNamespace, a.Cfg.KubeContext)
	args := []string{"install", orkaRelease, filename, "-n", OrkaNamespace,
		"--kube-context", a.Cfg.KubeContext, "--create-namespace", "--wait", "--timeout", "10m",
		"--set", "controller.mode=harness-v2", "--set", "fullnameOverride=orka-api",
		"--set-string", "labels.kmx\\.kaimahi\\.dev/installed=true"}
	for _, key := range orkaInstallImageKeys {
		args = append(args, "--set-string", key+"="+expected.images[key])
	}
	if expected.archiveDigest != "" {
		args = append(args, "--set-string", "kmxInstallerSHA256="+expected.archiveDigest)
		args = append(args, "--set-string", "controller.watchNamespace="+expected.taskNamespace)
		for _, key := range orkaInstallRepositoryKeys {
			args = append(args, "--set-string", key+"="+expected.images[key])
		}
		if expected.enforcementBundled {
			enabled := false
			if expected.enrollment != nil {
				enabled = *expected.enrollment
			}
			args = append(args, "--set", fmt.Sprintf("enforcement.enabled=%t", enabled), "--wait-for-jobs")
			if expected.enforcementImage != "" {
				args = append(args, "--set-string", "enforcement.image="+expected.enforcementImage)
			}
		}
	}
	if err := quiet.Run("helm", args...); err != nil {
		return fmt.Errorf("installing pinned Orka chart: %w\n  A failed release and CRDs may remain; kmx refuses to silently reinstall over them. Inspect without exposing Secrets:\n  helm --kube-context %s -n %s status %s\n  kubectl --context %s -n %s get pods,events", err, shellArg(a.Cfg.KubeContext), OrkaNamespace, orkaRelease, shellArg(a.Cfg.KubeContext), OrkaNamespace)
	}
	return nil
}

func writeOrkaChartArchive(chart []byte) (name string, err error) {
	file, err := os.CreateTemp("", "kmx-orka-*.tgz")
	if err != nil {
		return "", err
	}
	name = file.Name()
	defer func() {
		if err != nil {
			os.Remove(name)
		}
	}()
	_, writeErr := file.Write(chart)
	closeErr := file.Close()
	if writeErr != nil {
		return name, writeErr
	}
	return name, closeErr
}

func (a *App) setOrkaEnrollment(chart []byte, expected orkaInstallSpec) error {
	if expected.archiveDigest == "" || !expected.enforcementBundled || expected.enrollment == nil {
		return fmt.Errorf("enrollment changes require an exact verified bundled chart")
	}
	raw, err := a.Run.Capture("helm", "get", "values", orkaRelease, "-n", OrkaNamespace, "--kube-context", a.Cfg.KubeContext, "-o", "json")
	if err != nil {
		return fmt.Errorf("cannot read current enrollment; no change attempted")
	}
	var installed orkaHelmValues
	if json.Unmarshal([]byte(raw), &installed) != nil || installed.Enforcement.Enabled == nil {
		return fmt.Errorf("current enrollment is missing or invalid; refusing to infer or overwrite it")
	}
	if *installed.Enforcement.Enabled == *expected.enrollment {
		return nil
	}
	if known, err := a.orkaInstallStateFor(expected); err != nil || !known {
		return fmt.Errorf("cannot reverify the exact owned chart before enrollment change; no upgrade attempted")
	}
	filename, err := writeOrkaChartArchive(chart)
	if err != nil {
		return err
	}
	defer os.Remove(filename)
	quiet := *a.Run
	quiet.Echo, quiet.Stdout, quiet.Stderr = false, io.Discard, io.Discard
	a.notef("Explicit enrollment change: %t; same verified chart and retained provider installation. Existing bindings are not retired.", *expected.enrollment)
	if err := quiet.Run("helm", "upgrade", orkaRelease, filename, "-n", OrkaNamespace,
		"--kube-context", a.Cfg.KubeContext, "--reuse-values", "--wait", "--wait-for-jobs", "--timeout", "10m",
		"--set", fmt.Sprintf("enforcement.enabled=%t", *expected.enrollment)); err != nil {
		return fmt.Errorf("enrollment change failed or was ambiguous; inspect Helm status and installer Jobs before any retry; retained bindings must not be removed")
	}
	raw, err = a.Run.Capture("helm", "get", "values", orkaRelease, "-n", OrkaNamespace, "--kube-context", a.Cfg.KubeContext, "-o", "json")
	if err != nil || json.Unmarshal([]byte(raw), &installed) != nil || installed.Enforcement.Enabled == nil || *installed.Enforcement.Enabled != *expected.enrollment {
		return fmt.Errorf("enrollment upgrade completed but its requested state was not verified; do not retry or remove retained bindings")
	}
	if known, err := a.orkaInstallStateFor(expected); err != nil || !known {
		return fmt.Errorf("enrollment upgrade completed but exact owned chart identity was not reverified")
	}
	return nil
}

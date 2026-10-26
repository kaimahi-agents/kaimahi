package app

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"path"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/fileaccess"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/scaffold"
	"go.yaml.in/yaml/v3"
)

var orkaChartVersionPattern = regexp.MustCompile(`^v?[0-9]+\.[0-9]+\.[0-9]+(-[A-Za-z0-9.-]+)?(\+[A-Za-z0-9.-]+)?$`)
var orkaImageRepositoryPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._:/-]*$`)

func orkaLiteralDigestImage(image string) bool {
	repository, hash, ok := strings.Cut(image, "@sha256:")
	return ok && orkaImageRepositoryPattern.MatchString(repository) && verificationDigest(hash)
}

var orkaInstallImageKeys = [...]string{
	"controller.image.digest", "workers.ai.image.digest",
	"workers.general.image.digest", "publisher.image.digest",
	"controller.acpRuntime.codexImage", "controller.acpRuntime.claudeImage",
	"controller.acpRuntime.copilotImage", "controller.acpRuntime.opencodeImage",
}

var orkaInstallRepositoryKeys = [...]string{
	"controller.image.repository", "workers.ai.image.repository",
	"workers.general.image.repository", "publisher.image.repository",
}

type orkaInstallSpec struct {
	chartVersion       string
	appVersion         string
	archiveDigest      string
	taskNamespace      string
	images             map[string]string
	enforcementBundled bool
	enforcementDefault bool
	enforcementImage   string
	enrollment         *bool
}

func publishedOrkaInstallSpec() orkaInstallSpec {
	pins := [...]string{orkaControllerDigest, orkaAIWorkerDigest, orkaGeneralWorkerDigest,
		orkaPublisherDigest, orkaCodexImage, orkaClaudeImage, orkaCopilotImage, orkaOpencodeImage}
	spec := orkaInstallSpec{chartVersion: "0.2.0", appVersion: OrkaVersion, taskNamespace: OrkaNamespace, images: make(map[string]string)}
	for i, key := range orkaInstallImageKeys {
		spec.images[key] = pins[i]
	}
	return spec
}

func orkaInstallImageValues(values orkaHelmValues) map[string]string {
	pins := [...]string{values.Controller.Image.Digest, values.Workers.AI.Image.Digest,
		values.Workers.General.Image.Digest, values.Publisher.Image.Digest,
		values.Controller.ACPRuntime.CodexImage, values.Controller.ACPRuntime.ClaudeImage,
		values.Controller.ACPRuntime.CopilotImage, values.Controller.ACPRuntime.OpencodeImage}
	repositories := [...]string{values.Controller.Image.Repository, values.Workers.AI.Image.Repository,
		values.Workers.General.Image.Repository, values.Publisher.Image.Repository}
	result := make(map[string]string)
	for i, key := range orkaInstallImageKeys {
		result[key] = pins[i]
	}
	for i, key := range orkaInstallRepositoryKeys {
		result[key] = repositories[i]
	}
	return result
}

func (spec orkaInstallSpec) controllerImage() string {
	repository := spec.images["controller.image.repository"]
	if repository == "" {
		repository = "ghcr.io/orka-agents/orka"
	}
	return repository + "@" + spec.images["controller.image.digest"]
}

func (a *App) loadOrkaInstaller(opt OrkaOptions) ([]byte, orkaInstallSpec, error) {
	if opt.Chart == "" {
		if opt.ChartSHA256 != "" {
			return nil, orkaInstallSpec{}, fmt.Errorf("--chart-sha256 requires --chart")
		}
		if opt.Enforcement != nil {
			return nil, orkaInstallSpec{}, fmt.Errorf("--enforcement requires an exact local Orka chart with the bundled Guardd installer; the pinned release does not provide it")
		}
		chart, err := a.fetchOrkaInstaller()
		return chart, publishedOrkaInstallSpec(), err
	}
	if !verificationDigest(opt.ChartSHA256) {
		return nil, orkaInstallSpec{}, fmt.Errorf("--chart requires --chart-sha256 with 64 lowercase hexadecimal characters")
	}
	absolute, err := filepath.Abs(opt.Chart)
	if err != nil {
		return nil, orkaInstallSpec{}, fmt.Errorf("resolve local Orka chart: %w", err)
	}
	parent, err := fileaccess.OpenDirectory(a.operationContext(), filepath.Dir(absolute))
	if err != nil {
		return nil, orkaInstallSpec{}, fmt.Errorf("open local Orka chart directory: %w", err)
	}
	defer parent.Close()
	file, err := fileaccess.OpenRegularAt(a.operationContext(), parent, filepath.Base(absolute))
	if err != nil {
		return nil, orkaInstallSpec{}, fmt.Errorf("open local Orka chart: %w", err)
	}
	defer file.Close()
	const bound = 8 << 20
	chart, err := io.ReadAll(io.LimitReader(file, bound+1))
	if err != nil {
		return nil, orkaInstallSpec{}, fmt.Errorf("read local Orka chart: %w", err)
	}
	if len(chart) > bound {
		return nil, orkaInstallSpec{}, fmt.Errorf("local Orka chart exceeds 8 MiB bound")
	}
	sum := sha256.Sum256(chart)
	if hex.EncodeToString(sum[:]) != opt.ChartSHA256 {
		return nil, orkaInstallSpec{}, fmt.Errorf("local Orka chart SHA256 does not match; nothing was applied")
	}
	spec, err := localOrkaInstallSpec(chart, opt.ChartSHA256)
	if err != nil {
		return nil, orkaInstallSpec{}, err
	}
	if opt.Enforcement != nil && !spec.enforcementBundled {
		return nil, orkaInstallSpec{}, fmt.Errorf("--enforcement requires a bundled Guardd chart aliased enforcement")
	}
	if spec.enforcementDefault && opt.Enforcement == nil {
		return nil, orkaInstallSpec{}, fmt.Errorf("local chart enables privileged enrollment by default; select --enforcement explicitly")
	}
	if opt.Enforcement != nil && *opt.Enforcement {
		if spec.taskNamespace == OrkaNamespace {
			return nil, orkaInstallSpec{}, fmt.Errorf("enforcement requires a distinct precreated controller.watchNamespace for Tasks")
		}
		if spec.enforcementImage == "" {
			return nil, orkaInstallSpec{}, fmt.Errorf("enforcement requires a digest-pinned Guardd image in the verified chart")
		}
	}
	spec.enrollment = opt.Enforcement
	a.notef("Local installer verified: sha256 %s; chart %s, app %s; Task namespace %s. Operator-supplied bytes, not publisher attestation.",
		opt.ChartSHA256, spec.chartVersion, spec.appVersion, spec.taskNamespace)
	return chart, spec, nil
}

func localOrkaInstallSpec(chart []byte, digest string) (orkaInstallSpec, error) {
	fail := func(reason string) (orkaInstallSpec, error) {
		return orkaInstallSpec{}, fmt.Errorf("invalid local Orka chart: %s", reason)
	}
	zr, err := gzip.NewReader(bytes.NewReader(chart))
	if err != nil {
		return fail("cannot open gzip archive")
	}
	defer zr.Close()
	const expandedBound = 64 << 20
	expanded, err := io.ReadAll(io.LimitReader(zr, expandedBound+1))
	if err != nil || len(expanded) > expandedBound {
		return fail("invalid gzip data or expanded archive exceeds 64 MiB")
	}
	tr := tar.NewReader(bytes.NewReader(expanded))
	seen := make(map[string]bool)
	var metadata, defaults []byte
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fail("cannot read tar entry")
		}
		name := strings.TrimSuffix(h.Name, "/")
		if path.Clean(name) != name || (name != "orka" && !strings.HasPrefix(name, "orka/")) ||
			seen[name] || (h.Typeflag != tar.TypeReg && h.Typeflag != tar.TypeDir) || len(seen) >= 8192 {
			return fail("expected unique regular entries in one orka chart tree")
		}
		seen[name] = true
		if name != "orka/Chart.yaml" && name != "orka/values.yaml" {
			continue
		}
		if h.Typeflag != tar.TypeReg || h.Size <= 0 || h.Size > 1<<20 {
			return fail("invalid chart metadata or defaults entry")
		}
		body, err := io.ReadAll(tr)
		if err != nil {
			return fail("cannot read chart metadata or defaults")
		}
		if name == "orka/Chart.yaml" {
			metadata = body
		} else {
			defaults = body
		}
	}
	var info struct {
		APIVersion   string `yaml:"apiVersion"`
		Name         string `yaml:"name"`
		Version      string `yaml:"version"`
		AppVersion   string `yaml:"appVersion"`
		Dependencies []struct {
			Name       string `yaml:"name"`
			Alias      string `yaml:"alias"`
			Version    string `yaml:"version"`
			Repository string `yaml:"repository"`
		} `yaml:"dependencies"`
	}
	version := orkaChartVersionPattern
	if len(metadata) == 0 || yaml.Unmarshal(metadata, &info) != nil ||
		info.APIVersion != "v2" || info.Name != "orka" || strings.HasPrefix(info.Version, "v") ||
		len(info.Version) > 128 || len(info.AppVersion) > 128 ||
		!version.MatchString(info.Version) || !version.MatchString(info.AppVersion) {
		return fail("expected versioned Orka v2 metadata and appVersion")
	}
	for _, dependency := range info.Dependencies {
		if dependency.Repository != "" {
			return fail("dependencies must already be bundled without repository URLs")
		}
	}
	var values orkaHelmValues
	if len(defaults) == 0 || yaml.Unmarshal(defaults, &values) != nil || values.Controller.Mode != "harness-v2" {
		return fail("expected harness-v2 values")
	}
	namespace := values.Controller.WatchNamespace
	if namespace == "" {
		namespace = OrkaNamespace
	}
	if scaffold.ValidateNamespace(namespace) != nil {
		return fail("invalid controller.watchNamespace")
	}
	images := orkaInstallImageValues(values)
	// Helm --set-string interprets commas and backslashes. Image identities
	// deliberately use a smaller, literal-only alphabet before that boundary.
	repository := orkaImageRepositoryPattern
	for i, key := range orkaInstallRepositoryKeys {
		if !repository.MatchString(images[key]) || strings.Contains(images[key], "@") {
			return fail("expected literal image repositories")
		}
		value := images[orkaInstallImageKeys[i]]
		if !strings.HasPrefix(value, "sha256:") || !verificationDigest(strings.TrimPrefix(value, "sha256:")) {
			return fail("all component images must have SHA256 digests")
		}
	}
	for _, key := range orkaInstallImageKeys[4:] {
		if !orkaLiteralDigestImage(images[key]) {
			return fail("all ACP runtime images must be repository@sha256 identities")
		}
	}
	bundled := false
	for _, dependency := range info.Dependencies {
		if dependency.Name == "guardd" && dependency.Alias == "enforcement" {
			forms := 0
			for _, name := range []string{"orka/charts/guardd.tgz", "orka/charts/guardd-" + dependency.Version + ".tgz"} {
				if seen[name] {
					forms++
				}
			}
			if seen["orka/charts/guardd/Chart.yaml"] && seen["orka/charts/guardd/values.yaml"] {
				forms++
			}
			if bundled || forms != 1 || !version.MatchString(dependency.Version) {
				return fail("expected one bundled Guardd dependency in the generator or Helm-packaged layout")
			}
			bundled = true
		}
	}
	enabled := values.Enforcement.Enabled != nil && *values.Enforcement.Enabled
	if bundled && values.Enforcement.Image != "" {
		if !orkaLiteralDigestImage(values.Enforcement.Image) {
			return fail("enforcement image must be a literal repository@sha256 identity")
		}
	}
	return orkaInstallSpec{chartVersion: info.Version, appVersion: info.AppVersion,
		archiveDigest: digest, taskNamespace: namespace, images: images,
		enforcementBundled: bundled, enforcementDefault: enabled, enforcementImage: values.Enforcement.Image}, nil
}

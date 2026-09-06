package app

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"runtime/debug"
	"slices"
	"strings"
	"time"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/admin"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/planebuild"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/run"
)

// PlaneImage is the tag the plane's image is built and side-loaded under.
//
// It is a CONSTANT, and equal to the tag committed in k8s/plane/proxy.yaml,
// because the kind path applies that manifest exactly as committed — no
// render, no transform — which is what makes "kind is unchanged" a fact
// rather than a claim (scripts/plane-deploy.sh's opening comment). Only a
// REGISTRY target renders, and that is still the script's job: kmx is kind
// only.
//
// The alternative — tagging by kmx's own revision — is better staleness
// protection in general, and it is deliberately not taken
// here. `kmx plane` REBUILDS the image and side-loads it on every run, then
// restarts the deployment unconditionally, so the "same tag, older bytes"
// failure the moving tag exists to prevent cannot occur on this path; and
// buying that protection would mean rendering the manifest on kind too,
// which means a second implementation of a render whose fail-closed rules
// were each paid for by an incident. Which revision is actually RUNNING is
// answered where it belongs: `kaimahi_build_info`, which now carries the
// revision on this path as well (plane/internal/metrics).
//
// TestPlaneImageMatchesTheCommittedManifest pins this to the manifest, and
// asserts the manifest still pins `imagePullPolicy: Never` — a side-loaded
// LOCAL tag must never quietly fall back to PULLING a squattable public
// name.
const PlaneImage = "kaimahi-proxy:p15"

// PlaneSteps are the stages of `kmx plane`, addressable individually so the
// Makefile's `plane-image` and `plane-secrets` targets delegate to the same
// code rather than keeping a second copy of it.
var PlaneSteps = []string{"image", "secrets", "deploy"}

// planeManifests are applied in the order `kubectl apply -f k8s/plane/`
// applies them (kubectl sorts by filename), so the two paths cannot diverge
// on ordering. The namespace comes first either way.
var planeManifests = []string{
	"plane/namespace.yaml",
	"plane/network-policy.yaml",
	"plane/postgres.yaml",
	"plane/proxy.yaml",
	"plane/upstreams.yaml",
}

// PlaneOptions carry the source selection.
type PlaneOptions struct {
	// Step, when set, runs one stage only.
	Step string
	// Source is a checkout to build the plane from. "-" forces the
	// clone-free path even inside a checkout; empty means auto-detect.
	Source string
}

// Plane stands the governance plane up on the active context: build the
// proxy image, bootstrap the plane's own secrets, apply the manifests.
//
// This is `make plane` (which is `guard plane-image plane-secrets` and then
// scripts/plane-deploy.sh), with one difference that is the point of the
// milestone: the image does not need a clone. See planebuild.
func (a *App) Plane(opt PlaneOptions) error {
	started := a.timeNow()
	steps := PlaneSteps
	if opt.Step != "" {
		found := false
		for _, s := range PlaneSteps {
			if s == opt.Step {
				found, steps = true, []string{s}
				break
			}
		}
		if !found {
			return fmt.Errorf("unknown step %q — one of: %s", opt.Step, strings.Join(PlaneSteps, ", "))
		}
	}
	dependencies := []dependency{depKubectl}
	for _, step := range steps {
		if step != "image" {
			continue
		}
		dependencies = append(dependencies, depKind, a.engineDependency())
		needsGo := strings.TrimSpace(opt.Source) == "-"
		if strings.TrimSpace(opt.Source) == "" {
			cwd, err := os.Getwd()
			if err != nil {
				return err
			}
			_, local := planebuild.DetectSourceFS(cwd)
			needsGo = !local
		}
		if needsGo {
			dependencies = append(dependencies, depGo)
		}
	}
	if err := a.preflight(dependencies...); err != nil {
		return err
	}

	action, command := "deploy the Kaimahi governance plane (proxy + Postgres ledger)", "kmx plane"
	if opt.Step != "" {
		action = "run the plane's '" + opt.Step + "' step"
		command = "kmx plane --step " + opt.Step
	}
	if err := a.Guard(action, command); err != nil {
		return err
	}

	for i, s := range steps {
		var err error
		name := map[string]string{"image": "Build and load proxy image", "secrets": "Reconcile plane secrets", "deploy": "Deploy and verify plane"}[s]
		err = a.runPhase(phase{current: i + 1, total: len(steps), name: name}, func() error {
			switch s {
			case "image":
				return a.planeImage(opt)
			case "secrets":
				return a.planeSecrets()
			case "deploy":
				return a.planeDeploy()
			}
			return nil
		})
		if err != nil {
			return err
		}
	}

	if opt.Step == "" {
		a.complete("Governance plane ready", started)
		a.notef("\nNEXT  Nothing is governed by the plane yet:\n"+
			"  kmx govern %s      # issue the credential and put the agent behind the plane\n"+
			"  kmx ledger            # what it has spent", a.Cfg.Credential)
	}
	return nil
}

// ---- the image ------------------------------------------------------------

// planeImage builds the proxy image and side-loads it into the kind cluster.
func (a *App) planeImage(opt PlaneOptions) error {
	if err := a.refuseForeignImageTag(); err != nil {
		return err
	}

	source, err := a.planeSource(opt.Source)
	if err != nil {
		return err
	}
	if source != "" {
		if err := a.buildFromSource(source); err != nil {
			return err
		}
	} else if err := a.buildFromModuleProxy(); err != nil {
		return err
	}
	return a.loadImage()
}

// refuseForeignImageTag fails closed on PLANE_IMAGE.
//
// The Makefile's variable exists for the registry path, where the image
// reference and the pull policy are RENDERED into the manifest at deploy
// time. kmx applies the manifest unrendered, so honouring PLANE_IMAGE here
// would build and side-load one tag while deploying another — a plane that
// silently keeps running the previous image. Say so instead of ignoring it.
func (a *App) refuseForeignImageTag() error {
	set := strings.TrimSpace(os.Getenv("PLANE_IMAGE"))
	if set == "" || set == PlaneImage {
		return nil
	}
	return fmt.Errorf("PLANE_IMAGE=%s, but kmx deploys k8s/plane/proxy.yaml exactly as committed, which names %s.\n"+
		"  kmx is the kind path: a side-loaded local tag, imagePullPolicy Never.\n"+
		"  A registry-backed cluster renders the manifest instead — that is `TARGET=aks make plane` (docs/aks.md).",
		set, PlaneImage)
}

// planeSource resolves where the plane's code comes from: the flag, then the
// checkout kmx is being run from, else "" for the module proxy.
//
// A checkout WINS, and that is not a convenience. CI runs the Makefile, the
// Makefile passes `--source .`, and so a pull request that changes plane/ is
// tested against the code it changed rather than against whatever the public
// proxy last published.
func (a *App) planeSource(flag string) (string, error) {
	switch strings.TrimSpace(flag) {
	case "-":
		a.notef("plane source: the public Go proxy (--source - overrides the checkout)")
		return "", nil
	case "":
		cwd, err := os.Getwd()
		if err != nil {
			return "", err
		}
		if root, ok := planebuild.DetectSourceFS(cwd); ok {
			a.notef("plane source: this checkout (%s) — `--source -` fetches the published module instead", root)
			return root, nil
		}
		return "", nil
	default:
		given := strings.TrimSpace(flag)
		abs, err := filepath.Abs(given)
		if err != nil {
			return "", err
		}
		// The detected ROOT is what gets built, not the path as given.
		// Detection walks UP, so `--source docs` inside a clone validates
		// against the repository two levels above it; building `docs/plane`
		// would then fail with a path error instead of doing the obvious
		// thing.
		root, ok := planebuild.DetectSourceFS(abs)
		if !ok {
			return "", fmt.Errorf("--source %s is not a checkout of this repository (no go.mod for github.com/kaimahi-agents/kaimahi with a plane/ module under it)", given)
		}
		a.notef("plane source: %s", root)
		return root, nil
	}
}

// buildFromSource builds plane/Dockerfile out of a checkout — literally what
// `make plane-image` has always run, so a checkout has ONE image recipe and
// a change to how the image is built is exercised by the PR that makes it.
func (a *App) buildFromSource(root string) error {
	// The revision stamped into the binary for kaimahi_build_info. Say when
	// it cannot be read: an image built here publishes
	// kaimahi_build_info{version="unknown"}, which is the very label the
	// rest of this change exists to make trustworthy.
	version := "unknown"
	if out, err := a.Run.Capture("git", "-C", root, "rev-parse", "--short=12", "HEAD"); err == nil && out != "" {
		version = out
	} else {
		a.notef("NOTE: cannot read %s's git revision, so the plane will report kaimahi_build_info{version=\"unknown\"}", root)
	}
	return a.Run.Run(a.Cfg.ContainerEngine, "build",
		"--build-arg", "VERSION="+version, "-t", PlaneImage, filepath.Join(root, "plane"))
}

// buildFromModuleProxy is the clone-free path: fetch and build the plane at
// kmx's own revision, then package the binary onto the same runtime base
// plane/Dockerfile uses.
func (a *App) buildFromModuleProxy() error {
	rev, err := planebuild.Revision(debug.ReadBuildInfo())
	if err != nil {
		return err
	}

	cache, err := a.Cfg.PlaneCacheDir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(cache, 0o755); err != nil {
		return err
	}
	gopath, err := a.Run.Capture("go", "env", "GOPATH")
	if err != nil {
		return fmt.Errorf("cannot read GOPATH: %w", err)
	}

	targetOS, targetArch := planebuild.TargetPlatform()
	plan := planebuild.PlanInstall(rev, runtime.GOOS, runtime.GOARCH, targetOS, targetArch, gopath, cache)

	a.notef("fetching and building the plane at kmx's own revision %s (%s/%s), checksummed by Go's sum database",
		rev, targetOS, targetArch)
	installer := *a.Run
	installer.Env = plan.Env
	installer.Unset = plan.Unset
	if plan.Cross {
		// `go install` refuses to cross-compile while GOBIN is set, and
		// GOBIN is set by mise, asdf and `go env -w` alike. Removing the
		// variable handles the first two; a GOBIN in Go's own environment
		// file survives that, so check what the toolchain actually sees.
		if gobin, err := installer.Capture("go", "env", "GOBIN"); err == nil && strings.TrimSpace(gobin) != "" {
			return planebuild.GOBINStillSet(strings.TrimSpace(gobin))
		}
	}
	started := time.Now().Add(-time.Second)
	if err := a.goInstallPlane(&installer, plan, rev); err != nil {
		return err
	}
	// Where the binary landed is a decision, not a search: an older binary
	// left in the same place by an earlier run must not be packaged as if
	// this build had produced it.
	info, err := os.Stat(plan.Output)
	if err != nil {
		return fmt.Errorf("go install reported success but %s is not there: %w", plan.Output, err)
	}
	if info.ModTime().Before(started) {
		return fmt.Errorf("%s was not written by this build (modified %s) — refusing to package a stale binary",
			plan.Output, info.ModTime().Format(time.RFC3339))
	}

	context, err := os.MkdirTemp("", "kmx-plane-image-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(context)
	binary, err := os.ReadFile(plan.Output)
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(context, planebuild.Binary), binary, 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(context, "Dockerfile"), []byte(planebuild.Dockerfile()), 0o644); err != nil {
		return err
	}
	return a.Run.Run(a.Cfg.ContainerEngine, "build", "-t", PlaneImage, context)
}

// loadImage side-loads the built image into the kind cluster.
//
// The podman half is the Makefile's, carried across with its reason: `kind
// load docker-image` does not work against podman here — kind reports "image
// not present locally" for images podman demonstrably has (verified with
// podman 5.8.1 / kind 0.27.0, even for `alpine`). Piping an archive is the
// engine-agnostic path kind documents for non-docker providers. Docker keeps
// the direct load: it works, and it skips writing a ~19MB tarball.
func (a *App) loadImage() error {
	if a.Cfg.ContainerEngine != "podman" {
		return a.Run.Run("kind", "load", "docker-image", PlaneImage, "--name", a.Cfg.KindCluster)
	}
	tar, err := os.CreateTemp("", "kaimahi-plane-*.tar")
	if err != nil {
		return err
	}
	tar.Close()
	defer os.Remove(tar.Name())
	if err := a.Run.Run("podman", "save", "-o", tar.Name(), PlaneImage); err != nil {
		return err
	}
	return a.Run.Run("kind", "load", "image-archive", tar.Name(), "--name", a.Cfg.KindCluster)
}

// ---- the plane's own secrets ---------------------------------------------

// planeSecrets bootstraps the plane's own secrets idempotently: the Postgres
// password and the admin API bearer, both in the kaimahi namespace.
//
// This is scripts/plane-secrets.sh, with one deliberate difference: the
// namespace is APPLIED from the embedded k8s/plane/namespace.yaml rather
// than created bare, so it carries whatever that manifest carries and a
// later `kmx plane` step cannot be the first thing to define it.
//
// Existing Secrets are KEPT — regenerating
// the pg password under a live database would lock the proxy out — and the
// generated values travel only through the pipe into kubectl. The script had
// to write them to 0600 files first, because `kubectl create secret
// --from-file` reads a path; kmx renders the Secret itself and pipes it, so
// no secret value ever reaches a file, argv, the environment or a log.
func (a *App) planeSecrets() error {
	if err := a.apply("plane/namespace.yaml"); err != nil {
		return err
	}
	if err := a.ensureSecret("kaimahi-pg", "password"); err != nil {
		return err
	}
	return a.ensureSecret("kaimahi-admin", "token")
}

func (a *App) ensureSecret(name, key string) error {
	_, err := a.kubectlCapture("-n", admin.Namespace, "get", "secret", name, "-o", "name")
	switch {
	case err == nil:
		a.notef("Secret %s exists; keeping it.", name)
		return nil
	case !isNotFound(err):
		// Deliberately not collapsed with NotFound: an unreachable API
		// server, an expired credential or an RBAC denial answered as
		// "absent" would send us into creating a SECOND password under a
		// live database.
		return fmt.Errorf("cannot tell whether Secret %s exists (refusing to generate a second one): %w", name, err)
	}

	value, err := randomHex(32)
	if err != nil {
		return err
	}
	body := secretManifest(name, admin.Namespace, map[string]string{key: value}, nil)
	// `create`, not `apply`: if something raced us to it, the loser must
	// fail rather than overwrite a password the running database is using.
	quiet := *a.Run
	quiet.Echo = false
	if err := quiet.RunStdin(body, "kubectl", a.kubectl("-n", admin.Namespace, "create", "-f", "-")...); err != nil {
		return err
	}
	a.notef("Secret %s created.", name)
	return nil
}

// randomHex returns n cryptographically random bytes, hex encoded — the
// script's `od -An -N32 -tx1 /dev/urandom`.
func randomHex(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("entropy read failed: %w", err)
	}
	return hex.EncodeToString(b), nil
}

// secretManifest renders an Opaque Secret. Values are base64-encoded into
// `data`, so nothing has to be escaped and no value can break out of the
// document.
func secretManifest(name, namespace string, values, annotations map[string]string) []byte {
	var b strings.Builder
	b.WriteString("apiVersion: v1\nkind: Secret\nmetadata:\n")
	fmt.Fprintf(&b, "  name: %s\n  namespace: %s\n", name, namespace)
	if len(annotations) > 0 {
		b.WriteString("  annotations:\n")
		for _, k := range sortedKeys(annotations) {
			fmt.Fprintf(&b, "    %s: %q\n", k, annotations[k])
		}
	}
	b.WriteString("type: Opaque\ndata:\n")
	for _, k := range sortedKeys(values) {
		fmt.Fprintf(&b, "  %s: %s\n", k, base64.StdEncoding.EncodeToString([]byte(values[k])))
	}
	return []byte(b.String())
}

// sortedKeys keeps the rendered document stable, so re-running a step
// produces byte-identical YAML and `kubectl apply` reports no change.
func sortedKeys(m map[string]string) []string {
	return slices.Sorted(maps.Keys(m))
}

// ---- deploying ------------------------------------------------------------

// planeDeploy applies the plane's manifests and waits for it to serve.
func (a *App) planeDeploy() error {
	for _, name := range planeManifests {
		if err := a.apply(name); err != nil {
			return err
		}
	}
	if err := a.kubectlRun("-n", admin.Namespace, "rollout", "status",
		"deploy/kaimahi-postgres", "--timeout=300s"); err != nil {
		return err
	}
	// ALWAYS restart. A rebuilt image under the SAME tag leaves the
	// Deployment's spec unchanged, so apply alone would keep the old binary
	// running: kind's `imagePullPolicy: Never` reuses same-tag images
	// without complaint. This is the line that makes `kmx plane` mean "the
	// plane is now running the code I just built".
	if err := a.kubectlRun("-n", admin.Namespace, "rollout", "restart", "deploy/kaimahi-proxy"); err != nil {
		return err
	}
	return a.kubectlRun("-n", admin.Namespace, "rollout", "status",
		"deploy/kaimahi-proxy", "--timeout=300s")
}

// planeNotOnProxyYet matches the failure that means "the proxy has not been
// asked for the nested module yet", not "this build is broken".
//
// Anchored on both halves — the fallback phrase and the plane's own package
// path — so a genuine missing package elsewhere cannot be mistaken for it.
var planeNotOnProxyYet = regexp.MustCompile(
	`module .* found, but does not contain package .*/plane/cmd/kaimahi-proxy`)

// goInstallPlane installs the plane, resolving the nested module first.
//
// plane/ has its own go.mod. `go install pkg@version` finds the providing
// module by asking the proxy for each path prefix, longest first; when the
// proxy has not cached …/plane it answers 404, Go falls back to the ROOT
// module, and the error reads as a broken layout when nothing is broken.
//
// Naming the module outright is what makes the proxy fetch it. Established
// by experiment on the revision that reddened main, with a COLD module
// cache each time — the distinction matters, because a warm cache hides it:
//
//	go install …/plane/cmd/kaimahi-proxy@<rev>   -> fails
//	go list -m …/plane@<rev>                     -> resolves
//	go install …/plane/cmd/kaimahi-proxy@<rev>   -> succeeds
//
// So this is a priming step, not a retry: the first command is the one that
// makes the second possible, and repeating the install alone would have
// waited out a clock that was never going to help.
func (a *App) goInstallPlane(installer *run.Runner, plan planebuild.Install, rev string) error {
	out, _, err := installer.CaptureCombined("go", plan.Args...)
	if err == nil {
		return nil
	}
	if !planeNotOnProxyYet.MatchString(out) {
		return planeInstallErr(rev, err, out)
	}

	a.notef("the Go proxy has not served the nested plane module at %s yet; resolving it explicitly", rev)
	resolver := *installer
	if _, rerr := resolver.Capture("go", "list", "-m", planebuild.NestedModule+"@"+rev); rerr != nil {
		return planeInstallErr(rev, err, out)
	}
	if out, _, err = installer.CaptureCombined("go", plan.Args...); err != nil {
		return planeInstallErr(rev, err, out)
	}
	return nil
}

func planeInstallErr(rev string, err error, out string) error {
	return fmt.Errorf("cannot build the plane at revision %s: %w\n%s\n"+
		"  If this revision is not yet on the public Go proxy (a commit that has not merged),\n"+
		"  build from a checkout instead: kmx plane --source <path to the repo>",
		rev, err, strings.TrimSpace(out))
}

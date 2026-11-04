package app

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime/debug"
	"slices"
	"strings"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/run"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/version"
)

// UpOptions selects the existing journey or a local node-only bundle. An absent
// OfflineBundle preserves Up's online behavior and callers.
type UpOptions struct{ Step, OfflineBundle string }

// UpWithOptions never provisions tools or runs model/runtime phases for a bundle.
func (a *App) UpWithOptions(opt UpOptions) error {
	if opt.OfflineBundle == "" {
		return a.Up(opt.Step)
	}
	if opt.Step != "cluster" {
		return fmt.Errorf("--offline-bundle requires --step cluster")
	}
	if strings.TrimSpace(opt.OfflineBundle) == "" {
		return fmt.Errorf("--offline-bundle requires a non-empty directory")
	}
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
	identity, err := resolve()
	if err != nil {
		return err
	}
	bundle, err := loadOfflineNodeBundle(opt.OfflineBundle, identity)
	if err != nil {
		return err
	}
	// Freeze every offline kind call to the verified engine, never auto-detect.
	local, runner := *a, *a.Run
	runner.Env = append(append([]string(nil), runner.Env...), "KIND_EXPERIMENTAL_PROVIDER="+a.Cfg.ContainerEngine)
	local.Run = &runner
	a = &local
	if err := a.preflightLocal(depKind, depKubectl, a.engineDependency()); err != nil {
		return err
	}
	if err := a.verifyOfflineNodeImage(bundle, bundle.node.LocalRef); err != nil {
		return err
	}
	if err := a.GuardCreate("run the offline 'cluster' step", a.operationCommand("cluster", "up", "--step", "cluster", "--offline-bundle", opt.OfflineBundle)); err != nil {
		return err
	}
	return a.runPhase(phase{current: 1, total: 1, name: upPhaseName("cluster")}, func() error { return a.stepClusterWithNode(bundle) })
}

// Kind retries a failed local inspection with pull. Constrain only its create
// child: trusted kind's engine calls, not arbitrary executables or OS isolation.
func (a *App) offlineKindRunner() (*run.Runner, func(), error) {
	if err := a.Run.Command("/bin/sh", "-c", ":").Run(); err != nil {
		return nil, nil, fmt.Errorf("offline kind creation requires /bin/sh: %w", err)
	}
	engine := a.Run.Command(a.Cfg.ContainerEngine)
	if engine.Err != nil {
		return nil, nil, engine.Err
	}
	executable, err := filepath.Abs(engine.Path)
	if err != nil {
		return nil, nil, err
	}
	dir, err := os.MkdirTemp("", "kmx-offline-kind-")
	if err != nil {
		return nil, nil, err
	}
	cleanup := func() { _ = os.RemoveAll(dir) }
	absolute, err := filepath.Abs(dir)
	if err != nil {
		cleanup()
		return nil, nil, err
	}
	dir = absolute
	script := `#!/bin/sh
case "$1 $2" in
"__kmx_offline_guard_probe__ ") exit 42 ;;
"pull "*|"image pull") echo 'offline kind: image pull is disabled' >&2; exit 1 ;;
"run "*|"create "*) command="$1"; shift; set -- "$command" --pull=never "$@" ;;
esac
exec ` + shellArg(executable) + ` "$@"
`
	if err := os.WriteFile(filepath.Join(dir, a.Cfg.ContainerEngine), []byte(script), 0700); err != nil {
		cleanup()
		return nil, nil, err
	}
	if err := a.verifyOfflineKindGuard(filepath.Join(dir, a.Cfg.ContainerEngine)); err != nil {
		cleanup()
		return nil, nil, err
	}
	path := dir
	for _, entry := range engine.Environ() {
		if value, ok := strings.CutPrefix(entry, "PATH="); ok {
			path += string(os.PathListSeparator) + value
		}
	}
	runner := *a.Run
	runner.Env = append(append([]string(nil), runner.Env...), "PATH="+path)
	return &runner, cleanup, nil
}

// Direct execution must work: LookPath skips a noexec shim and could otherwise
// find the real engine later in PATH. A shell-interpreted probe would miss this.
func (a *App) verifyOfflineKindGuard(path string) error {
	err := a.Run.Command(path, "__kmx_offline_guard_probe__").Run()
	if cancelled := a.operationContext().Err(); cancelled != nil {
		return fmt.Errorf("offline kind guard execution: %w", cancelled)
	}
	if status, ok := err.(*exec.ExitError); ok && status.ExitCode() == 42 {
		return nil
	}
	return fmt.Errorf("offline kind guard execution failed (expected probe exit 42): %v", err)
}

func runningOfflineIdentity() (offlineKMXIdentity, error) {
	build := version.Current()
	info, ok := debug.ReadBuildInfo()
	clean := false
	if ok {
		for _, s := range info.Settings {
			if s.Key == "vcs.modified" {
				clean = s.Value == "false"
			}
		}
	}
	if !clean || build.Modified || build.Version == "" || build.Version == version.Unknown || !offlineHex(build.Revision, 40) {
		return offlineKMXIdentity{}, fmt.Errorf("offline bundle requires kmx built with known version/revision and unmodified VCS metadata")
	}
	path, err := os.Executable()
	if err != nil {
		return offlineKMXIdentity{}, fmt.Errorf("cannot locate running kmx executable: %w", err)
	}
	f, err := os.Open(path)
	if err != nil {
		return offlineKMXIdentity{}, fmt.Errorf("cannot read running kmx executable: %w", err)
	}
	defer f.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, f); err != nil {
		return offlineKMXIdentity{}, fmt.Errorf("cannot hash running kmx executable: %w", err)
	}
	return offlineKMXIdentity{Version: build.Version, Revision: build.Revision, SHA256: fmt.Sprintf("%x", hash.Sum(nil))}, nil
}

// Unlike Runner.Capture, inspection capture is bounded and never attaches raw
// subprocess stdout/stderr to errors (engine responses are untrusted metadata).
type offlineOutput struct{ buffer bytes.Buffer }

func (b *offlineOutput) Write(p []byte) (int, error) {
	if len(p) > offlineJSONLimit-b.buffer.Len() {
		return 0, fmt.Errorf("offline inspection exceeds 1 MiB")
	}
	return b.buffer.Write(p)
}
func (a *App) offlineCapture(tool string, args ...string) ([]byte, error) {
	if err := a.operationContext().Err(); err != nil {
		return nil, err
	}
	cmd := a.Run.Command(tool, args...)
	var out offlineOutput
	cmd.Stdout = &out
	cmd.Stderr = io.Discard
	if err := cmd.Run(); err != nil {
		if err := a.operationContext().Err(); err != nil {
			return nil, err
		}
		return nil, fmt.Errorf("%s local inspection failed (missing, unusable or oversized local metadata)", tool)
	}
	return out.buffer.Bytes(), nil
}
func (b *offlineNodeBundle) acceptsID(id string) bool {
	// Some stores expose SHA-256 IDs without the algorithm prefix. Normalize
	// only a complete lower-case SHA-256, never a short ID or mutable reference.
	if offlineHex(id, 64) {
		id = "sha256:" + id
	}
	return id == b.node.ConfigDigest || id == b.node.ManifestDigest || b.node.IndexDigest != nil && id == *b.node.IndexDigest
}
func (a *App) verifyOfflineNodeImage(b *offlineNodeBundle, ref string) error {
	raw, err := a.offlineCapture(a.Cfg.ContainerEngine, "image", "inspect", ref)
	if err != nil {
		return fmt.Errorf("node image %s: %w", ref, err)
	}
	var rows []struct {
		ID           string `json:"Id"`
		OS           string `json:"Os"`
		Architecture string
		Variant      string
		RootFS       struct {
			Type   string
			Layers []string
		}
	}
	if offlineJSON(raw, &rows, false) != nil || len(rows) != 1 {
		return fmt.Errorf("node image %s: malformed/unknown engine inspection", ref)
	}
	r := rows[0]
	if !b.acceptsID(r.ID) || r.OS != b.platform.OS || r.Architecture != b.platform.Architecture || !(r.Variant == "" || r.Architecture == "arm64" && r.Variant == "v8") || r.RootFS.Type != "layers" || !slices.Equal(r.RootFS.Layers, b.diffIDs) {
		return fmt.Errorf("node image %s does not match pinned config/manifest/index, native platform and rootfs", ref)
	}
	return nil
}

func offlineNodeNames(raw []byte) ([]string, error) {
	names := strings.Fields(string(raw))
	if len(names) == 0 || len(names) > 100 {
		return nil, fmt.Errorf("kind/engine node inventory is empty or oversized")
	}
	slices.Sort(names)
	for i, name := range names {
		if !offlineContainerName.MatchString(name) || i > 0 && name == names[i-1] {
			return nil, fmt.Errorf("invalid or duplicate kind node name")
		}
	}
	return names, nil
}
func (a *App) verifyOfflineClusterNodes(b *offlineNodeBundle) ([]string, error) {
	raw, err := a.offlineCapture("kind", "get", "nodes", "--name", a.Cfg.KindCluster)
	if err != nil {
		return nil, err
	}
	names, err := offlineNodeNames(raw)
	if err != nil {
		return nil, err
	}
	raw, err = a.offlineCapture(a.Cfg.ContainerEngine, "ps", "-a", "--filter", "label=io.x-k8s.kind.cluster="+a.Cfg.KindCluster, "--format", "{{.Names}}")
	if err != nil {
		return nil, err
	}
	labeled, err := offlineNodeNames(raw)
	if err != nil {
		return nil, err
	}
	if !slices.Equal(names, labeled) {
		return nil, fmt.Errorf("kind and %s node inventories disagree for %q", a.Cfg.ContainerEngine, a.Cfg.KindCluster)
	}
	for _, name := range names {
		raw, err := a.offlineCapture(a.Cfg.ContainerEngine, "container", "inspect", name)
		if err != nil {
			return nil, err
		}
		var rows []struct {
			Image, Name             string
			Config                  struct{ Labels map[string]string }
			ImageManifestDescriptor *struct{ Digest string }
		}
		if offlineJSON(raw, &rows, false) != nil || len(rows) != 1 || strings.TrimPrefix(rows[0].Name, "/") != name || rows[0].Config.Labels["io.x-k8s.kind.cluster"] != a.Cfg.KindCluster || !b.acceptsID(rows[0].Image) {
			return nil, fmt.Errorf("kind node %s has unknown or mismatched immutable image/cluster identity", name)
		}
		r := rows[0]
		selected := r.ImageManifestDescriptor
		parent := b.node.IndexDigest != nil && strings.TrimPrefix(r.Image, "sha256:") == strings.TrimPrefix(*b.node.IndexDigest, "sha256:")
		if selected != nil && selected.Digest != b.node.ManifestDigest || parent && selected == nil {
			return nil, fmt.Errorf("kind node %s has unknown or mismatched selected manifest", name)
		}
		// An index inspection may choose the host child rather than this node's.
		ref := r.Image
		if selected != nil {
			ref = selected.Digest
		}
		if err := a.verifyOfflineNodeImage(b, ref); err != nil {
			return nil, fmt.Errorf("kind node %s: %w", name, err)
		}
	}
	return names, nil
}

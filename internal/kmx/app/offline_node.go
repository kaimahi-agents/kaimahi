package app

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"runtime/debug"
	"slices"
	"strings"

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
			Image, Name string
			Config      struct{ Labels map[string]string }
		}
		if offlineJSON(raw, &rows, false) != nil || len(rows) != 1 || strings.TrimPrefix(rows[0].Name, "/") != name || rows[0].Config.Labels["io.x-k8s.kind.cluster"] != a.Cfg.KindCluster || !b.acceptsID(rows[0].Image) {
			return nil, fmt.Errorf("kind node %s has unknown or mismatched immutable image/cluster identity", name)
		}
		// Inspect the immutable ID used by this container, never its original tag:
		// a correctly cached tag cannot certify an older node created from another ID.
		if err := a.verifyOfflineNodeImage(b, rows[0].Image); err != nil {
			return nil, fmt.Errorf("kind node %s: %w", name, err)
		}
	}
	return names, nil
}

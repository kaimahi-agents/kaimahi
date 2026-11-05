package app

import (
	"encoding/hex"
	"fmt"
	"slices"
	"strings"
)

const modelCredentialDir = "/run/bridge/credentials"

const modelDefaultKindConfig = `apiVersion: kind.x-k8s.io/v1alpha4
kind: Cluster
kubeadmConfigPatches:
- |
  apiVersion: kubelet.config.k8s.io/v1beta1
  kind: KubeletConfiguration
  syncFrequency: 5s
`

func modelKindConfig(profile string) ([]byte, error) {
	if profile == "" {
		return []byte(modelDefaultKindConfig), nil
	}
	if profile != "copilot-v1" {
		return nil, fmt.Errorf("unsupported credential mount profile")
	}
	return []byte(modelDefaultKindConfig + `nodes:
- role: control-plane
  extraMounts:
  - hostPath: /run/bridge/credentials
    containerPath: /run/bridge/credentials
    readOnly: true
    propagation: None
`), nil
}

// Inspect every node independently. Mount metadata is configuration evidence,
// not proof that a running namespace still sees the current tmpfs instance.
func modelNodeMount(raw []byte) error {
	var rows []struct {
		Mounts []struct {
			Type, Source, Destination, Propagation string
			RW                                     *bool
		}
	}
	if offlineJSON(raw, &rows, false) != nil || len(rows) != 1 {
		return fmt.Errorf("ambiguous credential node mount")
	}
	count := 0
	for _, m := range rows[0].Mounts {
		if strings.HasPrefix(m.Destination, modelCredentialDir+"/") {
			return fmt.Errorf("credential node mount has an unexpected nested bind")
		}
		if m.Destination != modelCredentialDir {
			continue
		}
		count++
		if m.Type != "bind" || m.Source != modelCredentialDir || (m.RW == nil || *m.RW) || m.Propagation != "rprivate" {
			return fmt.Errorf("incompatible credential node mount")
		}
	}
	if count != 1 {
		return fmt.Errorf("credential node mount missing or ambiguous")
	}
	return nil
}

func (a *App) modelMountSource() (string, error) {
	fail := fmt.Errorf("credential memory mount is unavailable or incompatible")
	raw, err := a.offlineCapture("findmnt", "-rn", "-M", modelCredentialDir, "-o", "FSTYPE,OPTIONS")
	if err != nil {
		return "", fail
	}
	fields := strings.Fields(string(raw))
	if len(fields) != 2 || fields[0] != "tmpfs" {
		return "", fail
	}
	for _, flag := range []string{"nosuid", "nodev", "noexec"} {
		if !strings.Contains(","+fields[1]+",", ","+flag+",") {
			return "", fail
		}
	}
	for _, path := range []string{"/run", "/run/bridge", modelCredentialDir} {
		raw, err = a.offlineCapture("stat", "-c", "%F:%u:%g:%a:%d:%i", "--", path)
		if err != nil {
			return "", fail
		}
		f := strings.Split(strings.TrimSpace(string(raw)), ":")
		if len(f) != 6 || f[0] != "directory" || f[1] != "0" {
			return "", fail
		}
		if path == modelCredentialDir {
			if f[2] != "65532" || f[3] != "750" {
				return "", fail
			}
		} else if f[3] != "755" && f[3] != "700" && f[3] != "750" {
			return "", fail
		}
	}
	return string(raw), nil
}

func (a *App) checkModelNodeMounts() error {
	identity, err := a.modelMountSource()
	if err != nil {
		return err
	}
	raw, err := a.offlineCapture("kind", "get", "nodes", "--name", a.Cfg.KindCluster)
	if err != nil {
		return err
	}
	names, err := offlineNodeNames(raw)
	if err != nil {
		return err
	}
	raw, err = a.offlineCapture("docker", "ps", "-a", "--filter", "label=io.x-k8s.kind.cluster="+a.Cfg.KindCluster, "--format", "{{.Names}}")
	if err != nil {
		return err
	}
	labeled, err := offlineNodeNames(raw)
	if err != nil || !slices.Equal(names, labeled) {
		return fmt.Errorf("credential node inventory is incomplete")
	}
	for _, name := range names {
		raw, err = a.offlineCapture("docker", "container", "inspect", name)
		if err != nil {
			return err
		}
		if err := modelNodeMount(raw); err != nil {
			return err
		}
		var rows []struct {
			Name, Image string
			Config      struct{ Labels map[string]string }
			State       struct{ Running *bool }
		}
		if offlineJSON(raw, &rows, false) != nil || len(rows) != 1 || strings.TrimPrefix(rows[0].Name, "/") != name || rows[0].Config.Labels["io.x-k8s.kind.cluster"] != a.Cfg.KindCluster || !modelImageID(rows[0].Image) || rows[0].State.Running == nil {
			return fmt.Errorf("credential node identity is unknown")
		}
		if *rows[0].State.Running {
			raw, err = a.offlineCapture("docker", "exec", name, "stat", "-c", "%F:%u:%g:%a:%d:%i", "--", modelCredentialDir)
			if err != nil || string(raw) != identity {
				return fmt.Errorf("credential node bind identity changed")
			}
			raw, err = a.offlineCapture("docker", "exec", name, "findmnt", "-rn", "-M", modelCredentialDir, "-o", "FSTYPE,OPTIONS")
			f := strings.Fields(string(raw))
			if err != nil || len(f) != 2 || f[0] != "tmpfs" || !strings.Contains(","+f[1]+",", ",ro,") {
				return fmt.Errorf("credential node bind is not read-only memory")
			}
		}
	}
	return nil
}

func modelImageID(value string) bool {
	if len(value) != 71 || !strings.HasPrefix(value, "sha256:") {
		return false
	}
	_, err := hex.DecodeString(value[7:])
	return err == nil
}

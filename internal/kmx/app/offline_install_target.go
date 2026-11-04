package app

import (
	"fmt"
	"slices"
	"strings"
)

func offlineCRIImage(raw []byte, row offlineImageRow, platform offlinePlatform, ids []string) error {
	var response struct {
		Status struct {
			ID          string   `json:"id"`
			RepoDigests []string `json:"repoDigests"`
		} `json:"status"`
		Info struct {
			ImageSpec struct {
				OS           string `json:"os"`
				Architecture string `json:"architecture"`
				RootFS       struct {
					Type    string   `json:"type"`
					DiffIDs []string `json:"diff_ids"`
				} `json:"rootfs"`
			} `json:"imageSpec"`
		} `json:"info"`
	}
	if offlineJSON(raw, &response, false) != nil || response.Status.ID != row.ConfigDigest || !slices.Contains(response.Status.RepoDigests, row.Repository+"@"+row.ManifestDigest) || response.Info.ImageSpec.OS != platform.OS || response.Info.ImageSpec.Architecture != platform.Architecture || response.Info.ImageSpec.RootFS.Type != "layers" || !slices.Equal(response.Info.ImageSpec.RootFS.DiffIDs, ids) {
		return fmt.Errorf("image %s lacks exact repository/child/config/platform/rootfs CRI identity", row.Component)
	}
	return nil
}

func (a *App) offlineInstallCache(b *offlineNodeBundle) error {
	raw, err := a.offlineCapture("kind", "get", "clusters")
	if err != nil {
		return err
	}
	if !slices.Contains(strings.Fields(string(raw)), a.Cfg.KindCluster) {
		return fmt.Errorf("offline install requires the existing selected kind cluster; no cluster created")
	}
	names, err := a.verifyOfflineClusterNodes(b)
	if err != nil {
		return err
	}
	exists, err := a.verifyOfflineKubeconfig(true)
	if err != nil {
		return err
	}
	if !exists {
		return fmt.Errorf("offline install requires the selected existing kubeconfig; no export attempted")
	}
	if err := a.offlineInstallGuard(); err != nil {
		return err
	}
	raw, err = a.offlineCapture("kubectl", a.kubectl("get", "nodes", "-o", "json")...)
	if err != nil {
		return err
	}
	var list struct {
		Items []struct {
			Metadata struct {
				Name              string
				DeletionTimestamp *string
			}
			Spec   struct{ Unschedulable bool }
			Status struct {
				NodeInfo struct{ OperatingSystem, Architecture string }
			}
		}
	}
	if offlineJSON(raw, &list, false) != nil || len(list.Items) != len(names) {
		return fmt.Errorf("offline Kubernetes/kind node inventory mismatch")
	}
	seen := map[string]bool{}
	schedulable := 0
	for _, node := range list.Items {
		name := node.Metadata.Name
		if !slices.Contains(names, name) || seen[name] || node.Metadata.DeletionTimestamp != nil || node.Status.NodeInfo.OperatingSystem != b.platform.OS || node.Status.NodeInfo.Architecture != b.platform.Architecture {
			return fmt.Errorf("offline Kubernetes node name/platform mismatch")
		}
		seen[name] = true
		if node.Spec.Unschedulable {
			continue
		}
		schedulable++
		for _, row := range b.install.images {
			raw, err := a.offlineCapture(a.Cfg.ContainerEngine, "exec", name, "crictl", "--runtime-endpoint", "unix:///run/containerd/containerd.sock", "--image-endpoint", "unix:///run/containerd/containerd.sock", "inspecti", "-o", "json", row.Repository+"@"+row.ManifestDigest)
			if err != nil {
				return fmt.Errorf("node %s image %s: %w", name, row.Component, err)
			}
			if err := offlineCRIImage(raw, row, b.platform, b.install.diffIDs[row.Component]); err != nil {
				return fmt.Errorf("node %s: %w", name, err)
			}
		}
	}
	if schedulable == 0 {
		return fmt.Errorf("offline install has no schedulable node")
	}
	return nil
}

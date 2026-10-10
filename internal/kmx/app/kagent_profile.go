package app

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// Pins are carried by the invocation, never selected through environment or
// mutable package state. The zero App keeps the historical create profile.
type kagentProfile struct {
	Version, Commit                             string
	ControllerImage, GoImage, PythonImage       string
	ControllerDigests, GoDigests, PythonDigests [3]string
	ControllerConfigDigests, GoConfigDigests    [2]string
	AgentSchema, ModelSchema, RemoteSchema      string
	StrictPlatform                              bool
}

func kagentCreateProfile() kagentProfile {
	return kagentProfile{
		Version: "v0.10.2", Commit: kagentPinnedCommit,
		ControllerImage: kagentControllerImage, GoImage: kagentGoImage, PythonImage: kagentPythonImage,
		ControllerDigests: [3]string{kagentControllerIndexDigest, kagentControllerAMD64Digest, kagentControllerARM64Digest},
		GoDigests:         [3]string{kagentGoIndexDigest, kagentGoAMD64Digest, kagentGoARM64Digest},
		PythonDigests:     [3]string{kagentPythonIndexDigest, kagentPythonAMD64Digest, kagentPythonARM64Digest},
		AgentSchema:       kagentAgentSchemaSHA256, ModelSchema: kagentModelSchemaSHA256,
	}
}

func kagentLiftProfile() kagentProfile {
	return kagentProfile{
		Version: "v0.10.3", Commit: "8878c39ac64421cb665c8e62ecc6411dd12f2fc2",
		ControllerImage:   "ghcr.io/kagent-dev/kagent/controller:0.10.3",
		GoImage:           "ghcr.io/kagent-dev/kagent/golang-adk:0.10.3",
		ControllerDigests: [3]string{"b2572a59386b6bac1f5e652e135fdad54c0ee603d3f9ee60eb7c80bbaedf9b76", "8ca13042c21181ecf280d6f3ca005d58e216a0a56978bb8824fd73cc02b685d8", "fb3e7c0400d73f9abbba9ffc4552c39be253178f7ddece6a840a0a64415e150c"},
		GoDigests:         [3]string{"5ee9592cb7d959538d0f0fe36acf31ba3a06de084f84048b1459c3bdfafcda1f", "7f788d09d5f21ccc3068679310943dcf4f0df19e1a93960423810438f4238b24", "0eb8becbaf323df2c29c8e73a14a775d24bf9c2f9314bd8a12a1da6e57e851fc"},
		// OCI platform-manifest config descriptors, verified separately from the
		// running index/platform imageIDs. Kubelet may report these in status.image.
		ControllerConfigDigests: [2]string{"84a2b0198392941e3739df6751d642026975a26e2fe290864b862e0d88d2d380", "151f6d0a41d3f1148e6cfe150f562b4e0501d0bde6fa2b3aa0b54105df8b5a0a"},
		GoConfigDigests:         [2]string{"869b98f1e423328ff1eab3b65004e1bf5a4893fdb228044026423e32c9388c75", "eb435a67036b5cdc8fa20514a07c8d17964eeb2369dc741096d9129b2efafae3"},
		AgentSchema:             kagentAgentSchemaSHA256, ModelSchema: kagentModelSchemaSHA256,
		RemoteSchema:   "21b622fa018c2e93b8077b7ad2165d39e72b999d136c8e1e028288d787f8e1e9",
		StrictPlatform: true,
	}
}

func (a *App) selectedKagentProfile() kagentProfile {
	if a.kagentProfile != nil {
		return *a.kagentProfile
	}
	return kagentCreateProfile()
}

func (p kagentProfile) tag() string { return strings.TrimPrefix(p.Version, "v") }
func (p kagentProfile) validVersion(version, commit string) bool {
	commit = strings.ToLower(commit)
	if len(commit) < 7 || len(commit) > len(p.Commit) {
		return false
	}
	for _, ch := range commit {
		if ch < '0' || ch > '9' && ch < 'a' || ch > 'f' {
			return false
		}
	}
	return (version == p.Version || version == p.tag()) && strings.HasPrefix(p.Commit, commit)
}
func (p kagentProfile) validateLabels(labels map[string]string) error {
	if p.StrictPlatform && (labels["app.kubernetes.io/version"] == "" || labels["helm.sh/chart"] == "") {
		return fmt.Errorf("Kagent %s requires explicit version and chart labels", p.Version)
	}
	if version := labels["app.kubernetes.io/version"]; version != "" && version != p.tag() {
		return fmt.Errorf("app.kubernetes.io/version must equal %s when present", p.tag())
	}
	if chart := labels["helm.sh/chart"]; chart != "" && chart != "kagent-"+p.tag() {
		return fmt.Errorf("helm.sh/chart must equal kagent-%s when present", p.tag())
	}
	return nil
}
func (a *App) verifyKagentPodPlatform(ctx context.Context, node, declaredImage, statusImage, imageID, repository string, digests []string) error {
	raw, err := a.kagentCapture(ctx, nil, "get", "node", node, "-o", "json")
	if err != nil {
		return fmt.Errorf("cannot qualify Kagent pod platform: %w", err)
	}
	var object struct {
		Kind     string         `json:"kind"`
		Metadata kagentMetadata `json:"metadata"`
		Status   struct {
			NodeInfo struct{ OperatingSystem, Architecture string } `json:"nodeInfo"`
		} `json:"status"`
	}
	if json.Unmarshal(raw, &object) != nil || object.Kind != "Node" || object.Metadata.Name != node || object.Metadata.UID == "" || object.Metadata.DeletionTimestamp != nil || object.Status.NodeInfo.OperatingSystem != "linux" || object.Metadata.Labels["kubernetes.io/os"] != "linux" || object.Metadata.Labels["kubernetes.io/arch"] != object.Status.NodeInfo.Architecture {
		return kagentFailure("unreadable", fmt.Errorf("Kagent pod node has unreadable or unsupported platform identity"))
	}
	index := 1
	switch object.Status.NodeInfo.Architecture {
	case "amd64":
	case "arm64":
		index = 2
	default:
		return kagentFailure("drift", fmt.Errorf("Kagent v0.10.3 supports only linux/amd64 and linux/arm64"))
	}
	if len(digests) != 3 || !validKagentImageID(imageID, repository, []string{digests[0], digests[index]}) {
		return kagentFailure("drift", fmt.Errorf("Kagent pod image digest differs from its qualified node platform"))
	}
	profile := a.selectedKagentProfile()
	platformDigests := []string{digests[0], digests[index]}
	if !profile.imageMatches(declaredImage, declaredImage, repository, platformDigests) {
		return kagentFailure("drift", fmt.Errorf("Kagent declared image reference differs from its qualified node platform"))
	}
	var configs [2]string
	switch repository {
	case kagentControllerImageRepo:
		configs = profile.ControllerConfigDigests
	case kagentGoImageRepo:
		configs = profile.GoConfigDigests
	}
	// Config digests are accepted only as informational status.image spelling,
	// for this exact repository and node platform, never as running imageIDs.
	config := configs[index-1]
	if !profile.imageMatches(statusImage, declaredImage, repository, platformDigests) && (profile.Version != "v0.10.3" || config == "" || statusImage != "sha256:"+config) {
		return kagentFailure("drift", fmt.Errorf("Kagent status image identity differs from its qualified node platform"))
	}
	return nil
}

func (p kagentProfile) imageMatches(actual, canonical, repository string, digests []string) bool {
	if !p.StrictPlatform {
		return actual == canonical
	}
	canonical = repository + ":" + p.tag()
	if actual == canonical {
		return true
	}
	for _, digest := range digests {
		suffix := "@sha256:" + digest
		if actual == canonical+suffix || actual == repository+suffix {
			return true
		}
	}
	return false
}

func (p kagentProfile) runtimeTagMatches(runtimeName, tag string) bool {
	if tag == p.tag() {
		return true
	}
	if !p.StrictPlatform {
		return false
	}
	_, _, digests, err := p.runtimeImage(runtimeName)
	if err != nil {
		return false
	}
	for _, digest := range digests {
		if tag == p.tag()+"@sha256:"+digest {
			return true
		}
	}
	return false
}

func (p kagentProfile) runtimeImage(name string) (string, string, []string, error) {
	switch name {
	case "go":
		return p.GoImage, kagentGoImageRepo, p.GoDigests[:], nil
	case "python":
		if p.Version == "v0.10.3" {
			return "", "", nil, fmt.Errorf("Kagent v0.10.3 deploy/status supports only the Go runtime; Python is render-only and not runtime-qualified")
		}
		return p.PythonImage, kagentPythonImageRepo, p.PythonDigests[:], nil
	default:
		return "", "", nil, fmt.Errorf("unsupported Kagent declarative runtime")
	}
}

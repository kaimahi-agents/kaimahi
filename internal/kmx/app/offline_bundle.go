package app

import (
	"bytes"
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"path/filepath"
	"reflect"
	"regexp"
	"runtime"
	"strings"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/fileaccess"
)

// This catalog authorizes only the outer node, never chart/application inputs.
//
//go:embed offline_profiles/orka-59703d6e-native-v1.json
var offlineProfileJSON []byte

const offlineJSONLimit = 1 << 20

type offlinePlatform struct {
	OS           string `json:"os"`
	Architecture string `json:"architecture"`
}
type offlineKMXIdentity struct {
	Version  string `json:"version"`
	Revision string `json:"revision"`
	SHA256   string `json:"sha256"`
	Modified bool   `json:"-"`
}
type offlineFileRow struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
}
type offlineOrkaIdentity struct {
	Mode     string `json:"mode"`
	Identity string `json:"identity"`
}
type offlineNodeRow struct {
	LocalRef       string  `json:"localRef"`
	Repository     string  `json:"repository"`
	IndexPath      *string `json:"indexPath"`
	IndexDigest    *string `json:"indexDigest"`
	ManifestPath   string  `json:"manifestPath"`
	ManifestDigest string  `json:"manifestDigest"`
	ConfigPath     string  `json:"configPath"`
	ConfigDigest   string  `json:"configDigest"`
}
type offlineImageRow struct {
	Component      string  `json:"component"`
	Repository     string  `json:"repository"`
	IndexPath      *string `json:"indexPath"`
	IndexDigest    *string `json:"indexDigest"`
	ManifestPath   string  `json:"manifestPath"`
	ManifestDigest string  `json:"manifestDigest"`
	ConfigPath     string  `json:"configPath"`
	ConfigDigest   string  `json:"configDigest"`
	PullPolicy     string  `json:"pullPolicy"`
}
type offlineManifest struct {
	SchemaVersion string              `json:"schemaVersion"`
	Profile       string              `json:"profile"`
	ProfileSHA256 string              `json:"profileSha256"`
	Platform      offlinePlatform     `json:"platform"`
	KMX           offlineKMXIdentity  `json:"kmx"`
	Orka          offlineOrkaIdentity `json:"orka"`
	Chart         offlineFileRow      `json:"chart"`
	Artifacts     offlineFileRow      `json:"artifacts"`
	Node          offlineNodeRow      `json:"node"`
	Components    map[string]bool     `json:"components"`
	Images        []offlineImageRow   `json:"images"`
}
type offlineNodePin struct {
	ManifestDigest string `json:"manifestDigest"`
	ConfigDigest   string `json:"configDigest"`
}
type offlineProfile struct {
	Profile      string              `json:"profile"`
	Capabilities []string            `json:"capabilities"`
	Orka         offlineOrkaIdentity `json:"orka"`
	Node         struct {
		Repository  string                    `json:"repository"`
		IndexDigest string                    `json:"indexDigest"`
		Platforms   map[string]offlineNodePin `json:"platforms"`
	} `json:"node"`
}

// All fields stay private and are frozen before external tools run. No layer or
// archive bytes, install authorization or caller-selected catalog survive here.
type offlineNodeBundle struct {
	node     offlineNodeRow
	platform offlinePlatform
	diffIDs  []string
}

func offlineHash(b []byte) string { return fmt.Sprintf("%x", sha256.Sum256(b)) }
func offlineHex(s string, n int) bool {
	if len(s) != n {
		return false
	}
	for _, c := range s {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}
func offlineDigest(s string) bool {
	return strings.HasPrefix(s, "sha256:") && offlineHex(strings.TrimPrefix(s, "sha256:"), 64)
}
func offlinePath(s string) bool {
	return fs.ValidPath(s) && s != "." && !strings.ContainsAny(s, "\\:\x00")
}

// The token walk catches duplicate keys at every depth, including unknown OCI
// metadata. Shape checking separately requires exact case, presence and nulls;
// DisallowUnknownFields alone accepts case aliases and missing required fields.
func offlineJSON(raw []byte, out any, closed bool) error {
	if len(raw) > offlineJSONLimit {
		return fmt.Errorf("JSON exceeds 1 MiB")
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	if err := offlineJSONValue(dec, 0); err != nil {
		return fmt.Errorf("invalid or duplicate JSON")
	}
	if _, err := dec.Token(); err != io.EOF {
		return fmt.Errorf("trailing JSON data")
	}
	if closed {
		var value any
		if json.Unmarshal(raw, &value) != nil || !offlineShape(value, reflect.TypeOf(out).Elem()) {
			return fmt.Errorf("JSON fields must have the complete closed shape")
		}
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("invalid JSON types")
	}
	return nil
}
func offlineJSONValue(dec *json.Decoder, depth int) error {
	if depth > 64 {
		return fmt.Errorf("JSON nesting")
	}
	token, err := dec.Token()
	if err != nil {
		return err
	}
	delim, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	keys := map[string]bool{}
	count := 0
	for dec.More() {
		count++
		if count > 1024 {
			return fmt.Errorf("JSON count")
		}
		if delim == '{' {
			key, err := dec.Token()
			if err != nil {
				return err
			}
			name, ok := key.(string)
			if !ok || keys[name] {
				return fmt.Errorf("JSON key")
			}
			keys[name] = true
		}
		if err := offlineJSONValue(dec, depth+1); err != nil {
			return err
		}
	}
	end, err := dec.Token()
	if err != nil {
		return err
	}
	if delim == '{' && end != json.Delim('}') || delim == '[' && end != json.Delim(']') {
		return fmt.Errorf("JSON delimiter")
	}
	return nil
}
func offlineShape(value any, t reflect.Type) bool {
	if t.Kind() == reflect.Pointer {
		return value == nil || offlineShape(value, t.Elem())
	}
	if value == nil {
		return false
	}
	switch t.Kind() {
	case reflect.Struct:
		obj, ok := value.(map[string]any)
		if !ok {
			return false
		}
		count := 0
		for i := 0; i < t.NumField(); i++ {
			f := t.Field(i)
			name := f.Tag.Get("json")
			if name == "-" {
				continue
			}
			count++
			v, ok := obj[name]
			if !ok || !offlineShape(v, f.Type) {
				return false
			}
		}
		return len(obj) == count
	case reflect.Map:
		obj, ok := value.(map[string]any)
		if !ok {
			return false
		}
		for _, v := range obj {
			if !offlineShape(v, t.Elem()) {
				return false
			}
		}
		return true
	case reflect.Slice:
		list, ok := value.([]any)
		if !ok {
			return false
		}
		for _, v := range list {
			if !offlineShape(v, t.Elem()) {
				return false
			}
		}
		return true
	}
	return true // Unmarshal enforces primitive types after exact field checking.
}

func readOfflineFile(root *fileaccess.Directory, path string) ([]byte, error) {
	if !offlinePath(path) {
		return nil, fmt.Errorf("invalid relative path %q", path)
	}
	ctx := context.Background()
	parent := root
	parts := strings.Split(path, "/")
	for _, part := range parts[:len(parts)-1] {
		next, err := fileaccess.OpenDirectoryAt(ctx, parent, part)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		defer next.Close()
		parent = next
	}
	f, err := fileaccess.OpenRegularAt(ctx, parent, parts[len(parts)-1])
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, offlineJSONLimit+1))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if len(raw) > offlineJSONLimit {
		return nil, fmt.Errorf("%s exceeds 1 MiB", path)
	}
	return raw, nil
}

func loadOfflineNodeBundle(dir string, identity offlineKMXIdentity) (*offlineNodeBundle, error) {
	if runtime.GOOS != "linux" {
		return nil, fmt.Errorf("offline bundles require native Linux")
	}
	if identity.Modified || identity.Version == "" || identity.Version == "unknown" || identity.Version == "(devel)" || !offlineHex(identity.Revision, 40) || !offlineHex(identity.SHA256, 64) {
		return nil, fmt.Errorf("offline bundle requires a known clean kmx version, revision and executable hash")
	}
	path, err := filepath.Abs(filepath.Clean(dir))
	if err != nil {
		return nil, err
	}
	root, err := fileaccess.OpenDirectory(context.Background(), path)
	if err != nil {
		return nil, fmt.Errorf("offline bundle directory: %w", err)
	}
	defer root.Close()
	raw, err := readOfflineFile(root, "install.json")
	if err != nil {
		return nil, err
	}
	var m offlineManifest
	if err := offlineJSON(raw, &m, true); err != nil {
		return nil, fmt.Errorf("install.json: %w", err)
	}
	var p offlineProfile
	if err := offlineJSON(offlineProfileJSON, &p, true); err != nil {
		return nil, fmt.Errorf("embedded offline profile: %w", err)
	}
	if m.SchemaVersion != "kmx-offline/v1" || m.Profile != p.Profile || m.ProfileSHA256 != offlineHash(offlineProfileJSON) || m.Orka != p.Orka {
		return nil, fmt.Errorf("offline schema, profile hash or source identity is not supported")
	}
	if m.Platform.OS != "linux" || m.Platform.Architecture != runtime.GOARCH {
		return nil, fmt.Errorf("offline bundle platform must match native Linux/%s", runtime.GOARCH)
	}
	pin, ok := p.Node.Platforms[m.Platform.Architecture]
	if !ok {
		return nil, fmt.Errorf("unsupported offline node platform")
	}
	if m.KMX != identity {
		return nil, fmt.Errorf("offline kmx version, revision or executable hash mismatch")
	}
	if err := validateOfflineShape(m); err != nil {
		return nil, err
	}
	n := m.Node
	if n.Repository != p.Node.Repository || n.IndexDigest == nil || *n.IndexDigest != p.Node.IndexDigest || n.ManifestDigest != pin.ManifestDigest || n.ConfigDigest != pin.ConfigDigest {
		return nil, fmt.Errorf("node identities do not equal the compiled offline profile")
	}
	if n.LocalRef != "kindest/node:bridge-"+n.ManifestDigest[7:23]+"-"+m.Platform.Architecture {
		return nil, fmt.Errorf("node localRef does not match the pinned platform child")
	}
	index, err := readOfflineFile(root, *n.IndexPath)
	if err != nil {
		return nil, fmt.Errorf("node index: %w", err)
	}
	manifest, err := readOfflineFile(root, n.ManifestPath)
	if err != nil {
		return nil, fmt.Errorf("node manifest: %w", err)
	}
	config, err := readOfflineFile(root, n.ConfigPath)
	if err != nil {
		return nil, fmt.Errorf("node config: %w", err)
	}
	diffIDs, err := validateOfflineOCI(n, m.Platform, index, manifest, config)
	if err != nil {
		return nil, fmt.Errorf("node evidence: %w", err)
	}
	return &offlineNodeBundle{node: n, platform: m.Platform, diffIDs: diffIDs}, nil
}

func validateOfflineShape(m offlineManifest) error {
	for _, f := range []offlineFileRow{m.Chart, m.Artifacts} {
		if !offlinePath(f.Path) || !offlineHex(f.SHA256, 64) {
			return fmt.Errorf("invalid chart/artifacts path or sha256")
		}
	}
	enabled := strings.Fields("controller ai-worker general-worker publisher session-loader scm-egress-proxy store")
	disabled := strings.Fields("acp-codex-runtime acp-claude-runtime acp-copilot-runtime acp-opencode-runtime provider-proxy toolboxes execution-workspace-dispatch substrate-mcp-tools connectors dashboard agent-hosts ingress")
	if len(m.Components) != len(enabled)+len(disabled) {
		return fmt.Errorf("components must contain the complete fixed map")
	}
	for _, list := range []struct {
		names []string
		value bool
	}{{enabled, true}, {disabled, false}} {
		for _, name := range list.names {
			v, ok := m.Components[name]
			if !ok || v != list.value {
				return fmt.Errorf("unsupported component %s", name)
			}
		}
	}
	repos := map[string]string{"controller": "ghcr.io/orka-agents/orka", "ai-worker": "ghcr.io/orka-agents/orka/ai-worker", "general-worker": "ghcr.io/orka-agents/orka/general-worker", "publisher": "ghcr.io/orka-agents/orka/workspace-publisher", "session-loader": "docker.io/library/busybox"}
	if len(m.Images) != 5 {
		return fmt.Errorf("images must contain exactly five enabled components")
	}
	for _, row := range m.Images {
		if repos[row.Component] == "" || repos[row.Component] != row.Repository || row.PullPolicy != "IfNotPresent" {
			return fmt.Errorf("unsupported or duplicate image component/repository/policy")
		}
		delete(repos, row.Component)
		if err := validateOfflinePaths(row.IndexPath, row.IndexDigest, row.ManifestPath, row.ManifestDigest, row.ConfigPath, row.ConfigDigest); err != nil {
			return fmt.Errorf("image %s: %w", row.Component, err)
		}
	}
	return validateOfflinePaths(m.Node.IndexPath, m.Node.IndexDigest, m.Node.ManifestPath, m.Node.ManifestDigest, m.Node.ConfigPath, m.Node.ConfigDigest)
}
func validateOfflinePaths(ip, id *string, mp, md, cp, cd string) error {
	if (ip == nil) != (id == nil) || ip != nil && (!offlinePath(*ip) || !offlineDigest(*id)) || !offlinePath(mp) || !offlinePath(cp) || !offlineDigest(md) || !offlineDigest(cd) {
		return fmt.Errorf("invalid OCI paths, digests or index null pairing")
	}
	return nil
}

type offlineDescriptor struct {
	MediaType string                                     `json:"mediaType"`
	Digest    string                                     `json:"digest"`
	Size      int64                                      `json:"size"`
	Platform  struct{ OS, Architecture, Variant string } `json:"platform"`
}

func validateOfflineOCI(n offlineNodeRow, p offlinePlatform, index, manifest, config []byte) ([]string, error) {
	if "sha256:"+offlineHash(manifest) != n.ManifestDigest || "sha256:"+offlineHash(config) != n.ConfigDigest {
		return nil, fmt.Errorf("manifest/config bytes differ from pinned digests")
	}
	var m struct {
		SchemaVersion int
		MediaType     string
		Config        offlineDescriptor
	}
	var c struct {
		OS, Architecture string
		RootFS           struct {
			Type    string
			DiffIDs []string `json:"diff_ids"`
		} `json:"rootfs"`
	}
	if offlineJSON(manifest, &m, false) != nil || offlineJSON(config, &c, false) != nil {
		return nil, fmt.Errorf("malformed manifest/config JSON")
	}
	if m.SchemaVersion != 2 || m.MediaType != "application/vnd.oci.image.manifest.v1+json" && m.MediaType != "application/vnd.docker.distribution.manifest.v2+json" || m.Config.Digest != n.ConfigDigest || m.Config.Size != int64(len(config)) {
		return nil, fmt.Errorf("config descriptor digest/size or manifest type mismatch")
	}
	if c.OS != p.OS || c.Architecture != p.Architecture || c.RootFS.Type != "layers" || len(c.RootFS.DiffIDs) == 0 {
		return nil, fmt.Errorf("config platform/rootfs mismatch")
	}
	for _, id := range c.RootFS.DiffIDs {
		if !offlineDigest(id) {
			return nil, fmt.Errorf("invalid rootfs diff ID")
		}
	}
	if n.IndexDigest != nil {
		if "sha256:"+offlineHash(index) != *n.IndexDigest {
			return nil, fmt.Errorf("index bytes differ from pinned digest")
		}
		var idx struct {
			SchemaVersion int
			MediaType     string
			Manifests     []offlineDescriptor
		}
		if offlineJSON(index, &idx, false) != nil || idx.SchemaVersion != 2 || idx.MediaType != "application/vnd.oci.image.index.v1+json" && idx.MediaType != "application/vnd.docker.distribution.manifest.list.v2+json" {
			return nil, fmt.Errorf("invalid index JSON/type")
		}
		matches := 0
		for _, d := range idx.Manifests {
			variant := d.Platform.Variant
			if d.Platform.OS != p.OS || d.Platform.Architecture != p.Architecture || !(variant == "" || p.Architecture == "arm64" && variant == "v8") {
				continue
			}
			matches++
			if d.Digest != n.ManifestDigest || d.Size != int64(len(manifest)) || d.MediaType != m.MediaType {
				return nil, fmt.Errorf("index child membership/size/type mismatch")
			}
		}
		if matches != 1 {
			return nil, fmt.Errorf("index must select exactly one native platform child")
		}
	}
	return c.RootFS.DiffIDs, nil
}

// OCI repositories are fixed by the catalog/schema; this pattern is only for
// engine-returned container names, never a way to authorize another image.
var offlineContainerName = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]*$`)

package app

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"unicode"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/config"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/fileaccess"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/guard"
	"go.yaml.in/yaml/v3"
)

// offlineTarget resolves only CLI configuration, before any engine/probe I/O.
// The selected URI is frozen on copies; stat is locality evidence, not inode
// containment against a concurrently replaced socket or a local proxy.
func (a *App) offlineTarget() (*App, error) {
	engine := a.Cfg.ContainerEngine
	if _, err := exec.LookPath(engine); err != nil {
		return nil, fmt.Errorf("offline endpoint: %s is not on PATH", engine)
	}
	env := map[string]string{}
	for _, entry := range a.Run.Command(engine).Environ() {
		key, value, _ := strings.Cut(entry, "=")
		env[key] = value
	}
	var host string
	var clear []string
	switch engine {
	case "docker":
		raw, err := a.offlineCapture(engine, "context", "inspect")
		if err != nil {
			return nil, err
		}
		var rows []struct {
			Endpoints struct {
				Docker struct{ Host string } `json:"docker"`
			}
		}
		if offlineJSON(raw, &rows, false) != nil || len(rows) != 1 {
			return nil, fmt.Errorf("offline endpoint: ambiguous Docker context metadata")
		}
		host = rows[0].Endpoints.Docker.Host
		clear = []string{"DOCKER_HOST", "DOCKER_CONTEXT", "DOCKER_TLS", "DOCKER_TLS_VERIFY", "DOCKER_CERT_PATH"}
	case "podman":
		connection := env["CONTAINER_CONNECTION"]
		host = env["CONTAINER_HOST"]
		if connection != "" && host != "" {
			return nil, fmt.Errorf("offline endpoint: select only one of CONTAINER_CONNECTION or CONTAINER_HOST")
		}
		if connection != "" {
			raw, err := a.offlineCapture(engine, "system", "connection", "list", "--format=json")
			if err != nil {
				return nil, err
			}
			var rows []struct{ Name, URI string }
			if offlineJSON(raw, &rows, false) != nil {
				return nil, fmt.Errorf("offline endpoint: invalid Podman connection metadata")
			}
			matches := 0
			for _, row := range rows {
				if row.Name == connection {
					host = row.URI
					matches++
				}
			}
			if matches != 1 {
				return nil, fmt.Errorf("offline endpoint: selected Podman connection is missing or ambiguous")
			}
		}
		if host == "" {
			return nil, fmt.Errorf("offline endpoint: Podman requires explicit CONTAINER_HOST or CONTAINER_CONNECTION socket selection")
		}
		clear = []string{"CONTAINER_HOST", "CONTAINER_CONNECTION"}
	default:
		return nil, fmt.Errorf("offline endpoint: unsupported container engine")
	}
	if !offlineLocalSocket(host) {
		return nil, fmt.Errorf("offline endpoint: selected engine must use an existing local filesystem Unix socket")
	}
	local, runner := *a, *a.Run
	clear = append(clear, "KIND_EXPERIMENTAL_PROVIDER")
	runner.Env = nil
	for _, entry := range a.Run.Env {
		key, _, _ := strings.Cut(entry, "=")
		drop := false
		for _, name := range clear {
			if key == name {
				drop = true
				break
			}
		}
		if !drop {
			runner.Env = append(runner.Env, entry)
		}
	}
	runner.Unset = append(append([]string(nil), a.Run.Unset...), clear...)
	key := "DOCKER_HOST"
	if engine == "podman" {
		key = "CONTAINER_HOST"
	}
	runner.Env = append(runner.Env, key+"="+host, "KIND_EXPERIMENTAL_PROVIDER="+engine)
	local.Run = &runner
	return &local, nil
}

func offlineLocalSocket(host string) bool {
	// No percent escaping: multiple CLI URL parsers must agree on one path.
	if !strings.HasPrefix(host, "unix:///") || strings.ContainsAny(host, "%?#") || strings.ContainsFunc(host, unicode.IsControl) {
		return false
	}
	u, err := url.Parse(host)
	if err != nil || u.Scheme != "unix" || u.Host != "" || u.User != nil || u.Opaque != "" || !filepath.IsAbs(u.Path) || strings.HasPrefix(u.Path, "//") {
		return false
	}
	info, err := os.Stat(u.Path) // follow /var/run -> /run and other local aliases
	return err == nil && info.Mode()&os.ModeSocket != 0
}

func (a *App) offlineKubeconfig() (*guard.Kubeconfig, error) {
	raw, err := a.offlineCapture("kubectl", "config", "view", "-o", "json")
	if err != nil {
		return nil, err
	}
	var cfg guard.Kubeconfig
	if offlineJSON(raw, &cfg, false) != nil {
		return nil, fmt.Errorf("offline kubeconfig: invalid merged local configuration")
	}
	if _, err := offlineContextExists(&cfg, a.Cfg.KubeContext); err != nil {
		return nil, err
	}
	return &cfg, nil
}

func (a *App) offlineGuardCreate(action, command string) error {
	cfg, err := a.offlineKubeconfig()
	if err != nil {
		return err
	}
	if a.InvocationCommand != "" {
		command = a.InvocationCommand
	}
	return guard.CheckContext(a.operationContext(), cfg, guard.Request{
		Action: action, Context: a.Cfg.KubeContext, Source: a.Cfg.ContextSource,
		Namespaces: config.GuardNamespaceHint, Confirm: a.Cfg.Confirm,
		Command: command, CreatesContext: true,
	}, a.Err, a.Stdin)
}

// Observe the normal merged loader result, not a new raw-file merge policy.
// A named but broken reference must never be treated as permission to export.
func offlineContextExists(cfg *guard.Kubeconfig, target string) (bool, error) {
	contexts, clusters, cluster := 0, 0, ""
	for _, row := range cfg.Contexts {
		if row.Name == target {
			contexts++
			cluster = row.Context.Cluster
		}
	}
	if contexts == 0 {
		return false, nil
	}
	if contexts != 1 || cluster == "" {
		return false, fmt.Errorf("offline kubeconfig: selected context is broken or ambiguous")
	}
	for _, row := range cfg.Clusters {
		if row.Name == cluster {
			clusters++
			if !offlineServerURL(row.Cluster.Server) {
				return false, fmt.Errorf("offline kubeconfig: selected cluster has invalid server")
			}
		}
	}
	if clusters != 1 {
		return false, fmt.Errorf("offline kubeconfig: selected cluster reference is broken or ambiguous")
	}
	return true, nil
}

type offlineClusterConnection struct {
	Server        string `json:"server" yaml:"server"`
	CAData        string `json:"certificate-authority-data" yaml:"certificate-authority-data"`
	CAFile        string `json:"certificate-authority" yaml:"certificate-authority"`
	TLSServerName string `json:"tls-server-name" yaml:"tls-server-name"`
	Insecure      bool   `json:"insecure-skip-tls-verify" yaml:"insecure-skip-tls-verify"`
	ProxyURL      string `json:"proxy-url" yaml:"proxy-url"`
}

func offlineServerURL(server string) bool {
	u, err := url.Parse(server)
	return err == nil && (u.Scheme == "https" || u.Scheme == "http") && u.Hostname() != "" && u.User == nil && !strings.ContainsFunc(server, unicode.IsControl)
}

func (a *App) offlineCA(c offlineClusterConnection) (data []byte, err error) {
	defer func() {
		if cancelled := a.operationContext().Err(); cancelled != nil {
			data, err = nil, cancelled
		}
	}()
	if err := a.operationContext().Err(); err != nil {
		return nil, err
	}
	if c.CAData != "" {
		data, err := base64.StdEncoding.DecodeString(c.CAData)
		if err != nil {
			return nil, fmt.Errorf("offline kubeconfig: invalid CA data")
		}
		return data, nil
	}
	if c.CAFile == "" {
		return nil, nil
	}
	// The cluster-only kubectl projection can retain a relative reference
	// without its source-file origin. Refuse rather than guess that origin.
	if !filepath.IsAbs(c.CAFile) {
		return nil, fmt.Errorf("offline kubeconfig: unresolved CA file origin")
	}
	path, err := filepath.EvalSymlinks(c.CAFile)
	if err != nil {
		return nil, fmt.Errorf("offline kubeconfig: unavailable CA file")
	}
	dir, err := fileaccess.OpenDirectory(a.operationContext(), filepath.Dir(path))
	if err != nil {
		return nil, fmt.Errorf("offline kubeconfig: unavailable CA file")
	}
	defer dir.Close()
	file, err := fileaccess.OpenRegularAt(a.operationContext(), dir, filepath.Base(path))
	if err != nil {
		return nil, fmt.Errorf("offline kubeconfig: unavailable regular CA file")
	}
	defer file.Close()
	data, err = io.ReadAll(io.LimitReader(file, offlineJSONLimit+1))
	if err != nil || len(data) > offlineJSONLimit {
		return nil, fmt.Errorf("offline kubeconfig: unreadable or oversized CA file")
	}
	return data, nil
}

func offlineExpectedConnection(raw []byte, target string) (offlineClusterConnection, error) {
	var cfg struct {
		Contexts []struct {
			Name    string `yaml:"name"`
			Context struct {
				Cluster string `yaml:"cluster"`
			} `yaml:"context"`
		} `yaml:"contexts"`
		Clusters []struct {
			Name    string                   `yaml:"name"`
			Cluster offlineClusterConnection `yaml:"cluster"`
		} `yaml:"clusters"`
	}
	bad := fmt.Errorf("offline kubeconfig: expected kind configuration is unavailable or ambiguous")
	decoder := yaml.NewDecoder(bytes.NewReader(raw))
	if decoder.Decode(&cfg) != nil {
		return offlineClusterConnection{}, bad
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		return offlineClusterConnection{}, bad
	}
	contexts, clusters, name := 0, 0, ""
	for _, row := range cfg.Contexts {
		if row.Name == target {
			contexts++
			name = row.Context.Cluster
		}
	}
	if contexts != 1 || name == "" {
		return offlineClusterConnection{}, bad
	}
	var selected offlineClusterConnection
	for _, row := range cfg.Clusters {
		if row.Name == name {
			clusters++
			selected = row.Cluster
		}
	}
	if clusters != 1 || !offlineServerURL(selected.Server) {
		return offlineClusterConnection{}, bad
	}
	return selected, nil
}

// verifyOfflineKubeconfig runs after actual-node verification and before any
// export, start or API request. It returns genuine absence for the normal
// export path; an existing entry requires read-only proof from the node.
func (a *App) verifyOfflineKubeconfig(listed bool) (bool, error) {
	cfg, err := a.offlineKubeconfig()
	if err != nil {
		return false, err
	}
	exists, err := offlineContextExists(cfg, a.Cfg.KubeContext)
	if err != nil || !exists {
		return exists, err
	}
	if !listed {
		return false, fmt.Errorf("offline kubeconfig: existing selected context cannot be overwritten without a verified kind node")
	}
	raw, err := a.offlineCapture("kubectl", a.kubectl("config", "view", "--raw", "--minify", "-o", "jsonpath={.clusters[0].cluster}")...)
	if err != nil {
		return false, err
	}
	var selected offlineClusterConnection
	if offlineJSON(raw, &selected, false) != nil || !offlineServerURL(selected.Server) {
		return false, fmt.Errorf("offline kubeconfig: selected cluster projection is invalid")
	}
	raw, err = a.offlineCapture("kind", "get", "kubeconfig", "--name", a.Cfg.KindCluster)
	if err != nil {
		return false, fmt.Errorf("offline kubeconfig: cannot prove expected configuration before node start: %w", err)
	}
	expected, err := offlineExpectedConnection(raw, a.Cfg.KubeContext)
	if err != nil {
		return false, err
	}
	actualCA, err := a.offlineCA(selected)
	if err != nil {
		return false, err
	}
	expectedCA, err := a.offlineCA(expected)
	if err != nil {
		return false, err
	}
	if selected.Server != expected.Server || !bytes.Equal(actualCA, expectedCA) ||
		selected.TLSServerName != expected.TLSServerName || selected.Insecure != expected.Insecure || selected.ProxyURL != expected.ProxyURL {
		return false, fmt.Errorf("offline kubeconfig: selected cluster connection does not match the verified kind node; refusing without overwrite")
	}
	return true, nil
}

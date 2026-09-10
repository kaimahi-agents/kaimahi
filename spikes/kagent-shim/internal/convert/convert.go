// Package convert implements a deliberately narrow, offline compatibility spike
// from kagent v0.10.1 to Orka v0.1.3. It never resolves Secrets or contacts a cluster.
package convert

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"net/url"
	"regexp"
	"slices"
	"strings"

	"go.yaml.in/yaml/v3"
)

const adapterName = "kagent-shim-adapter"
const provenanceDescription = "kagent-shim.kaimahi.ai/source-description"

type resource struct {
	kind, name, namespace string
	node                  *yaml.Node
}

func (r *resource) path() string     { return r.kind + "/" + r.name }
func (r *resource) spec() *yaml.Node { return field(r.node, "spec") }

type secretRef struct {
	Name string `json:"name"`
	Key  string `json:"key"`
}
type route struct {
	URL     string               `json:"url"`
	Name    string               `json:"name"`
	Headers map[string]secretRef `json:"headers,omitempty"`
}

// Convert returns a complete YAML bundle or no bytes and a refusal. Source
// resources require explicit namespaces; snapshot server keys are source names.
func Convert(input, schemas []byte) ([]byte, error) {
	resources, err := parseResources(input)
	if err != nil {
		return nil, err
	}
	snapshot, err := parseSnapshot(schemas)
	if err != nil {
		return nil, err
	}
	var agent, model *resource
	for _, r := range resources {
		switch r.kind {
		case "Agent":
			if agent != nil {
				return nil, fmt.Errorf("exactly one Agent required")
			}
			agent = r
		case "ModelConfig":
			if model != nil {
				return nil, fmt.Errorf("exactly one ModelConfig required")
			}
			model = r
		}
	}
	if agent == nil || model == nil {
		return nil, fmt.Errorf("exactly one Agent and one ModelConfig required")
	}
	ns := agent.namespace
	for _, r := range resources {
		if r.namespace != ns {
			return nil, fmt.Errorf("%s.metadata.namespace: cross-namespace resources unsupported", r.path())
		}
	}
	declarative := field(agent.spec(), "declarative")
	if value(declarative, "modelConfig") != model.name {
		return nil, fmt.Errorf("%s.spec.declarative.modelConfig: missing ModelConfig dependency", agent.path())
	}
	provider, err := convertProvider(model)
	if err != nil {
		return nil, err
	}
	used := map[*resource]bool{agent: true, model: true}
	prompt, promptCM, err := convertPrompt(agent, resources)
	if err != nil {
		return nil, err
	}
	if promptCM != nil {
		used[promptCM] = true
	}

	routes := map[string]route{}
	tools := map[string]any{}
	refs := []map[string]any{}
	usedServers := map[string]bool{}
	usedSourceNames := map[string]bool{}
	for i, toolRef := range field(declarative, "tools").Content {
		ref := field(toolRef, "mcpServer")
		path := fmt.Sprintf("%s.spec.declarative.tools[%d].mcpServer", agent.path(), i)
		if remoteNS := field(ref, "namespace"); remoteNS != nil && remoteNS.Value != ns {
			return nil, fmt.Errorf("%s.namespace: cross-namespace reference unsupported", path)
		}
		serverName := value(ref, "name")
		remote := lookup(resources, "RemoteMCPServer", serverName)
		if remote == nil {
			return nil, fmt.Errorf("%s.name: missing RemoteMCPServer dependency", path)
		}
		if usedServers[serverName] {
			return nil, fmt.Errorf("%s: duplicate tool server reference", path)
		}
		usedServers[serverName], used[remote] = true, true
		serverSchema, ok := snapshot[serverName]
		if !ok {
			return nil, fmt.Errorf("%s: missing schema snapshot for server", path)
		}
		remoteSpec := remote.spec()
		remoteURL := value(remoteSpec, "url")
		if err := safeURL(remoteURL, remote.path()+".spec.url"); err != nil {
			return nil, err
		}
		headers, err := convertHeaders(remote)
		if err != nil {
			return nil, err
		}
		for j, selected := range field(ref, "toolNames").Content {
			sourceName := selected.Value
			toolPath := fmt.Sprintf("%s.toolNames[%d]", path, j)
			if isBuiltIn(sourceName) {
				return nil, fmt.Errorf("%s: source name collides with Orka v0.1.3 built-in", toolPath)
			}
			// kagent exposes source names directly. Refuse ambiguity instead of
			// inventing disambiguation semantics across servers.
			if usedSourceNames[sourceName] {
				return nil, fmt.Errorf("%s: duplicate tool source name", toolPath)
			}
			usedSourceNames[sourceName] = true
			schema, ok := serverSchema[sourceName]
			if !ok {
				return nil, fmt.Errorf("%s: missing tool schema", toolPath)
			}
			name := generatedName(serverName, sourceName)
			if isBuiltIn(name) || tools[name] != nil {
				return nil, fmt.Errorf("%s: generated name collision", toolPath)
			}
			routes[name] = route{URL: remoteURL, Name: sourceName, Headers: headers}
			tool := outputResource("Tool", name, ns, map[string]any{
				"description": value(schema, "description"), "parameters": field(schema, "inputSchema"),
				"http": map[string]any{"method": "POST", "url": "http://" + adapterName + "." + ns + ".svc:8080/tools/" + name, "timeout": "60s"},
			})
			annotateDescription(tool, field(remoteSpec, "description"))
			tools[name] = tool
			refs = append(refs, map[string]any{"name": name})
		}
	}
	for _, r := range resources {
		if !used[r] {
			return nil, fmt.Errorf("%s: unreferenced input resource", r.path())
		}
	}
	for _, server := range slices.Sorted(maps.Keys(snapshot)) {
		if !usedServers[server] {
			return nil, fmt.Errorf("schemas.%s: unreferenced server snapshot", server)
		}
	}
	// Resource emission order is independent of source document order. Keep
	// the Agent's positive selection order and raw prompt/schema contents.
	outAgent := outputResource("Agent", agent.name, ns, map[string]any{
		"providerRef":  map[string]any{"name": model.name},
		"model":        map[string]any{"name": value(model.spec(), "model")},
		"systemPrompt": prompt, "tools": refs,
	})
	annotateDescription(outAgent, field(agent.spec(), "description"))
	routeJSON, err := json.Marshal(struct {
		Tools map[string]route `json:"tools"`
	}{Tools: routes})
	if err != nil {
		return nil, fmt.Errorf("encode adapter routes: %w", err)
	}
	config := map[string]any{"apiVersion": "v1", "kind": "ConfigMap", "metadata": map[string]any{"name": adapterName, "namespace": ns}, "data": map[string]string{"routes.json": string(routeJSON)}}
	docs := []any{provider}
	if promptCM != nil {
		docs = append(docs, promptCM.node)
	}
	for _, name := range slices.Sorted(maps.Keys(tools)) {
		docs = append(docs, tools[name])
	}
	docs = append(docs, config, outAgent)
	var output bytes.Buffer
	encoder := yaml.NewEncoder(&output)
	encoder.SetIndent(2)
	for _, doc := range docs {
		if err := encoder.Encode(doc); err != nil {
			return nil, fmt.Errorf("encode output: %w", err)
		}
	}
	if err := encoder.Close(); err != nil {
		return nil, fmt.Errorf("close output encoder: %w", err)
	}
	return output.Bytes(), nil
}

func parseResources(input []byte) ([]*resource, error) {
	decoder := yaml.NewDecoder(bytes.NewReader(input))
	var resources []*resource
	seen := map[string]bool{}
	for index := 1; ; index++ {
		var document yaml.Node
		if err := decoder.Decode(&document); err == io.EOF {
			break
		} else if err != nil {
			// Parser diagnostics can contain user scalar values. Do not echo
			// malformed credential-bearing input to stderr.
			return nil, fmt.Errorf("document[%d]: invalid YAML", index)
		}
		if len(document.Content) != 1 {
			return nil, fmt.Errorf("document[%d]: empty document", index)
		}
		n := document.Content[0]
		kind, name := value(n, "kind"), value(field(n, "metadata"), "name")
		path := fmt.Sprintf("document[%d]", index)
		if kind != "" && name != "" {
			path = kind + "/" + name
		}
		if err := checkTree(n, path); err != nil {
			return nil, err
		}
		rule, ok := resourceRules[kind]
		if !ok {
			return nil, fmt.Errorf("%s: unsupported resource kind %q (Secret documents are never accepted)", path, kind)
		}
		if err := rule.validate(n, path); err != nil {
			return nil, err
		}
		namespace := value(field(n, "metadata"), "namespace")
		if err := validName(name, path+".metadata.name"); err != nil {
			return nil, err
		}
		if len(namespace) > 63 || !dnsLabel.MatchString(namespace) {
			return nil, fmt.Errorf("%s.metadata.namespace: invalid namespace", path)
		}
		key := kind + "/" + namespace + "/" + name
		if seen[key] {
			return nil, fmt.Errorf("%s: duplicate resource", path)
		}
		seen[key] = true
		resources = append(resources, &resource{kind: kind, name: name, namespace: namespace, node: n})
	}
	return resources, nil
}

func parseSnapshot(input []byte) (map[string]map[string]*yaml.Node, error) {
	if !json.Valid(input) {
		return nil, fmt.Errorf("schemas: expected valid JSON snapshot")
	}
	var document yaml.Node
	if err := yaml.Unmarshal(input, &document); err != nil || len(document.Content) != 1 {
		return nil, fmt.Errorf("schemas: invalid JSON snapshot")
	}
	n := document.Content[0]
	if err := checkTree(n, "schemas"); err != nil {
		return nil, err
	}
	if err := snapshotRule.validate(n, "schemas"); err != nil {
		return nil, err
	}
	out := map[string]map[string]*yaml.Node{}
	for i := 0; i < len(n.Content); i += 2 {
		server := n.Content[i].Value
		if err := validName(server, "schemas."+server); err != nil {
			return nil, err
		}
		tools := map[string]*yaml.Node{}
		for j, tool := range field(n.Content[i+1], "tools").Content {
			name := value(tool, "name")
			if tools[name] != nil {
				return nil, fmt.Errorf("schemas.%s.tools[%d].name: duplicate tool schema", server, j)
			}
			tools[name] = tool
		}
		out[server] = tools
	}
	return out, nil
}

func convertProvider(model *resource) (map[string]any, error) {
	spec, path := model.spec(), model.path()+".spec"
	provider := value(spec, "provider")
	selected, other := "openAI", "anthropic"
	if provider == "Anthropic" {
		selected, other = other, selected
	}
	if field(spec, other) != nil {
		return nil, fmt.Errorf("%s.%s: provider configuration does not match provider", path, other)
	}
	if err := validName(value(spec, "apiKeySecret"), path+".apiKeySecret"); err != nil {
		return nil, err
	}
	if err := validKey(value(spec, "apiKeySecretKey"), path+".apiKeySecretKey"); err != nil {
		return nil, err
	}
	out := map[string]any{"type": strings.ToLower(provider), "defaultModel": value(spec, "model"), "secretRef": map[string]string{"name": value(spec, "apiKeySecret"), "key": value(spec, "apiKeySecretKey")}}
	if base := field(field(spec, selected), "baseUrl"); base != nil {
		if err := safeURL(base.Value, path+"."+selected+".baseUrl"); err != nil {
			return nil, err
		}
		out["baseURL"] = base.Value
	}
	return outputResource("Provider", model.name, model.namespace, out), nil
}

func convertPrompt(agent *resource, resources []*resource) (map[string]any, *resource, error) {
	decl := field(agent.spec(), "declarative")
	literal, from := field(decl, "systemMessage"), field(decl, "systemMessageFrom")
	path := agent.path() + ".spec.declarative"
	if (literal == nil) == (from == nil) {
		return nil, nil, fmt.Errorf("%s: exactly one of systemMessage or systemMessageFrom required", path)
	}
	if literal != nil {
		return map[string]any{"inline": literal.Value}, nil, nil
	}
	name, key := value(from, "name"), value(from, "key")
	if err := validName(name, path+".systemMessageFrom.name"); err != nil {
		return nil, nil, err
	}
	if err := validKey(key, path+".systemMessageFrom.key"); err != nil {
		return nil, nil, err
	}
	cm := lookup(resources, "ConfigMap", name)
	if cm == nil {
		return nil, nil, fmt.Errorf("%s.systemMessageFrom: missing ConfigMap dependency", path)
	}
	if name == adapterName {
		return nil, nil, fmt.Errorf("%s: ConfigMap name collision with adapter routes", cm.path())
	}
	data := field(cm.node, "data")
	prompt := field(data, key)
	if prompt == nil {
		return nil, nil, fmt.Errorf("%s.systemMessageFrom.key: missing ConfigMap key", path)
	}
	if strings.TrimSpace(prompt.Value) == "" {
		return nil, nil, fmt.Errorf("%s.data.%s: nonempty prompt required", cm.path(), key)
	}
	for i := 0; i < len(data.Content); i += 2 {
		if data.Content[i].Value != key {
			return nil, nil, fmt.Errorf("%s.data.%s: unreferenced ConfigMap key", cm.path(), data.Content[i].Value)
		}
	}
	return map[string]any{"configMapRef": map[string]string{"name": name, "key": key}}, cm, nil
}

func convertHeaders(remote *resource) (map[string]secretRef, error) {
	n := field(remote.spec(), "headersFrom")
	if n == nil {
		return nil, nil
	}
	out := map[string]secretRef{}
	seen := map[string]bool{}
	for i, header := range n.Content {
		path := fmt.Sprintf("%s.spec.headersFrom[%d]", remote.path(), i)
		name := value(header, "name")
		if !headerToken.MatchString(name) {
			return nil, fmt.Errorf("%s.name: invalid HTTP header name", path)
		}
		canonical := strings.ToLower(name)
		if seen[canonical] {
			return nil, fmt.Errorf("%s.name: duplicate header name", path)
		}
		seen[canonical] = true
		// These headers alter transport/framing or are owned by the MCP SDK,
		// rather than representing caller-supplied authentication material.
		if strings.HasPrefix(canonical, "mcp-") {
			return nil, fmt.Errorf("%s.name: transport/protocol header unsupported", path)
		}
		switch canonical {
		case "host", "connection", "content-length", "transfer-encoding", "trailer", "upgrade", "te", "accept", "content-type", "accept-encoding", "keep-alive", "proxy-authorization", "proxy-connection", "idempotency-key", "x-idempotency-key", "last-event-id":
			return nil, fmt.Errorf("%s.name: transport/protocol header unsupported", path)
		}
		source := field(header, "valueFrom")
		ref := secretRef{Name: value(source, "name"), Key: value(source, "key")}
		if err := validName(ref.Name, path+".valueFrom.name"); err != nil {
			return nil, err
		}
		if err := validKey(ref.Key, path+".valueFrom.key"); err != nil {
			return nil, err
		}
		out[name] = ref
	}
	return out, nil
}

func lookup(resources []*resource, kind, name string) *resource {
	for _, r := range resources {
		if r.kind == kind && r.name == name {
			return r
		}
	}
	return nil
}

func outputResource(kind, name, namespace string, spec map[string]any) map[string]any {
	return map[string]any{"apiVersion": "core.orka.ai/v1alpha1", "kind": kind, "metadata": map[string]any{"name": name, "namespace": namespace}, "spec": spec}
}

func annotateDescription(out map[string]any, description *yaml.Node) {
	if description != nil {
		out["metadata"].(map[string]any)["annotations"] = map[string]string{provenanceDescription: description.Value}
	}
}

func generatedName(server, tool string) string {
	// NUL separates the components unambiguously; 128 hash bits fit DNS and
	// LLM function-name limits. Any collision inside the bundle is refused.
	hash := sha256.Sum256([]byte(server + "\x00" + tool))
	return "shim-" + hex.EncodeToString(hash[:16])
}

var (
	dnsLabel    = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`)
	dataKey     = regexp.MustCompile(`^[a-zA-Z0-9._-]+$`)
	headerToken = regexp.MustCompile("^[!#$%&'*+.^_`|~0-9A-Za-z-]+$")
)

func validName(name, path string) error {
	if len(name) == 0 || len(name) > 253 {
		return fmt.Errorf("%s: invalid Kubernetes name", path)
	}
	for _, part := range strings.Split(name, ".") {
		if len(part) > 63 || !dnsLabel.MatchString(part) {
			return fmt.Errorf("%s: invalid Kubernetes name", path)
		}
	}
	return nil
}

func validKey(key, path string) error {
	if len(key) > 253 || !dataKey.MatchString(key) || key == "." || strings.HasPrefix(key, "..") {
		return fmt.Errorf("%s: invalid ConfigMap/Secret key", path)
	}
	return nil
}

func safeURL(raw, path string) error {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || strings.Contains(raw, "#") {
		return fmt.Errorf("%s: require absolute HTTP(S) URL without userinfo, query, or fragment; inline credentials unsupported", path)
	}
	return nil
}

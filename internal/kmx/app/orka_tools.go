package app

import (
	"context"
	"encoding/json"
	"net/url"
	"slices"
	"sort"
	"strings"
	"time"

	suitecore "github.com/agent-primitives/suite/core"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/localtools"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/scaffold"
	godigest "github.com/opencontainers/go-digest"
)

// OrkaToolRequirement is an explicit native-kind selection, not an execution
// binding. Disabled requirements do not resolve runtime dependencies.
type OrkaToolRequirement struct {
	Kind, Name string
	Enabled    bool
}
type OrkaToolOptions struct {
	Namespace    string
	Requirements []OrkaToolRequirement
}

// OrkaToolRow is payload-free availability. Available is not execution proof.
type OrkaToolRow struct {
	Kind            string `json:"kind"`
	Name            string `json:"name"`
	Availability    string `json:"availability"`
	Reason          string `json:"reason"`
	Source          string `json:"source"`
	Namespace       string `json:"namespace,omitempty"`
	UID             string `json:"uid,omitempty"`
	ResourceVersion string `json:"resourceVersion,omitempty"`
	SpecDigest      string `json:"specDigest,omitempty"`
	Generation      int64  `json:"generation,omitempty"`
}
type OrkaToolTarget struct {
	Context      string `json:"context"`
	Namespace    string `json:"namespace"`
	ClusterUID   string `json:"clusterUid,omitempty"`
	NamespaceUID string `json:"namespaceUid,omitempty"`
}

// OrkaToolInventory is shared by CLI discovery and destination preflight.
// Consumers require Complete AND available selected rows, then reobserve for
// execution; this diagnostic inventory grants no receipt/Send authority.
type OrkaToolInventory struct {
	Target         OrkaToolTarget `json:"target"`
	RuntimeVersion string         `json:"runtimeVersion,omitempty"`
	CatalogCommit  string         `json:"catalogCommit,omitempty"`
	Reason         string         `json:"reason"`
	Complete       bool           `json:"complete"`
	Tools          []OrkaToolRow  `json:"tools"`
}

// Exact API catalog, in upstream order: Orka v0.3.0, commit below,
// internal/api/handlers.go:56-64. Names are from each tool's Name() implementation.
// This is pinned release data, not a live API observation or worker call proof.
const orkaToolCatalogCommit = "a533c3491d350263c0fa39bb7887dabd0d055c4e"

func orkaToolCatalog() []string {
	return []string{"web_search", "code_exec", "file_read", "web_fetch", "file_write", "request_approval", "reply_in_conversation"}
}

type toolObserver struct {
	a      *App
	ctx    context.Context
	bytes  int
	reason string
}

func (o *toolObserver) problem(reason string) {
	if o.reason == "" {
		o.reason = reason
	}
}
func (o *toolObserver) get(path string, out any) bool {
	raw, reason := o.a.toolRead(o.ctx, path)
	o.bytes += len(raw)
	if reason != "" {
		o.problem(reason)
		return false
	}
	if o.bytes > 8<<20 {
		o.problem("inventory_limit")
		return false
	}
	shape := ""
	if path == "/apis/core.orka.ai/v1alpha1" {
		shape = "api"
	}
	if !discoveryDecode(raw, out, shape) {
		o.problem("malformed")
		return false
	}
	return true
}
func (o *toolObserver) namespace(name string) string {
	var ns struct {
		APIVersion, Kind string
		Metadata         struct {
			Name, UID         string
			DeletionTimestamp *string
		}
	}
	if !o.get("/api/v1/namespaces/"+name, &ns) {
		return ""
	}
	if ns.APIVersion != "v1" || ns.Kind != "Namespace" || ns.Metadata.Name != name || !statusOpaque(ns.Metadata.UID) || ns.Metadata.DeletionTimestamp != nil {
		o.problem("malformed")
		return ""
	}
	return ns.Metadata.UID
}
func (o *toolObserver) runtime(namespace string) (suiteNativeRuntime, bool) {
	items, ok := o.deployments()
	if !ok {
		return suiteNativeRuntime{}, false
	}
	d, _, err := selectOrkaController(items)
	if err == nil {
		var r suiteNativeRuntime
		r, err = suiteNativeControllerRuntime(d, namespace)
		if err == nil && statusOpaque(r.ControllerUID) {
			return r, true
		}
		if _, pe := suiteNativeControllerProfile(d, namespace); pe == nil {
			o.problem("read_unknown")
			return suiteNativeRuntime{}, false
		}
	}
	o.problem("unsupported_runtime")
	return suiteNativeRuntime{}, false
}
func (o *toolObserver) api() bool {
	var api struct {
		APIVersion, Kind, GroupVersion string
		Resources                      []struct {
			Name, Kind string
			Namespaced bool
			Verbs      []string
		}
	}
	if !o.get("/apis/core.orka.ai/v1alpha1", &api) {
		return false
	}
	if api.APIVersion == "v1" && api.Kind == "APIResourceList" && api.GroupVersion == "core.orka.ai/v1alpha1" {
		count := 0
		for _, r := range api.Resources {
			if r.Name == "tools" && r.Kind == "Tool" && r.Namespaced && slices.Contains(r.Verbs, "get") && slices.Contains(r.Verbs, "list") {
				count++
			}
		}
		if count == 1 {
			return true
		}
	}
	o.problem("unsupported_runtime")
	return false
}
func toolName(kind, name string) bool {
	if kind == "builtin" {
		return localtools.ValidToolName(name)
	}
	return kind == "http" && scaffold.ValidateObjectName(name) == nil
}
func toolRow(raw json.RawMessage, namespace string) (OrkaToolRow, bool) {
	row := OrkaToolRow{Kind: "http", Source: "kubernetes", Availability: "unknown", Reason: "malformed"}
	var obj struct {
		APIVersion, Kind string
		Metadata         struct {
			Name, Namespace, UID, ResourceVersion string
			Generation                            int64
			DeletionTimestamp                     *string
		}
		Spec   map[string]any
		Status struct{ Available bool }
	}
	if !discoveryDecode(raw, &obj, "") || (obj.Kind != "" && obj.Kind != "Tool") || (obj.APIVersion != "" && obj.APIVersion != "core.orka.ai/v1alpha1") || !toolName("http", obj.Metadata.Name) || obj.Metadata.Namespace != namespace || !statusOpaque(obj.Metadata.UID) || !statusOpaque(obj.Metadata.ResourceVersion) || obj.Metadata.Generation < 1 || obj.Spec == nil {
		return row, false
	}
	row.Name, row.Namespace, row.UID, row.ResourceVersion, row.Generation = obj.Metadata.Name, namespace, obj.Metadata.UID, obj.Metadata.ResourceVersion, obj.Metadata.Generation
	b, err := suitecore.Marshal(obj.Spec)
	if err != nil {
		return row, false
	}
	row.SpecDigest = godigest.FromBytes(b).String()
	if obj.Metadata.DeletionTimestamp != nil {
		row.Reason = "resource_terminating"
		return row, true
	}
	http, ok := obj.Spec["http"].(map[string]any)
	if _, mcp := obj.Spec["mcp"]; mcp || !ok {
		row.Availability, row.Reason = "unsupported", "backend_unsupported"
		return row, true
	}
	endpoint, ok := http["url"].(string)
	if !ok || strings.TrimSpace(endpoint) == "" {
		row.Availability, row.Reason = "unsupported", "backend_unsupported"
		return row, true
	}
	method := "POST"
	if value, exists := http["method"]; exists {
		var ok bool
		method, ok = value.(string)
		if !ok {
			method = ""
		}
	}
	if !slices.Contains([]string{"GET", "POST", "PUT", "PATCH", "DELETE"}, method) {
		row.Availability, row.Reason = "unsupported", "backend_unsupported"
		return row, true
	}
	row.Reason = "endpoint_unavailable"
	if obj.Status.Available {
		row.Availability, row.Reason = "available", "observed"
	}
	return row, true
}
func (o *toolObserver) tools(namespace string) []OrkaToolRow {
	rows := []OrkaToolRow{}
	seen, cursors, ids := map[string]bool{}, map[string]bool{}, map[string]bool{}
	cursor, version := "", ""
	for page := 0; page < 32; page++ {
		path := "/apis/core.orka.ai/v1alpha1/namespaces/" + namespace + "/tools?limit=64"
		if cursor != "" {
			path += "&continue=" + url.QueryEscape(cursor)
		}
		var list struct {
			APIVersion, Kind string
			Metadata         struct{ Continue, ResourceVersion string }
			Items            []json.RawMessage
		}
		if !o.get(path, &list) {
			break
		}
		if list.APIVersion != "core.orka.ai/v1alpha1" || list.Kind != "ToolList" || list.Items == nil {
			o.problem("malformed")
			break
		}
		if page > 0 && (version == "" || list.Metadata.ResourceVersion != version) {
			o.problem("read_unknown")
			return rows
		}
		version = list.Metadata.ResourceVersion
		for _, item := range list.Items {
			row, ok := toolRow(item, namespace)
			if !ok || seen[row.Name] || ids[row.UID] {
				o.problem("malformed")
				continue
			}
			seen[row.Name], ids[row.UID] = true, true
			if len(rows) >= 128 {
				o.problem("inventory_limit")
				return rows
			}
			rows = append(rows, row)
		}
		cursor = list.Metadata.Continue
		if cursor == "" {
			return rows
		}
		if len(cursor) > 4096 || cursors[cursor] || version == "" {
			o.problem("inventory_limit")
			return rows
		}
		cursors[cursor] = true
	}
	if cursor != "" {
		o.problem("inventory_limit")
	}
	return rows
}

// ReadOrkaTools performs bounded Kubernetes GETs only; it neither reads Secret
// values nor mints a token. The caller's App/config/Runner are never rebound.
func (a *App) ReadOrkaTools(ctx context.Context, opt OrkaToolOptions) (OrkaToolInventory, error) {
	result := OrkaToolInventory{Target: OrkaToolTarget{Namespace: opt.Namespace}, Tools: []OrkaToolRow{}, Reason: "invalid_arguments"}
	if a == nil || a.Cfg == nil || a.Run == nil || !statusSafeText(a.Cfg.KubeContext) || scaffold.ValidateNamespace(opt.Namespace) != nil || len(opt.Requirements) > 32 {
		return result, toolDiscoveryError(result.Reason)
	}
	seen := map[string]bool{}
	for _, r := range opt.Requirements {
		if !toolName(r.Kind, r.Name) || seen[r.Name] {
			return result, toolDiscoveryError(result.Reason)
		}
		seen[r.Name] = true
	}
	result.Target.Context = a.Cfg.KubeContext
	worker, cfg, runner := *a, *a.Cfg, *a.Run
	worker.Cfg, worker.Run, worker.orkaForwardContext = &cfg, &runner, nil
	runner.Env = append(append([]string(nil), runner.Env...), "KUBERC=off", "KUBECTL_KUBERC=false")
	runner.Unset = append(append([]string(nil), runner.Unset...), "KUBERC", "KUBECTL_KUBERC")
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	o := toolObserver{a: &worker, ctx: ctx}
	result.Target.ClusterUID = o.namespace("kube-system")
	result.Target.NamespaceUID = o.namespace(opt.Namespace)
	runtime, pinned := o.runtime(opt.Namespace)
	if pinned {
		result.RuntimeVersion, result.CatalogCommit = OrkaVersion, orkaToolCatalogCommit
		for _, name := range orkaToolCatalog() {
			result.Tools = append(result.Tools, OrkaToolRow{Kind: "builtin", Name: name, Availability: "available", Reason: "pinned-catalog", Source: "pinned-catalog"})
		}
	}
	api := false
	if result.Target.NamespaceUID != "" {
		api = o.api()
	}
	http := []OrkaToolRow{}
	if api && len(opt.Requirements) == 0 {
		http = o.tools(opt.Namespace)
		sort.Slice(http, func(i, j int) bool { return http[i].Name < http[j].Name })
	}
	result.Tools = append(result.Tools, http...)
	if len(opt.Requirements) > 0 {
		all := result.Tools
		result.Tools = []OrkaToolRow{}
		for _, r := range opt.Requirements {
			row := OrkaToolRow{Kind: r.Kind, Name: r.Name, Availability: "unknown", Reason: "read_unknown", Source: "kubernetes"}
			if !r.Enabled {
				row.Availability, row.Reason, row.Source = "disabled", "disabled", "declaration"
				result.Tools = append(result.Tools, row)
				continue
			}
			i := slices.IndexFunc(all, func(row OrkaToolRow) bool { return row.Kind == r.Kind && row.Name == r.Name })
			if i >= 0 {
				row = all[i]
			} else if r.Kind == "builtin" {
				row.Source = "pinned-catalog"
				if pinned {
					row.Availability, row.Reason = "unsupported", "name_unsupported"
				}
			} else if api && o.reason == "" {
				raw, reason := worker.toolRead(ctx, "/apis/core.orka.ai/v1alpha1/namespaces/"+opt.Namespace+"/tools/"+r.Name)
				o.bytes += len(raw)
				if o.bytes > 8<<20 {
					reason = "inventory_limit"
				}
				if reason == "resource_missing" {
					row.Availability, row.Reason = "missing", "resource_missing"
				} else if reason != "" {
					row.Reason = reason
					o.problem(reason)
				} else {
					var identity struct{ APIVersion, Kind string }
					identityOK := discoveryDecode(raw, &identity, "") && identity.APIVersion == "core.orka.ai/v1alpha1" && identity.Kind == "Tool"
					var valid bool
					row, valid = toolRow(raw, opt.Namespace)
					if !identityOK || !valid || row.Name != r.Name {
						row = OrkaToolRow{Kind: r.Kind, Name: r.Name, Availability: "unknown", Reason: "malformed", Source: "kubernetes"}
						o.problem("malformed")
					}
				}
			}
			result.Tools = append(result.Tools, row)
		}
	}
	// Availability is an observation within this bracket, not permanent authority.
	stable := result.Target.ClusterUID != "" && result.Target.NamespaceUID != ""
	if uid := o.namespace("kube-system"); uid != result.Target.ClusterUID || uid == "" {
		stable = false
	}
	if uid := o.namespace(opt.Namespace); uid != result.Target.NamespaceUID || uid == "" {
		stable = false
	}
	if pinned {
		again, ok := o.runtime(opt.Namespace)
		if !ok || again != runtime {
			stable = false
		}
	}
	if !stable || !pinned {
		why := "unsupported_runtime"
		if !stable || (o.reason != "" && o.reason != "unsupported_runtime") {
			why = o.reason
			if why == "" || why == "unsupported_runtime" {
				why = "stale_input"
			}
			o.reason = why
		}
		for i := range result.Tools {
			if result.Tools[i].Availability != "disabled" {
				result.Tools[i].Availability, result.Tools[i].Reason = "unknown", why
			}
		}
	}
	result.Complete = o.reason == ""
	result.Reason = "observed"
	if !result.Complete {
		result.Reason = o.reason
	}
	for {
		b, _ := json.Marshal(result)
		if len(b) <= 60<<10 {
			break
		}
		result.Complete, result.Reason = false, "inventory_limit"
		result.Tools = result.Tools[:len(result.Tools)-1]
	}
	if !result.Complete {
		return result, toolDiscoveryError(result.Reason)
	}
	return result, nil
}

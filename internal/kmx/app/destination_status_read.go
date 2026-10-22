package app

import (
	"encoding/json"
	"net/url"
	"reflect"
	"strings"
	"unicode"
	"unicode/utf8"

	suitecore "github.com/agent-primitives/suite/core"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/scaffold"
)

func statusSafeText(s string) bool {
	return s != "" && len(s) <= 1024 && utf8.ValidString(s) && strings.TrimSpace(s) == s && strings.IndexFunc(s, func(r rune) bool { return unicode.IsControl(r) || unicode.In(r, unicode.Cf) }) < 0 && scaffold.RefuseKeyShapes(s) == nil
}
func statusOpaque(s string) bool { return statusSafeText(s) && !strings.ContainsAny(s, "/\\ \t") }
func statusDecode(raw []byte, out any) bool {
	// Validate ambiguity, Unicode, numbers and nesting before projecting exact
	// Kubernetes map keys. Unused payload fields never enter a report DTO.
	var object map[string]any
	return suitecore.Unmarshal(raw, &object) == nil && object != nil && json.Unmarshal(raw, out) == nil
}
func (o *destinationStatusObserver) get(path string, out any) bool {
	if o.ctx.Err() != nil || o.calls >= 2048 || o.bytes >= 64<<20 {
		return false
	}
	o.calls++
	raw, err := o.a.orkaCapture(o.ctx, nil, "get", "--raw", path)
	o.bytes += len(raw)
	return err == nil && o.bytes <= 64<<20 && statusDecode(raw, out)
}
func (o *destinationStatusObserver) namespaceUID(name string) string {
	var ns map[string]any
	if !o.get("/api/v1/namespaces/"+name, &ns) || ns["apiVersion"] != "v1" || ns["kind"] != "Namespace" {
		o.problem("cluster or namespace identity is unknown")
		return ""
	}
	meta := statusMap(ns, "metadata")
	uid := statusString(meta, "uid")
	if meta["name"] != name || !statusOpaque(uid) || meta["deletionTimestamp"] != nil {
		o.problem("cluster or namespace identity is unknown")
		return ""
	}
	return uid
}
func (o *destinationStatusObserver) discover() map[string]string {
	states := map[string]string{"orka": "unknown", "kagent": "unknown"}
	o.collections = map[string]bool{}
	var discovery map[string]any
	if !o.get("/apis", &discovery) || discovery["apiVersion"] != "v1" || discovery["kind"] != "APIGroupList" {
		o.problem("Agent API discovery is unknown")
		return states
	}
	groups, ok := discovery["groups"].([]any)
	if !ok {
		o.problem("Agent API discovery is unknown")
		return states
	}
	versions := map[string]map[string]bool{}
	for _, value := range groups {
		g, ok := value.(map[string]any)
		if !ok {
			o.problem("Agent API discovery is malformed")
			return states
		}
		name := statusString(g, "name")
		v, ok := g["versions"].([]any)
		if !statusSafeText(name) || versions[name] != nil || !ok || len(v) == 0 {
			o.problem("Agent API discovery is malformed")
			return states
		}
		versions[name] = map[string]bool{}
		for _, value := range v {
			entry, ok := value.(map[string]any)
			if !ok {
				o.problem("Agent API discovery is malformed")
				return states
			}
			gv, version := statusString(entry, "groupVersion"), statusString(entry, "version")
			if gv != name+"/"+version || version == "" || versions[name][gv] {
				o.problem("Agent API discovery is malformed")
				return states
			}
			versions[name][gv] = true
		}
	}
	for _, api := range destinationStatusAPIs {
		group := strings.Split(api.version, "/")[0]
		if versions[group] == nil {
			states[api.runtime] = "absent"
			continue
		}
		if !versions[group][api.version] {
			o.problem(api.runtime + " Agent API version is unsupported")
			continue
		}
		var discovery map[string]any
		if !o.get(api.prefix, &discovery) || discovery["apiVersion"] != "v1" || discovery["kind"] != "APIResourceList" || discovery["groupVersion"] != api.version {
			o.problem(api.runtime + " Agent API discovery is unknown")
			continue
		}
		resources, ok := discovery["resources"].([]any)
		if !ok {
			o.problem(api.runtime + " Agent API discovery is unknown")
			continue
		}
		states[api.runtime] = "absent"
		found := false
		aux := map[string]string{}
		for _, kind := range statusCandidateKinds(api.runtime) {
			aux[kind.plural] = kind.kind
		}
		seenAux := map[string]bool{}
		for _, value := range resources {
			r, ok := value.(map[string]any)
			if !ok || !statusDiscoveryResource(r) {
				states[api.runtime] = "unknown"
				o.problem(api.runtime + " Agent API discovery is malformed")
				break
			}
			verbs, _ := r["verbs"].([]any)
			get, list := false, false
			for _, verb := range verbs {
				get = get || verb == "get"
				list = list || verb == "list"
			}
			if r["name"] != "agents" {
				name := statusString(r, "name")
				if kind, known := aux[name]; known {
					key := api.runtime + "/" + name
					if seenAux[name] || r["kind"] != kind || r["namespaced"] != true || !get || !list {
						delete(o.collections, key)
						o.problem(api.runtime + " candidate API discovery is malformed")
					} else {
						o.collections[key] = true
					}
					seenAux[name] = true
				}
				continue
			}
			if found || r["kind"] != "Agent" || r["namespaced"] != true || !get || !list {
				states[api.runtime] = "unknown"
				o.problem(api.runtime + " Agent API discovery is malformed")
				break
			}
			found = true
			states[api.runtime] = "served"
		}
	}
	return states
}

// Each collection has at most four128-item/4MiB pages. A later failure never
// discards readable earlier rows or silently turns incomplete inventory empty.
func (o *destinationStatusObserver) list(prefix, plural, kind, version, selector string) ([]map[string]any, bool) {
	rows := []map[string]any{}
	complete := true
	token, revision := "", ""
	seenTokens, seenNames, seenUIDs := map[string]bool{}, map[string]bool{}, map[string]bool{}
	for page := 0; page < 4; page++ {
		path := prefix + "/namespaces/" + o.report.Target.Namespace + "/" + plural + "?limit=128"
		if selector != "" {
			path += "&labelSelector=" + url.QueryEscape(selector)
		}
		if token != "" {
			path += "&continue=" + url.QueryEscape(token)
		}
		var list map[string]any
		if !o.get(path, &list) || list["apiVersion"] != version || list["kind"] != kind+"List" {
			o.problem(kind + " inventory is unknown or incomplete")
			return rows, false
		}
		meta := statusMap(list, "metadata")
		rv := statusString(meta, "resourceVersion")
		items, ok := list["items"].([]any)
		if !ok || len(items) > 128 || !statusOpaque(rv) || page > 0 && rv != revision {
			o.problem(kind + " inventory is unknown or incomplete")
			return rows, false
		}
		revision = rv
		for _, value := range items {
			doc, ok := value.(map[string]any)
			if ok {
				// Typed Kubernetes lists may omit item GVK. Only the validated
				// collection supplies protocol type; never provenance or identity.
				if _, present := doc["kind"]; !present {
					doc["kind"] = kind
				}
				if _, present := doc["apiVersion"]; !present {
					doc["apiVersion"] = version
				}
			}
			if !ok || !o.validObjectName(doc, kind, version) {
				o.problem(kind + " inventory contains an unknown identity")
				complete = false
				continue
			}
			meta := statusMap(doc, "metadata")
			name, uid := statusString(meta, "name"), statusString(meta, "uid")
			if !o.validObject(doc, kind, version) {
				o.problem(kind + " inventory contains a partial identity")
				complete = false
			}
			if seenNames[name] || statusOpaque(uid) && seenUIDs[uid] {
				o.problem(kind + " inventory contains duplicate identities")
				complete = false
				continue
			}
			seenNames[name] = true
			if statusOpaque(uid) {
				seenUIDs[uid] = true
			}
			rows = append(rows, doc)
		}
		token = ""
		if value, present := meta["continue"]; present {
			var ok bool
			token, ok = value.(string)
			if !ok {
				o.problem(kind + " inventory continuation is unknown")
				return rows, false
			}
		}
		if token == "" {
			return rows, complete
		}
		if len(token) > 4096 || !utf8.ValidString(token) || strings.IndexFunc(token, unicode.IsControl) >= 0 || seenTokens[token] {
			o.problem(kind + " inventory continuation is unknown")
			return rows, false
		}
		seenTokens[token] = true
	}
	o.problem(kind + " inventory exceeded its page budget")
	return rows, false
}
func (o *destinationStatusObserver) validObject(doc map[string]any, kind, version string) bool {
	meta := statusMap(doc, "metadata")
	uid, rv := statusString(meta, "uid"), statusString(meta, "resourceVersion")
	return o.validObjectName(doc, kind, version) && statusOpaque(uid) && statusOpaque(rv)
}
func (o *destinationStatusObserver) validObjectName(doc map[string]any, kind, version string) bool {
	meta := statusMap(doc, "metadata")
	name := statusString(meta, "name")
	return doc["kind"] == kind && doc["apiVersion"] == version && scaffold.ValidateObjectName(name) == nil && statusSafeText(name) && meta["namespace"] == o.report.Target.Namespace
}

// A missing supported resource proves absence only inside well-formed discovery.
// Validate even entries we do not consume, rather than silently skipping damage.
func statusDiscoveryResource(resource map[string]any) bool {
	name, kind := statusString(resource, "name"), statusString(resource, "kind")
	if !statusOpaque(kind) {
		return false
	}
	parts := strings.Split(name, "/")
	if len(parts) > 2 {
		return false
	}
	for _, part := range parts {
		if scaffold.ValidateObjectName(part) != nil {
			return false
		}
	}
	if _, ok := resource["namespaced"].(bool); !ok {
		return false
	}
	verbs, ok := resource["verbs"].([]any)
	if !ok {
		return false
	}
	for _, value := range verbs {
		verb, ok := value.(string)
		if !ok || !statusOpaque(verb) {
			return false
		}
	}
	return true
}

func (o *destinationStatusObserver) recheck(api destinationStatusAPI, plural string, before map[string]any) (destinationStatusReadiness, bool) {
	meta := statusMap(before, "metadata")
	name := statusString(meta, "name")
	after, ok := o.workloadObject(api.prefix, plural, statusString(before, "kind"), api.version, name)
	if !ok {
		return destinationStatusReadiness{}, false
	}
	observed := statusMap(after, "metadata")
	owner, state := statusOwnership(api.runtime, meta)
	other, otherState := statusOwnership(api.runtime, observed)
	ann, latest := statusMap(meta, "annotations"), statusMap(observed, "annotations")
	same := observed["uid"] == meta["uid"] && reflect.DeepEqual(observed["generation"], meta["generation"]) && reflect.DeepEqual(before["spec"], after["spec"]) && reflect.DeepEqual(owner, other) && state == otherState && reflect.DeepEqual(ann[releaseLiftRecordAnnotation], latest[releaseLiftRecordAnnotation])
	return statusPrimaryReadiness(api.runtime, after), same
}

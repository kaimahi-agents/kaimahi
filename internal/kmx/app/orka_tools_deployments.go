package app

import "net/url"

// Both target readiness and Tool pinning consume the same literal projection.
func (o *toolObserver) deployments() ([]orkaDeployment, bool) {
	all := []orkaDeployment{}
	cursor, version := "", ""
	seen := map[string]bool{}
	for page := 0; page < 32; page++ {
		path := "/apis/apps/v1/namespaces/orka-system/deployments?limit=64"
		if cursor != "" {
			path += "&continue=" + url.QueryEscape(cursor)
		}
		raw, reason := o.a.toolRead(o.ctx, path)
		o.bytes += len(raw)
		if reason != "" {
			o.problem(reason)
			return all, false
		}
		if o.bytes > 8<<20 {
			o.problem("inventory_limit")
			return all, false
		}
		items, next, rv, ok := discoveryDeployments(raw)
		if !ok {
			o.problem("malformed")
			return all, false
		}
		if page > 0 && (version == "" || rv != version) {
			o.problem("read_unknown")
			return all, false
		}
		version = rv
		all = append(all, items...)
		if len(all) > 128 {
			o.problem("inventory_limit")
			return nil, false
		}
		if next == "" {
			return all, true
		}
		if len(next) > 4096 || seen[next] || rv == "" {
			o.problem("read_unknown")
			return all, false
		}
		seen[next] = true
		cursor = next
	}
	o.problem("inventory_limit")
	return all, false
}

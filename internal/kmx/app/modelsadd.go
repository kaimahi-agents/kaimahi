package app

// `kmx models add` — onboarding a model endpoint this repo did not deploy.
//
// The gap it closes, measured rather than imagined: an adopter ran a
// framework that posts to `/v1/responses`, the committed table had no
// entry for it, and the ONLY route was to edit
// `k8s/plane/upstreams.yaml` — a file the next `kmx plane` re-applies,
// discarding the edit. The operator overlay instead preserves existing
// fragments, pins its apply to their resourceVersion, and validates against
// the running plane before anything is written.

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/admin"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/scaffold"
)

// AddModelOptions cannot carry credential material: model overlay entries
// are keyless by the plane's own rule.
type AddModelOptions struct {
	Name string
	// URL is the endpoint's own in-cluster URL, INCLUDING the path its
	// clients post to — the table stores the two halves separately and
	// cannot infer the second.
	URL string
	// Protocol is the wire shape. Optional when URL's path names one.
	Protocol string
	// Classification is "free" or "metered", and is required: this
	// project never classifies an upstream $0 by inference.
	Classification string
	// PodPort overrides the Service's resolved targetPort, for the one
	// case kmx cannot resolve on its own (a NAMED target port).
	PodPort int
	// ServerEgress is one of scaffold.ServerEgressModes.
	ServerEgress string
	Out          string
	NoApply      bool
	DryRun       bool
}

// AddModel scaffolds, validates and applies one model upstream.
func (a *App) AddModel(opt AddModelOptions) error {
	if err := scaffold.ValidateModelName(opt.Name); err != nil {
		return err
	}
	switch opt.Classification {
	case "free", "metered":
	case "":
		return fmt.Errorf("--classification is required (%s).\n"+
			"  free:    this endpoint costs nothing. Only a TOKEN budget can ever exhaust it.\n"+
			"  metered: tokens are always counted; a cost applies only where a price is configured,\n"+
			"           and under a CENTS budget an unpriced model is refused. Prices are a reviewed\n"+
			"           entry in k8s/plane/upstreams.yaml — an overlay may not set one, and this\n"+
			"           project never invents one.\n"+
			"  There is no default: a $0 by inference is a budget nothing can exhaust.",
			strings.Join(scaffold.Classifications, " | "))
	default:
		return fmt.Errorf("--classification %q: want one of %s",
			opt.Classification, strings.Join(scaffold.Classifications, ", "))
	}
	// `free` is a claim about somebody else's endpoint, and the plane
	// cannot check it. The in-cluster shape for a paid model is a router
	// that holds the key itself, so this is not a hypothetical: it is the
	// one setting here that can make real spend invisible. Named at the
	// point of choosing.
	if opt.Classification == "free" {
		a.notef("WARNING: %q is classified free — an EXPLICIT $0, not an observation.", opt.Name)
		a.notef("  No cents budget can ever bind it, and every call through it is ledgered as costing")
		a.notef("  nothing. If the endpoint behind this URL holds a paid key — a router or gateway in")
		a.notef("  front of a hosted model is the usual shape — that spend is real and this ledger will")
		a.notef("  not show it. Use --classification metered and a token budget if you are not certain.")
	}
	if opt.ServerEgress == "" {
		opt.ServerEgress = scaffold.EgressNone
	}
	switch opt.ServerEgress {
	case scaffold.EgressNone, scaffold.EgressDNS, scaffold.EgressKeep:
	default:
		return fmt.Errorf("--server-egress %q: want one of %s",
			opt.ServerEgress, strings.Join(scaffold.ServerEgressModes, ", "))
	}
	// A pure generate must still be a generate, exactly as elsewhere.
	if opt.Out == "-" {
		opt.NoApply = true
	}

	base, path, svc, ns, port, err := scaffold.ParseModelURL(opt.URL)
	if err != nil {
		return err
	}
	protocol, err := resolveProtocol(path, opt.Protocol)
	if err != nil {
		return err
	}
	spec := scaffold.ModelSpec{
		Name:             opt.Name,
		BaseURL:          base,
		Path:             path,
		Protocol:         protocol,
		Classification:   opt.Classification,
		Service:          svc,
		ServiceNamespace: ns,
		ServerDNS:        opt.ServerEgress == scaffold.EgressDNS,
		ServerEgressKeep: opt.ServerEgress == scaffold.EgressKeep,
	}
	// The pod selector and the container port come from the LIVE
	// Service, never from its name: a wrong guess silently blocks traffic.
	if err := a.resolveService(&spec, opt.PodPort, port); err != nil {
		return err
	}
	if err := a.showModelPolicyBlastRadius(spec); err != nil {
		return err
	}

	frag, err := spec.Fragment()
	if err != nil {
		return err
	}
	// The overlay ConfigMap is emitted WHOLE — every fragment already on
	// the cluster plus this one — because `kubectl apply` prunes a key
	// that was in the last applied configuration and is absent from the
	// new one. An emitted map missing an existing key would silently
	// un-onboard somebody else's model endpoint.
	spec.Fragments, spec.OverlayVersion, err = a.readOverlay()
	if err != nil {
		return err
	}
	if _, exists := spec.Fragments[spec.FragmentKey()]; exists {
		return fmt.Errorf("model upstream %q is already in the overlay (%s key %q).\n"+
			"  Read it back with:\n"+
			"    kubectl --context %s -n %s get configmap %s -o jsonpath='{.data.%s}'\n"+
			"  and remove it deliberately if you mean to replace it.",
			opt.Name, scaffold.OverlayConfigMap, spec.FragmentKey(),
			a.Cfg.KubeContext, scaffold.PlaneNamespace, scaffold.OverlayConfigMap, spec.FragmentKey())
	}
	spec.Fragments[spec.FragmentKey()] = frag

	document, err := scaffold.GenerateModel(spec)
	if err != nil {
		return err
	}

	// Validate BEFORE anything is written or applied, and with the
	// plane's own parser rather than a second copy of it. The candidate
	// overlay goes to the running proxy, which merges it over the
	// committed table and calls the same config.Parse it booted with.
	if err := a.validateModelOverlay(spec); err != nil {
		return err
	}

	// Onboarding widens every existing credential's reach, so say so.
	a.notef("")
	a.notef("NOTE: the model seam has no allowlist. %q is reachable by EVERY", opt.Name)
	a.notef("  credential the plane has issued, the moment it is in the table — there is no per-credential")
	a.notef("  scope on this seam at all. What still bounds them is the budget each credential carries,")
	a.notef("  and the fact that the ENTRY is keyless — the plane holds no credential for it. Whether the")
	a.notef("  endpoint itself holds one is yours to know; the plane cannot see behind the URL.")

	if opt.Out == "-" {
		_, err := a.Out.Write([]byte(document))
		return err
	}
	path = opt.Out
	if path == "" {
		path = filepath.Join("upstreams", "model-"+opt.Name+".yaml")
	}
	if err := scaffold.WriteNew(path, document); err != nil {
		return err
	}
	a.notef("Wrote %s — three documents: the overlay fragment, the proxy's egress to this endpoint,", path)
	a.notef("and this endpoint's ingress from the proxy alone.")
	if opt.NoApply {
		a.notef("Not applied (--no-apply). Review it, then:")
		a.notef("  kubectl --context %s apply -f %s", a.Cfg.KubeContext, path)
		a.notef("  kubectl --context %s -n %s rollout restart deploy/kaimahi-proxy", a.Cfg.KubeContext, scaffold.PlaneNamespace)
		a.notef("  (the proxy reads its table at boot; the entry is not live until it restarts)")
		a.seamAddress(spec)
		return nil
	}

	if err := a.Guard(fmt.Sprintf("onboard model upstream %q from %s", opt.Name, path),
		"kmx models add "+opt.Name); err != nil {
		return err
	}
	if opt.DryRun {
		return a.kubectlRun("apply", "--dry-run=server", "-f", path)
	}
	// `kubectl apply -f` applies each document independently and does not
	// roll back, so a ConfigMap refused on a stale resourceVersion would
	// still leave the two NetworkPolicies behind. Re-read the version
	// here, where nothing has happened yet.
	if err := a.refuseOnOverlayDrift(spec.OverlayVersion, path, "kmx models add", opt.Name); err != nil {
		return err
	}
	if err := a.kubectlRun("apply", "-f", path); err != nil {
		return err
	}
	// The proxy reads its table at boot and the overlay mounts by
	// directory, so the entry is not live until the proxy restarts.
	if err := a.rollProxy(); err != nil {
		return err
	}
	a.notef("")
	a.notef("Model upstream %q is in the table and metered.", opt.Name)
	a.seamAddress(spec)
	return nil
}

// seamAddress prints client wiring rather than generating a kagent resource:
// the owner's runtime may not use kagent.
func (a *App) seamAddress(spec scaffold.ModelSpec) {
	a.notef("Point a client at it:")
	a.notef("  base URL:   %s", scaffold.SeamBaseURL(spec.Name))
	a.notef("  the client appends %q — the one path this upstream accepts, and the only one.", spec.Path)
	a.notef("  credential: a kmh_ token in the Authorization header, where the model API key would go.")
	a.notef("              `kmx govern` issues one; keys never reach the client.")
	a.notef("  TLS:        the proxy serves under the plane's own authority. Its certificate is published")
	a.notef("              as Secret %s (key %s) — a client that does not trust it will not connect.",
		scaffold.PlaneCASecret, scaffold.PlaneCAKey)
}

// resolveProtocol decides which wire shape this upstream speaks, and
// refuses rather than guessing.
//
// The path is allowed to answer, because a path ending `/v1/responses`
// IS the Responses API and there is nothing to infer — but a --protocol
// that disagrees with its own path is refused rather than resolved,
// since whichever of the two is wrong, the meter would read the wrong
// field. The plane applies the identical rule at load; this one exists
// so the message names the URL the operator typed.
func resolveProtocol(path, declared string) (string, error) {
	fromPath := scaffold.PathProtocol(path)
	switch {
	case declared == "" && fromPath == "":
		return "", fmt.Errorf("the path %q names no protocol kmx knows, so declare one: --protocol %s.\n"+
			"  The protocol says where the plane reads token counts out of a response:\n"+
			"    chat_completions  {\"usage\": {\"prompt_tokens\", \"completion_tokens\"}}\n"+
			"    responses         {\"usage\": {\"input_tokens\", \"output_tokens\"}}\n"+
			"  There is no default, because a wrong guess reads zero tokens and a budget over this\n"+
			"  upstream could then never be exhausted.",
			path, strings.Join(scaffold.Protocols, " | "))
	case declared == "":
		return fromPath, nil
	case fromPath != "" && fromPath != declared:
		return "", fmt.Errorf("--protocol %s, but the path %q is %s — refused rather than resolved, "+
			"because whichever is wrong the meter reads the wrong field", declared, path, fromPath)
	}
	for _, p := range scaffold.Protocols {
		if declared == p {
			return declared, nil
		}
	}
	return "", fmt.Errorf("--protocol %q: want one of %s", declared, strings.Join(scaffold.Protocols, ", "))
}

// showModelPolicyBlastRadius names the pods the scaffolded ingress
// policy will govern. A read; it never blocks.
func (a *App) showModelPolicyBlastRadius(spec scaffold.ModelSpec) error {
	sel := make([]string, 0, len(spec.PodLabels))
	for k, v := range spec.PodLabels {
		sel = append(sel, k+"="+v)
	}
	sort.Strings(sel)
	out, err := a.kubectlCapture("-n", spec.ServiceNamespace, "get", "pods",
		"-l", strings.Join(sel, ","), "-o", "jsonpath={.items[*].metadata.name}")
	if err != nil {
		a.notef("NOTE: could not list the pods %s will govern (%v). Read the selector yourself.",
			spec.IngressPolicyName(), err)
		return nil
	}
	pods := strings.Fields(strings.TrimSpace(out))
	switch len(pods) {
	case 0:
		a.notef("The selector %s matches no pods right now — the policy is still correct; it opens nothing.",
			strings.Join(sel, ","))
	case 1:
		a.notef("The policy pair governs pod %s (selector %s).", pods[0], strings.Join(sel, ","))
	default:
		a.notef("NOTE: the selector %s matches %d pods, not one: %s.",
			strings.Join(sel, ","), len(pods), strings.Join(pods, ", "))
		a.notef("  ALL of them will be reachable only from the proxy. If they are not all this endpoint,")
		a.notef("  give it a Service whose selector picks only its own pods.")
	}
	return nil
}

// validateModelOverlay asks the RUNNING plane whether this overlay would
// load, and echoes back the protocol the plane resolved for every model
// upstream. Nothing is stored and nothing changes; this is a read.
func (a *App) validateModelOverlay(spec scaffold.ModelSpec) error {
	return a.session(func(c *admin.Client) error {
		body := map[string]any{"fragments": map[string]json.RawMessage{}}
		frags := body["fragments"].(map[string]json.RawMessage)
		for name, raw := range spec.Fragments {
			frags[name] = json.RawMessage(raw)
		}
		// A plane that predates the model overlay refuses the fragment
		// with its own flat "carries \"upstreams\", which an overlay may
		// not set" — true of that plane, and reading like a mistake by
		// the operator. Ask what the plane is first, and say that instead.
		if err := c.Require(admin.ContractModelOverlay,
			"add a model upstream through the operator overlay"); err != nil {
			return err
		}
		status, out, err := c.Do("POST", "/admin/config/validate", body)
		if err != nil {
			return err
		}
		var resp struct {
			OK        bool     `json:"ok"`
			Error     string   `json:"error"`
			Upstreams []string `json:"upstreams"`
		}
		_ = json.Unmarshal(out, &resp)
		if status != 200 || !resp.OK {
			msg := resp.Error
			if msg == "" {
				msg = strings.TrimSpace(string(out))
			}
			return fmt.Errorf("the plane refused this upstream table — nothing has been applied:\n  %s", msg)
		}
		a.notef("The plane validated the table. Model upstreams: %s.", strings.Join(resp.Upstreams, ", "))
		a.notef("  %s speaks %s: the meter reads %s.", spec.Name, spec.Protocol, tokenFieldsOf(spec.Protocol))
		if spec.Classification == "metered" {
			a.notef("  Classified metered with no price, which an overlay may not carry: token budgets")
			a.notef("  govern it, and a CENTS budget refuses it until a price is added to the committed table.")
		}
		return nil
	})
}

// tokenFieldsOf names the fields the meter will read, so the line above
// says what was actually decided rather than repeating the flag back.
// resolveService reads the actual selector and post-NAT container port.
// Neither can safely be inferred from a Service's name.
func (a *App) resolveService(spec *scaffold.ModelSpec, override, urlPort int) error {
	out, err := a.kubectlCapture("-n", spec.ServiceNamespace, "get", "service", spec.Service, "-o", "json")
	if err != nil {
		if isNotFound(err) {
			return fmt.Errorf("no Service %q in namespace %q.\n"+
				"  kmx reads the Service to learn which pods the NetworkPolicy must name and which port they listen on;\n"+
				"  neither can be guessed from a URL. Deploy the server first, then onboard it.", spec.Service, spec.ServiceNamespace)
		}
		return err
	}
	var svc struct {
		Spec struct {
			Selector map[string]string `json:"selector"`
			Ports    []struct {
				Port       int             `json:"port"`
				TargetPort json.RawMessage `json:"targetPort"`
				Protocol   string          `json:"protocol"`
			} `json:"ports"`
		} `json:"spec"`
	}
	if err := json.Unmarshal([]byte(out), &svc); err != nil {
		return fmt.Errorf("reading Service %s/%s: %w", spec.ServiceNamespace, spec.Service, err)
	}
	if len(svc.Spec.Selector) == 0 {
		return fmt.Errorf("Service %s/%s has no selector.\n"+
			"  A NetworkPolicy pinned to no labels selects every pod in the namespace, which is not a boundary.\n"+
			"  Onboard a Service that selects its own pods, or write the pair by hand.", spec.ServiceNamespace, spec.Service)
	}
	spec.PodLabels = svc.Spec.Selector
	for _, p := range svc.Spec.Ports {
		if p.Port != urlPort {
			continue
		}
		if p.Protocol != "" && p.Protocol != "TCP" {
			return fmt.Errorf("Service %s/%s port %d is %s; the model seam is TCP", spec.ServiceNamespace, spec.Service, urlPort, p.Protocol)
		}
		if override > 0 {
			spec.PodPort = override
			return nil
		}
		var num int
		if len(p.TargetPort) > 0 && string(p.TargetPort) != "null" {
			if err := json.Unmarshal(p.TargetPort, &num); err != nil {
				var name string
				_ = json.Unmarshal(p.TargetPort, &name)
				return fmt.Errorf("Service %s/%s port %d targets the NAMED port %q.\n"+
					"  A NetworkPolicy needs the number the container listens on; kmx will not guess it.\n"+
					"  Name it: kmx models add … --pod-port <number>", spec.ServiceNamespace, spec.Service, urlPort, name)
			}
		}
		if num == 0 {
			num = p.Port
		}
		spec.PodPort = num
		return nil
	}
	return fmt.Errorf("Service %s/%s publishes no port %d (the port in --url)", spec.ServiceNamespace, spec.Service, urlPort)
}

// readOverlay preserves every existing fragment and its apply precondition.
// Only genuine NotFound means an empty overlay; ambiguous reads fail closed.
func (a *App) readOverlay() (map[string]string, string, error) {
	out, err := a.kubectlCapture("-n", scaffold.PlaneNamespace, "get", "configmap", scaffold.OverlayConfigMap, "-o", "json")
	if err != nil {
		if isNotFound(err) {
			return map[string]string{}, "", nil
		}
		return nil, "", fmt.Errorf("reading the overlay ConfigMap %s: %w", scaffold.OverlayConfigMap, err)
	}
	var cm struct {
		Metadata struct {
			ResourceVersion string `json:"resourceVersion"`
		} `json:"metadata"`
		Data map[string]string `json:"data"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &cm); err != nil {
		return nil, "", fmt.Errorf("reading the overlay ConfigMap %s: %w", scaffold.OverlayConfigMap, err)
	}
	if cm.Data == nil {
		cm.Data = map[string]string{}
	}
	if cm.Metadata.ResourceVersion == "" {
		return nil, "", fmt.Errorf("the overlay ConfigMap %s has no resourceVersion; refusing to emit an apply that could silently replace another operator's fragments", scaffold.OverlayConfigMap)
	}
	return cm.Data, cm.Metadata.ResourceVersion, nil
}

// refuseOnOverlayDrift checks before the multi-document apply, which cannot
// roll back policies if its ConfigMap conflicts.
func (a *App) refuseOnOverlayDrift(readVersion, path, command, name string) error {
	_, version, err := a.readOverlay()
	if err != nil {
		return err
	}
	if version != readVersion {
		return fmt.Errorf("the overlay changed while this was being scaffolded "+
			"(read at version %s, now %s) — nothing has been applied.\n"+
			"  Somebody else onboarded an upstream or edited a fragment. Run the same command again "+
			"to build on their change:\n    rm %s && %s %s …", quoteVersion(readVersion), quoteVersion(version), path, command, name)
	}
	return nil
}

func quoteVersion(v string) string {
	if v == "" {
		return "(absent)"
	}
	return v
}

// rollProxy loads the validated model table into the serving replicas.
func (a *App) rollProxy() error {
	if err := a.kubectlRun("-n", scaffold.PlaneNamespace, "rollout", "restart", "deploy/kaimahi-proxy"); err != nil {
		return err
	}
	return a.kubectlRun("-n", scaffold.PlaneNamespace, "rollout", "status", "deploy/kaimahi-proxy", "--timeout=300s")
}

func tokenFieldsOf(protocol string) string {
	if protocol == "responses" {
		return "input_tokens / output_tokens"
	}
	return "prompt_tokens / completion_tokens"
}

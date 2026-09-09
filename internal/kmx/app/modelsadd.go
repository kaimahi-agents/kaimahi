package app

// `kmx models add` — onboarding a model endpoint this repo did not deploy.
//
// The gap it closes, measured rather than imagined: an adopter ran a
// framework that posts to `/v1/responses`, the committed table had no
// entry for it, and the ONLY route was to edit
// `k8s/plane/upstreams.yaml` — a file the next `kmx plane` re-applies,
// discarding the edit. The tool seam had solved this a week earlier with
// an overlay ConfigMap and `kmx tools add`. This is that same solution
// on the other seam, and it reuses the machinery rather than inventing a
// second shape: the same overlay, the same resourceVersion precondition,
// the same live-Service read for the NetworkPolicy pair, the same
// validate-against-the-running-plane before anything is written.
//
// Where it differs from `kmx tools add`, and why, is in the header of
// internal/kmx/scaffold/model.go.

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/admin"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/scaffold"
)

// AddModelOptions is `kmx models add`'s surface. As with `kmx tools
// add`, there is deliberately no flag, environment variable or file here
// that can carry a credential: an overlay upstream is keyless by the
// plane's own rule, and the one path in kmx that accepts credential
// material is the ruled terminal prompt (`kmx credential capture`).
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
	// point of choosing, the way a verb-level tool binding is.
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
	// Service, never from its name — the same read `kmx tools add` does,
	// and for the same reason: a guess that is wrong fails silently in
	// the direction of blocking everything.
	upstreamSpec := scaffold.UpstreamSpec{
		Name: spec.Name, Service: svc, ServiceNamespace: ns,
	}
	if err := a.resolveService(&upstreamSpec, opt.PodPort, port); err != nil {
		return err
	}
	spec.PodLabels, spec.PodPort = upstreamSpec.PodLabels, upstreamSpec.PodPort
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
	// un-onboard somebody else's upstream, model or tool.
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

	// The model seam has no allowlist. Saying so here is not a
	// formality: on the tool seam an onboarded server is unreachable
	// until a credential allowlists a tool on it, and an operator who
	// has used `kmx tools add` will carry that expectation across.
	a.notef("")
	a.notef("NOTE: the model seam has no allowlist. Unlike a tool upstream, %q is reachable by EVERY", opt.Name)
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
	// here, where nothing has happened yet. Shared with `kmx tools add`,
	// which writes the same overlay under the same precondition.
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

// seamAddress is the model seam's answer to the tool seam's fourth
// document. A governed tool server gets a RemoteMCPServer whose URL kmx
// derives, because getting that string wrong points an agent at a 404
// and says nothing. A model has the same hazard and no custom resource
// to put it in — a ModelConfig is a kagent CRD, and the adopter this
// command exists for has no kagent — so the string is PRINTED, with
// everything a client needs beside it.
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
func tokenFieldsOf(protocol string) string {
	if protocol == "responses" {
		return "input_tokens / output_tokens"
	}
	return "prompt_tokens / completion_tokens"
}

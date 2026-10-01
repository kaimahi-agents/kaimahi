package app

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/guard"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/orkaschema"
	agentruntime "github.com/kaimahi-agents/kaimahi/internal/kmx/runtime"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/scaffold"
)

// All Orka API subprocesses have both Kubernetes and process deadlines. Raw
// stderr is classified, never propagated: it may contain a TokenRequest or
// admission echo.
func (a *App) orkaCapture(ctx context.Context, stdin []byte, args ...string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	callCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	prepared := a.Command(append([]string{"--request-timeout=10s"}, args...)...)
	cmd := exec.CommandContext(callCtx, prepared.Path, prepared.Args[1:]...)
	cmd.Env = prepared.Env
	cmd.WaitDelay = time.Second
	cmd.Stdin = bytes.NewReader(stdin)
	out := &orkaBoundedBuffer{remaining: 4 << 20}
	// Classify stderr without returning its contents: API errors can echo
	// admission payloads, tokens or Secret data. Only fixed, safe reasons
	// leave this boundary.
	stderr := &orkaBoundedBuffer{remaining: 4 << 10}
	cmd.Stdout, cmd.Stderr = out, stderr
	if err := cmd.Run(); err != nil {
		if callCtx.Err() != nil {
			if err := ctx.Err(); err != nil {
				return nil, fmt.Errorf("kubectl request cancelled or timed out: %w", err)
			}
			return nil, fmt.Errorf("kubectl request cancelled or timed out")
		}
		reason := strings.ToLower(stderr.buffer.String())
		switch {
		case strings.Contains(reason, "notfound") || strings.Contains(reason, "not found"):
			if strings.Contains(reason, "namespaces") && strings.Contains(reason, OrkaNamespace) {
				return nil, fmt.Errorf("kubectl request failed: namespace %s not found", OrkaNamespace)
			}
			return nil, fmt.Errorf("kubectl request failed: requested Kubernetes resource not found")
		case strings.Contains(reason, "forbidden"), strings.Contains(reason, "unauthorized"):
			return nil, fmt.Errorf("kubectl request failed: access forbidden")
		default:
			return nil, fmt.Errorf("kubectl request failed; check permissions, prerequisites and the selected context")
		}
	}
	return out.buffer.Bytes(), nil
}

type orkaBoundedBuffer struct {
	// Do not embed bytes.Buffer: its promoted ReadFrom would let io.Copy
	// bypass Write and therefore bypass the subprocess response bound.
	buffer    bytes.Buffer
	remaining int
}

func (b *orkaBoundedBuffer) Write(p []byte) (int, error) {
	if len(p) > b.remaining {
		return 0, fmt.Errorf("kubectl response exceeds size limit")
	}
	b.remaining -= len(p)
	return b.buffer.Write(p)
}

// Reuse the existing mutation guard, but read its metadata through the same
// pinned, cancellable adapter as this command's other reads. Namespace is the
// user's explicit Orka selection, not the wider banner's fixed list.
func (a *App) guardOrkaCreate(ctx context.Context, opt CreateOptions) error {
	return a.guardOrkaMutation(ctx, opt, "")
}

func (a *App) guardOrkaMutation(ctx context.Context, opt CreateOptions, reconcileAction string) error {
	if a.guarded {
		return nil
	}
	raw, err := a.orkaCapture(ctx, nil, "config", "view", "-o", "json")
	if err != nil {
		return fmt.Errorf("cannot read context metadata for the mutation guard: %w", err)
	}
	cfg, err := guard.ParseKubeconfig(raw)
	if err != nil {
		return fmt.Errorf("cannot decode context metadata for the mutation guard")
	}
	command := a.InvocationCommand
	if command == "" {
		command = a.operationCommand("agent", "create", opt.Name)
	}
	action := "create Orka Provider and Agent in " + opt.Namespace
	if opt.Task != "" && !opt.DryRun {
		action += " and execute a model Task"
	}
	if opt.DryRun {
		action = "server dry-run Orka resources in " + opt.Namespace
	}
	if reconcileAction != "" {
		action = reconcileAction
	}
	if err := guard.CheckContext(ctx, cfg, guard.Request{Action: action, Context: a.Cfg.KubeContext, Source: a.Cfg.ContextSource, Namespaces: opt.Namespace, Confirm: a.Cfg.Confirm, Command: command}, a.Err, a.Stdin); err != nil {
		return err
	}
	a.guarded = true
	return nil
}

// createOrkaOnline deploys a bundle that was not rendered from a portable
// document. Lift is its caller: lift assembles its bundle from objects that
// already exist in the source cluster, so there is no portable source to
// render and no rendered bytes to emit — the artifact is serialized from the
// bundle itself, exactly as it always was.
func (a *App) createOrkaOnline(ctx context.Context, opt CreateOptions, bundle *scaffold.OrkaBundle) error {
	_, err := a.createOrkaStaged(ctx, opt, bundle, bundle.YAML, nil, nil)
	return err
}

// orkaStagedResource is one Provider, Agent or Task the staged create left
// current-generation Ready (or, for a Task, succeeded), with what it did to it.
type orkaStagedResource struct {
	id      orkaIdentity
	outcome agentruntime.ResourceOutcome
}

// createOrkaStaged runs the staged online create: mutation guard, installed
// CRD validation, collision checks, the separately provisioned Secret's key
// proof, strict server dry-runs, artifact emission, and Provider → Ready →
// Agent → Ready → optional Task/result in that order.
//
// artifact renders the reviewable document for the provenance this deploy
// resolved. It is a function rather than a string because the provenance is
// only known here, after the installed CRDs have been read — and it is a
// parameter rather than a call to bundle.YAML so that a caller holding the
// exact bytes it is deploying can emit those bytes instead of a
// re-serialized copy of them.
//
// reconcile, when set, is the rendered bundle a create is applying, and makes
// that create rerunnable: Provider and Agent go through reconcile Deploy's
// inspection and writes, so resources carrying this bundle's markers are
// reused or updated, identical unmarked ones adopted and anything else
// refused, and an identical existing artifact is kept. The optional Task stays
// create-only and still runs only after both are Ready. Without it (lift, and
// --dry-run) every resource must be absent, as it always had to be.
//
// It returns the Agent's identity so a lifecycle Deploy can name it. A
// --dry-run create writes nothing and returns a zero identity.
func (a *App) createOrkaStaged(ctx context.Context, opt CreateOptions, bundle *scaffold.OrkaBundle, artifact func(provenance string) (string, error), resources *[]orkaStagedResource, reconcile *agentruntime.RenderedBundle) (agent orkaIdentity, err error) {
	stage := "Validate schemas and prerequisites"
	report := func(status string, err error) {
		if a.operationProgress != nil {
			a.operationProgress(stage, status, err)
		}
	}
	report("active", nil)
	defer func() {
		if err != nil {
			report("failed", err)
		}
	}()
	if reconcile != nil && (opt.DryRun || a.liftReuse) {
		return orkaIdentity{}, fmt.Errorf("Orka reconcile requires an online create, not --dry-run or lift")
	}
	if err := a.guardOrkaCreate(ctx, opt); err != nil {
		return orkaIdentity{}, err
	}
	crds := map[string][]byte{}
	for _, kind := range []string{"Agent", "Provider", "Task"} {
		name := strings.ToLower(kind) + "s.core.orka.ai"
		raw, err := a.orkaCapture(ctx, nil, "get", "crd", name, "-o", "json")
		if err != nil {
			return orkaIdentity{}, fmt.Errorf("cannot read installed %s CRD (no offline fallback): %w", name, err)
		}
		crds[kind] = raw
	}
	validator, err := orkaschema.Installed(crds)
	if err != nil {
		return orkaIdentity{}, err
	}
	if err := validateOrkaBundle(bundle, validator); err != nil {
		return orkaIdentity{}, err
	}
	document, err := artifact(validator.Provenance())
	if err != nil {
		return orkaIdentity{}, err
	}
	existing := map[string]*orkaIdentity{}
	if a.liftReuse && bundle.Task != nil {
		return orkaIdentity{}, fmt.Errorf("lift cannot reuse or resubmit Tasks")
	}
	for _, doc := range bundle.Documents()[1:] {
		if a.liftReuse {
			id, err := a.matchingOrkaResource(ctx, opt.Namespace, doc)
			if err != nil {
				return orkaIdentity{}, err
			}
			if id != nil {
				existing[id.Kind] = id
			}
			continue
		}
		if reconcile != nil && doc["kind"] != "Task" {
			continue // Inspected, with its admission dry-run, in the next stage.
		}
		if err := a.orkaAbsent(ctx, opt.Namespace, doc); err != nil {
			return orkaIdentity{}, err
		}
	}
	key := bundle.Provider["spec"].(map[string]any)["secretRef"].(map[string]any)["key"].(string)
	if err := a.orkaProviderSecretPresent(ctx, opt.Namespace, opt.Secret, key); err != nil {
		return orkaIdentity{}, err
	}
	report("done", nil)
	stage = "Validate server admission"
	report("active", nil)
	for _, doc := range bundle.Documents()[1:] {
		if existing[doc["kind"].(string)] != nil {
			continue
		}
		if reconcile != nil && doc["kind"] != "Task" {
			// Read-only: refuses a resource this bundle may not own before
			// anything is written, and server dry-runs the write it would make.
			if _, err := a.inspectOrkaReconcile(ctx, opt.Namespace, doc, *reconcile); err != nil {
				return orkaIdentity{}, err
			}
			continue
		}
		body, err := json.Marshal(doc)
		if err != nil {
			return orkaIdentity{}, err
		}
		if _, err := a.orkaCapture(ctx, body, "-n", opt.Namespace, "create", "--dry-run=server", "--validate=strict", "-f", "-", "-o", "json"); err != nil {
			return orkaIdentity{}, fmt.Errorf("%s strict server create preflight failed: %w", doc["kind"], err)
		}
	}
	if opt.DryRun {
		if err := a.emitOrka(opt, document); err != nil {
			return orkaIdentity{}, err
		}
		a.notef("Orka bundle validated against installed schemas and server admission; not applied; result access and execution were not tested.")
		return orkaIdentity{}, nil
	}
	var session *orkaResultSession
	if bundle.Task != nil {
		session, err = a.openOrkaResultSession(ctx, opt)
		if err != nil {
			return orkaIdentity{}, err
		}
		defer session.close()
		// Stop dependency waits and later writes if the forward or its sole
		// result connection is lost. Never recover by resubmitting the Task.
		ctx = session.ctx
		if err := session.probe(ctx, opt.Namespace, orkaObjectName(bundle.Task)); err != nil {
			return orkaIdentity{}, err
		}
	}
	if !a.liftReuse {
		// Only a rerunnable create may keep an identical artifact: a Task's
		// random identity means its artifact never matches a later run.
		if err := a.emitOrkaArtifact(opt, document, reconcile != nil && bundle.Task == nil); err != nil {
			return orkaIdentity{}, err
		}
	}
	report("done", nil)
	var created []string
	defer func() {
		if err != nil {
			if reconcile == nil {
				a.notef("Stopped; no later resources attempted, no rollback or adoption. Created: %s", strings.Join(created, ", "))
				return
			}
			a.notef("Stopped; no later resources attempted, nothing rolled back. Created: %s", strings.Join(created, ", "))
			err = fmt.Errorf("%w\n%s", err, orkaRerunAdvice(bundle.Task != nil))
		}
	}()
	for _, doc := range []map[string]any{bundle.Provider, bundle.Agent} {
		stage = "Create " + doc["kind"].(string)
		report("active", nil)
		var id orkaIdentity
		var err error
		outcome := agentruntime.ResourceCreated
		if a.liftReuse {
			match, checkErr := a.matchingOrkaResource(ctx, opt.Namespace, doc)
			if checkErr != nil {
				return orkaIdentity{}, checkErr
			}
			if match != nil {
				id = *match
				outcome = agentruntime.ResourceReused
			}
		}
		switch {
		case reconcile != nil:
			// Reinspect immediately before the write: another writer may have
			// changed the preflight decision.
			var check orkaReconcileCheck
			check, err = a.inspectOrkaReconcile(ctx, opt.Namespace, doc, *reconcile)
			if err != nil {
				return orkaIdentity{}, err
			}
			outcome = check.outcome
			if outcome == agentruntime.ResourceAdopted {
				a.notef("Adopting identical unmarked %s/%s", check.id.Kind, check.id.Name)
			}
			id, err = a.applyOrkaReconcile(ctx, opt.Namespace, check)
		case id.UID == "":
			id, err = a.createOrkaObject(ctx, opt.Namespace, doc)
		default:
			a.notef("Reusing matching %s/%s", id.Kind, id.Name)
		}
		if err != nil {
			return orkaIdentity{}, err
		}
		if id.Kind == "Agent" {
			agent = id
		}
		if outcome == agentruntime.ResourceReused {
			report("skipped", nil)
		} else {
			report("done", nil)
		}
		stage = "Wait for " + id.Kind + " Ready"
		report("active", nil)
		if outcome == agentruntime.ResourceCreated || reconcile == nil {
			created = append(created, id.Kind+"/"+id.Name+" UID "+id.UID)
			a.notef("Created %s/%s (UID %s); waiting for current-generation Ready.", id.Kind, id.Name, id.UID)
		} else {
			a.notef("%s/%s (UID %s) %s; waiting for current-generation Ready.", id.Kind, id.Name, id.UID, outcome)
		}
		if err := a.waitOrkaReady(ctx, opt.Namespace, id); err != nil {
			return orkaIdentity{}, err
		}
		if reconcile != nil {
			if err := a.verifyOrkaReconcile(ctx, opt.Namespace, doc, *reconcile, id); err != nil {
				return orkaIdentity{}, err
			}
		}
		report("done", nil)
		if resources != nil {
			*resources = append(*resources, orkaStagedResource{id: id, outcome: outcome})
		}
	}
	if bundle.Task == nil {
		a.notef("Orka Provider and Agent are Ready; no model response was tested.")
		return agent, nil
	}
	id, err := a.createOrkaObject(ctx, opt.Namespace, bundle.Task)
	if err != nil {
		return orkaIdentity{}, err
	}
	created = append(created, "Task/"+id.Name+" UID "+id.UID)
	a.notef("Created Task/%s (UID %s); waiting for execution and a retrievable answer.", id.Name, id.UID)
	answer, err := a.waitOrkaTaskResult(ctx, opt.Namespace, id, session)
	if err != nil {
		return orkaIdentity{}, err
	}
	if _, err := fmt.Fprintln(a.Out, answer); err != nil {
		return orkaIdentity{}, err
	}
	a.notef("Task/%s UID %s succeeded and its answer was retrieved. Fresh-name and UID checks do not bind the result bytes to a UID.", id.Name, id.UID)
	if resources != nil {
		*resources = append(*resources, orkaStagedResource{id: id, outcome: agentruntime.ResourceCreated})
	}
	return agent, nil
}

// orkaRerunAdvice tells an operator whose create stopped part-way what a
// rerun does. Provider and Agent carry this bundle's markers from their first
// write, so the same command reuses them; a Task never is reused, and its
// fresh identity changes the artifact, so a --task rerun needs a new --out.
func orkaRerunAdvice(task bool) string {
	if task {
		return "Rerunning is safe with a new --out: the Provider and Agent this bundle owns are reused, and a new Task is submitted; the stopped Task is never resubmitted."
	}
	return "Rerunning the same command is safe: the Provider and Agent this bundle owns are reused or updated, and nothing else is overwritten."
}

func orkaObjectName(doc map[string]any) string {
	return doc["metadata"].(map[string]any)["name"].(string)
}

// orkaProviderSecretPresent proves the referenced Secret KEY exists before
// anything is created, and never reads its value.
//
// kmx does not provision Provider credentials: a Secret it wrote a skeleton
// into would be a credential nobody minted, and an Agent wired to it would
// fail at the first model call with an authentication error instead of here.
func (a *App) orkaProviderSecretPresent(ctx context.Context, namespace, secret, key string) error {
	// The validated key is quoted for the Go template. Iterate key NAMES only;
	// even an empty credential value counts as present, never as authenticated.
	template := "go-template=secret\n{{range $key, $_ := .data}}{{if eq $key " + fmt.Sprintf("%q", key) + "}}present{{end}}{{end}}"
	marker, err := a.orkaCapture(ctx, nil, "-n", namespace, "get", "secret", secret, "--ignore-not-found=true", "-o", template)
	if err != nil {
		return fmt.Errorf("cannot read Provider Secret key presence: %w", err)
	}
	switch string(marker) {
	case "":
		return fmt.Errorf("Provider Secret %s/%s is missing; provision it separately, never create the skeleton", namespace, secret)
	case "secret\n":
		return fmt.Errorf("Provider Secret exists but its referenced key is missing; provision that key separately")
	case "secret\npresent":
		return nil
	default:
		return fmt.Errorf("Provider Secret key check returned an invalid presence marker")
	}
}

func orkaPlural(kind string) string { return strings.ToLower(kind) + "s.core.orka.ai" }

func (a *App) orkaAbsent(ctx context.Context, namespace string, doc map[string]any) error {
	kind, name := doc["kind"].(string), orkaObjectName(doc)
	raw, err := a.orkaCapture(ctx, nil, "-n", namespace, "get", orkaPlural(kind), name, "--ignore-not-found=true", "-o", "name")
	if err != nil {
		return fmt.Errorf("cannot establish %s/%s absence: %w", kind, name, err)
	}
	if len(bytes.TrimSpace(raw)) != 0 {
		return fmt.Errorf("%s/%s already exists; refusing any collision, including identical/shared resources", kind, name)
	}
	return nil
}

type orkaIdentity struct {
	Kind, Name, UID string
	Generation      int64
}
type orkaObject struct {
	Kind     string `json:"kind"`
	Metadata struct {
		Name              string     `json:"name"`
		Namespace         string     `json:"namespace"`
		UID               string     `json:"uid"`
		Generation        int64      `json:"generation"`
		DeletionTimestamp *time.Time `json:"deletionTimestamp"`
	} `json:"metadata"`
	Status struct {
		Ready      bool              `json:"ready"`
		Conditions []serverCondition `json:"conditions"`
		Phase      string            `json:"phase"`
		ResultRef  struct {
			Available bool `json:"available"`
		} `json:"resultRef"`
	} `json:"status"`
}

func (a *App) createOrkaObject(ctx context.Context, namespace string, doc map[string]any) (orkaIdentity, error) {
	id := orkaIdentity{Kind: doc["kind"].(string), Name: orkaObjectName(doc)}
	body, err := json.Marshal(doc)
	if err != nil {
		return id, err
	}
	raw, err := a.orkaCapture(ctx, body, "-n", namespace, "create", "--validate=strict", "-f", "-", "-o", "json")
	if err != nil {
		return id, fmt.Errorf("create %s/%s failed or was ambiguous; it may exist or be running. No execution retry, adoption or cleanup: %w", id.Kind, id.Name, err)
	}
	var object orkaObject
	if err := json.Unmarshal(raw, &object); err != nil || object.Kind != id.Kind || object.Metadata.Name != id.Name || object.Metadata.Namespace != namespace || object.Metadata.UID == "" || object.Metadata.Generation < 1 {
		return id, fmt.Errorf("create %s/%s returned no valid identity; it may exist or be running. No retry", id.Kind, id.Name)
	}
	id.UID, id.Generation = object.Metadata.UID, object.Metadata.Generation
	return id, nil
}

func (a *App) readOrkaObject(ctx context.Context, namespace string, id orkaIdentity) (*orkaObject, error) {
	raw, err := a.orkaCapture(ctx, nil, "-n", namespace, "get", orkaPlural(id.Kind), id.Name, "-o", "json")
	if err != nil {
		return nil, fmt.Errorf("cannot read %s/%s UID %s; it may have disappeared: %w", id.Kind, id.Name, id.UID, err)
	}
	var object orkaObject
	if err := json.Unmarshal(raw, &object); err != nil {
		return nil, fmt.Errorf("invalid %s/%s status response", id.Kind, id.Name)
	}
	if object.Kind != id.Kind || object.Metadata.Name != id.Name || object.Metadata.Namespace != namespace || object.Metadata.UID != id.UID || object.Metadata.Generation != id.Generation {
		return nil, fmt.Errorf("%s/%s UID %s was replaced or its spec generation changed; refusing stale state", id.Kind, id.Name, id.UID)
	}
	if object.Metadata.DeletionTimestamp != nil {
		return nil, fmt.Errorf("%s/%s UID %s is terminating; refusing stale state", id.Kind, id.Name, id.UID)
	}
	return &object, nil
}

func (a *App) waitOrkaReady(ctx context.Context, namespace string, id orkaIdentity) error {
	for {
		object, err := a.readOrkaObject(ctx, namespace, id)
		if err != nil {
			return err
		}
		if orkaCurrentGenerationReady(object, id.Generation) {
			return nil
		}
		if err := orkaPause(ctx); err != nil {
			return fmt.Errorf("waiting for %s/%s UID %s current-generation Ready: %w", id.Kind, id.Name, id.UID, err)
		}
	}
}

// verifyOrkaReadyNow is a bounded final observation, not another wait: a
// resource that regressed while later resources started cannot earn a receipt.
func (a *App) verifyOrkaReadyNow(ctx context.Context, namespace string, id orkaIdentity) error {
	object, err := a.readOrkaObject(ctx, namespace, id)
	if err != nil {
		return err
	}
	if !orkaCurrentGenerationReady(object, id.Generation) {
		return fmt.Errorf("%s/%s UID %s is no longer current-generation Ready; refusing deployment receipt", id.Kind, id.Name, id.UID)
	}
	return nil
}

func orkaCurrentGenerationReady(object *orkaObject, generation int64) bool {
	if !object.Status.Ready {
		return false
	}
	for _, condition := range object.Status.Conditions {
		if condition.Type == "Ready" && condition.Status == "True" && condition.ObservedGeneration == generation {
			return true
		}
	}
	return false
}

func orkaPause(ctx context.Context) error {
	timer := time.NewTimer(time.Second)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

package app

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"time"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/guard"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/orkaschema"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/scaffold"
)

// All Orka API subprocesses have both Kubernetes and process deadlines. Raw
// stderr is never propagated: it may contain a TokenRequest or admission echo.
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
	cmd.Stdout, cmd.Stderr = out, io.Discard
	if err := cmd.Run(); err != nil {
		if callCtx.Err() != nil {
			return nil, fmt.Errorf("kubectl request cancelled or timed out")
		}
		return nil, fmt.Errorf("kubectl request failed; check permissions, prerequisites and the selected context")
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
// user's explicit Orka selection, not the legacy kagent banner's fixed list.
func (a *App) guardOrkaCreate(ctx context.Context, opt CreateOptions) error {
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
	_, err := a.createOrkaStaged(ctx, opt, bundle, bundle.YAML)
	return err
}

// createOrkaStaged runs the staged online create: mutation guard, installed
// CRD validation, collision checks, the separately provisioned Secret's key
// proof, strict server dry-runs, artifact emission, and Provider → Ready →
// Agent → Ready → optional Task/result in that order. Nothing about that
// sequence changed when the seam was added.
//
// artifact renders the reviewable document for the provenance this deploy
// resolved. It is a function rather than a string because the provenance is
// only known here, after the installed CRDs have been read — and it is a
// parameter rather than a call to bundle.YAML so that a caller holding the
// exact bytes it is deploying can emit those bytes instead of a
// re-serialized copy of them.
//
// It returns the created Agent's identity so a lifecycle Deploy can name what
// it created. A --dry-run create writes nothing and returns a zero identity.
func (a *App) createOrkaStaged(ctx context.Context, opt CreateOptions, bundle *scaffold.OrkaBundle, artifact func(provenance string) (string, error)) (agent orkaIdentity, err error) {
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
			id, err := a.matchingLiftResource(ctx, opt.Namespace, doc)
			if err != nil {
				return orkaIdentity{}, err
			}
			if id != nil {
				existing[id.Kind] = id
			}
			continue
		}
		if err := a.orkaAbsent(ctx, opt.Namespace, doc); err != nil {
			return orkaIdentity{}, err
		}
	}
	key := bundle.Provider["spec"].(map[string]any)["secretRef"].(map[string]any)["key"].(string)
	// The validated key is quoted for the Go template. Iterate key NAMES only;
	// even an empty credential value counts as present, never as authenticated.
	template := "go-template=secret\n{{range $key, $_ := .data}}{{if eq $key " + fmt.Sprintf("%q", key) + "}}present{{end}}{{end}}"
	marker, err := a.orkaCapture(ctx, nil, "-n", opt.Namespace, "get", "secret", opt.Secret, "--ignore-not-found=true", "-o", template)
	if err != nil {
		return orkaIdentity{}, fmt.Errorf("cannot read Provider Secret key presence: %w", err)
	}
	switch string(marker) {
	case "":
		return orkaIdentity{}, fmt.Errorf("Provider Secret %s/%s is missing; provision it separately, never create the skeleton", opt.Namespace, opt.Secret)
	case "secret\n":
		return orkaIdentity{}, fmt.Errorf("Provider Secret exists but its referenced key is missing; provision that key separately")
	case "secret\npresent":
	default:
		return orkaIdentity{}, fmt.Errorf("Provider Secret key check returned an invalid presence marker")
	}
	report("done", nil)
	stage = "Validate server admission"
	report("active", nil)
	for _, doc := range bundle.Documents()[1:] {
		if existing[doc["kind"].(string)] != nil {
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
		if err := a.emitOrka(opt, document); err != nil {
			return orkaIdentity{}, err
		}
	}
	report("done", nil)
	var created []string
	defer func() {
		if err != nil {
			a.notef("Stopped; no later resources attempted, no rollback or adoption. Created: %s", strings.Join(created, ", "))
		}
	}()
	for _, doc := range []map[string]any{bundle.Provider, bundle.Agent} {
		stage = "Create " + doc["kind"].(string)
		report("active", nil)
		var id orkaIdentity
		var err error
		reused := false
		if a.liftReuse {
			match, checkErr := a.matchingLiftResource(ctx, opt.Namespace, doc)
			if checkErr != nil {
				return orkaIdentity{}, checkErr
			}
			if match != nil {
				id = *match
				reused = true
			}
		}
		if id.UID == "" {
			id, err = a.createOrkaObject(ctx, opt.Namespace, doc)
		} else {
			a.notef("Reusing matching %s/%s", id.Kind, id.Name)
		}
		if err != nil {
			return orkaIdentity{}, err
		}
		if id.Kind == "Agent" {
			agent = id
		}
		created = append(created, id.Kind+"/"+id.Name+" UID "+id.UID)
		if reused {
			report("skipped", nil)
		} else {
			report("done", nil)
		}
		stage = "Wait for " + id.Kind + " Ready"
		report("active", nil)
		a.notef("Created %s/%s (UID %s); waiting for current-generation Ready.", id.Kind, id.Name, id.UID)
		if err := a.waitOrkaReady(ctx, opt.Namespace, id); err != nil {
			return orkaIdentity{}, err
		}
		report("done", nil)
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
	return agent, nil
}

func orkaObjectName(doc map[string]any) string {
	return doc["metadata"].(map[string]any)["name"].(string)
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
		if object.Status.Ready {
			for _, condition := range object.Status.Conditions {
				if condition.Type == "Ready" && condition.Status == "True" && condition.ObservedGeneration == id.Generation {
					return nil
				}
			}
		}
		if err := orkaPause(ctx); err != nil {
			return fmt.Errorf("waiting for %s/%s UID %s current-generation Ready: %w", id.Kind, id.Name, id.UID, err)
		}
	}
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

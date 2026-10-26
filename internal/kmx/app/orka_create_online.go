package app

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/guard"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/runview"
	"os/exec"
	"strings"
	"time"
)

type orkaBoundedBuffer struct {
	// Do not embed bytes.Buffer: its promoted ReadFrom would let io.Copy
	// bypass Write and therefore bypass the subprocess response bound.
	buffer    bytes.Buffer
	remaining int
}

type orkaIdentity struct {
	Kind, Name, UID string
	Generation      int64
}

type orkaObject struct {
	Enforcement *runview.Enforcement `json:"-"`
	Kind        string               `json:"kind"`
	Metadata    struct {
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
	if err := a.executeCapture(callCtx, cmd); err != nil {
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

func (b *orkaBoundedBuffer) Write(p []byte) (int, error) {
	if len(p) > b.remaining {
		return 0, fmt.Errorf("kubectl response exceeds size limit")
	}
	b.remaining -= len(p)
	return b.buffer.Write(p)
}

func (a *App) guardOrkaMutation(ctx context.Context, opt CreateOptions, reconcileAction string) error {
	if a.liftConsent != nil {
		if err := a.liftConsent.check(ctx, a, opt); err != nil {
			return err
		}
		a.guarded = true
		return nil
	}
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
		command = a.operationCommand("create", "--live", opt.Name)
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

func (a *App) createOrkaObject(ctx context.Context, namespace string, doc map[string]any) (orkaIdentity, error) {
	id, _, err := a.createOrkaObjectObserved(ctx, namespace, doc)
	return id, err
}

// Keep the CREATE observation available to callers that bind admitted behavior.
// A failed or ambiguous create is never followed by a recovery read.
func (a *App) createOrkaObjectObserved(ctx context.Context, namespace string, doc map[string]any) (orkaIdentity, []byte, error) {
	id := orkaIdentity{Kind: doc["kind"].(string), Name: orkaObjectName(doc)}
	body, err := json.Marshal(doc)
	if err != nil {
		return id, nil, err
	}
	raw, err := a.orkaCapture(ctx, body, "-n", namespace, "create", "--validate=strict", "-f", "-", "-o", "json")
	if err != nil {
		return id, nil, fmt.Errorf("create %s/%s failed or was ambiguous; it may exist or be running. No execution retry, adoption or cleanup: %w", id.Kind, id.Name, err)
	}
	var object orkaObject
	if err := json.Unmarshal(raw, &object); err != nil || object.Kind != id.Kind || object.Metadata.Name != id.Name || object.Metadata.Namespace != namespace || object.Metadata.UID == "" || object.Metadata.Generation < 1 {
		return id, nil, fmt.Errorf("create %s/%s returned no valid identity; it may exist or be running. No retry", id.Kind, id.Name)
	}
	id.UID, id.Generation = object.Metadata.UID, object.Metadata.Generation
	return id, raw, nil
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
	started := false
	for {
		object, err := a.readOrkaObject(ctx, namespace, id)
		if err != nil {
			return err
		}
		if orkaCurrentGenerationReady(object, id.Generation) {
			return nil
		}
		if !started {
			var cancel context.CancelFunc
			ctx, cancel = a.waitContext(ctx, "orka-ready-"+strings.ToLower(id.Kind), 0)
			defer cancel()
			started = true
		}
		if err := a.pause(ctx, time.Second); err != nil {
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

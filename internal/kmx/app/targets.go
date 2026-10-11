package app

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"slices"
	"strings"
	"text/tabwriter"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentsessions"
	agentruntime "github.com/kaimahi-agents/kaimahi/internal/kmx/runtime"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/scaffold"
)

type TargetsOptions struct {
	Output               string
	Detect               bool
	Sessions, SessionsCA string
}

// TargetsReport describes compiled command support separately from observed
// installation evidence. It neither selects a runtime nor grants mutation authority.
type TargetsReport struct {
	SchemaVersion int          `json:"schemaVersion"`
	Targets       []TargetInfo `json:"targets"`
}
type TargetInfo struct {
	ID                string                `json:"id"`
	DisplayName       string                `json:"displayName"`
	Kind              string                `json:"kind"`
	SupportTier       string                `json:"supportTier"`
	QualifiedVersions []TargetQualification `json:"qualifiedVersions"`
	Capabilities      []TargetCapability    `json:"capabilities"`
	Detection         TargetDetection       `json:"detection"`
}
type TargetQualification struct {
	Version  string `json:"version"`
	Scope    string `json:"scope"`
	Evidence string `json:"evidence"`
}
type TargetCapability struct {
	Operation string `json:"operation"`
	Supported bool   `json:"supported"`
	Reason    string `json:"reason"`
}
type TargetDetection struct {
	State   string                `json:"state"`
	Scope   string                `json:"scope"`
	Version string                `json:"version,omitempty"`
	Detail  string                `json:"detail"`
	Error   *TargetDetectionError `json:"error,omitempty"`
}

// TargetDetectionError uses fixed diagnostics; external response bodies are
// never printable errors. A failed observation cannot become absence.
type TargetDetectionError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Action  string `json:"action"`
}

func (e *TargetDetectionError) Error() string { return e.Message + "; " + e.Action }

type TargetsUnreadableError struct{ Runtimes []string }

func (e *TargetsUnreadableError) Error() string {
	return "target detection unreadable for " + strings.Join(e.Runtimes, ", ") + "; see the reported error codes and actions"
}

// compiledTargets is the diagnostic composition root, not another runtime
// registry. Research candidates and unwired experimental ports do not appear here.
func compiledTargets() []TargetInfo {
	orka := orkaRuntimeAdapter{create: &CreateOptions{}}
	kagent := kagentRuntimeAdapter{create: &CreateOptions{}, bindings: &agentruntime.KagentBindings{}}
	rows := []TargetInfo{
		{ID: string(orka.ID()), DisplayName: "Orka", Kind: "adapter", SupportTier: "supported",
			QualifiedVersions: []TargetQualification{{Version: OrkaVersion, Scope: "native Orka commands", Evidence: "SHA-256-pinned release chart; e2e-orka-runtime"}},
			Capabilities:      lifecycleTargetCapabilities(orka),
			Detection:         TargetDetection{State: "not-probed", Scope: "controller Deployments in orka-system", Detail: "use --detect with an explicit or saved Kubernetes context"}},
		{ID: string(kagent.ID()), DisplayName: "Kagent", Kind: "adapter", SupportTier: "create-only",
			QualifiedVersions: []TargetQualification{{Version: scaffold.KagentVersion, Scope: "explicit create only", Evidence: "exact controller commit and OCI image checks; e2e-kagent-create"}},
			Capabilities:      lifecycleTargetCapabilities(kagent),
			Detection:         TargetDetection{State: "not-probed", Scope: "explicit create only", Detail: "no Kagent discovery; create validates its preinstalled exact version"}},
		{ID: "agentsessions", DisplayName: "agentsessions", Kind: "integration", SupportTier: "eval-only",
			QualifiedVersions: []TargetQualification{{Version: agentsessions.ReferenceRevision, Scope: "reference-chat evaluation and local replay only; not the remote host version", Evidence: "pinned Go module; e2e-eval-loop"}},
			Detection:         TargetDetection{State: "not-probed", Scope: "Sessions API only", Detail: "use --sessions host:port; HarnessRegistry is not probed or qualified"}},
	}
	for _, operation := range []string{"render", "deploy", "status", "evaluate", "chat", "run", "retire", "verify", "logs", "diff", "rollback"} {
		supported := operation == "evaluate" || operation == "verify"
		reason := "requires the qualified text-only reference chat profile; no registration or lift gate"
		if !supported {
			reason = (&agentruntime.UnsupportedVerbError{Runtime: "agentsessions", Verb: operation}).Error() + "; lifecycle adapter blocked on an operator-only HarnessRegistry listener and shared conformance"
		}
		rows[2].Capabilities = append(rows[2].Capabilities, TargetCapability{Operation: operation, Supported: supported, Reason: reason})
	}
	return rows
}

func lifecycleTargetCapabilities(adapter agentruntime.LifecycleAdapter) []TargetCapability {
	c := adapter.Capabilities()
	support := map[string]bool{"render": c.Render, "deploy": c.Deploy, "status": c.Status, "evaluate": c.Evaluate}
	if adapter.ID() == agentruntime.Orka {
		support["chat"], support["run"], support["retire"] = true, true, true
	}
	var result []TargetCapability
	for _, operation := range []string{"render", "deploy", "status", "evaluate", "chat", "run", "retire", "verify", "logs", "diff", "rollback"} {
		supported := support[operation]
		reason := "requires valid command input and a prepared supported target; detection is not readiness or semantic-result proof"
		if !supported {
			reason = (&agentruntime.UnsupportedVerbError{Runtime: adapter.ID(), Verb: operation}).Error() + "; use only the supported commands listed here"
		} else if adapter.ID() == agentruntime.Kagent {
			reason = "explicit exact-v0.10.2 create only; no installation, reconciliation, adoption or rollback"
		} else if operation == "retire" {
			reason = "native Orka workload delete/release; not retained-registration retirement; no target teardown"
		}
		result = append(result, TargetCapability{Operation: operation, Supported: supported, Reason: reason})
	}
	return result
}

func (a *App) Targets(opt TargetsOptions) error {
	if opt.Output != "table" && opt.Output != "json" {
		return fmt.Errorf("targets output must be table or json")
	}
	if a.Out == nil {
		return fmt.Errorf("targets requires an output stream")
	}
	if opt.SessionsCA != "" && opt.Sessions == "" {
		return fmt.Errorf("--sessions-ca requires --sessions")
	}
	if opt.Sessions != "" {
		if _, err := agentsessions.NormalizeAddress(opt.Sessions); err != nil {
			return err
		}
	}
	report := TargetsReport{SchemaVersion: 1, Targets: compiledTargets()}
	if opt.Detect {
		report.Targets[0].Detection = a.detectOrkaTarget()
	}
	if opt.Sessions != "" {
		report.Targets[2].Detection = a.detectSessionsTarget(opt)
	}
	slices.SortFunc(report.Targets, func(a, b TargetInfo) int { return strings.Compare(a.ID, b.ID) })
	if opt.Output == "json" {
		if err := json.NewEncoder(a.Out).Encode(report); err != nil {
			return err
		}
	} else if err := writeTargetsTable(a.Out, report); err != nil {
		return err
	}
	var unreadable []string
	for _, row := range report.Targets {
		if row.Detection.Error != nil {
			unreadable = append(unreadable, row.ID)
		}
	}
	if len(unreadable) > 0 {
		return &TargetsUnreadableError{Runtimes: unreadable}
	}
	return nil
}

func writeTargetsTable(out io.Writer, report TargetsReport) error {
	var buffer bytes.Buffer
	w := tabwriter.NewWriter(&buffer, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "RUNTIME\tKIND\tCOMPILED SUPPORT\tDETECTION\tOBSERVED VERSION")
	for _, row := range report.Targets {
		version := row.Detection.Version
		if version == "" {
			version = "unknown"
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", row.ID, row.Kind, row.SupportTier, row.Detection.State, version)
	}
	if err := w.Flush(); err != nil {
		return err
	}
	for _, row := range report.Targets {
		fmt.Fprintf(&buffer, "\n%s — %s\n  detection scope: %s\n  %s\n", row.ID, row.DisplayName, row.Detection.Scope, row.Detection.Detail)
		if row.Detection.Error != nil {
			fmt.Fprintf(&buffer, "  error [%s]: %s\n", row.Detection.Error.Code, row.Detection.Error)
		}
		for _, q := range row.QualifiedVersions {
			fmt.Fprintf(&buffer, "  qualified %s: %s (%s)\n", q.Version, q.Scope, q.Evidence)
		}
		for _, c := range row.Capabilities {
			state := "unsupported"
			if c.Supported {
				state = "supported"
			}
			fmt.Fprintf(&buffer, "  %s: %s — %s\n", c.Operation, state, c.Reason)
		}
	}
	fmt.Fprintln(&buffer, "\nStatic support is not installed-target qualification. This view does not select, install or deploy a runtime. Matrix: docs/runtime-adapters.md")
	_, err := io.Copy(out, &buffer)
	return err
}

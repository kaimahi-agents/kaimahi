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
	rows := []TargetInfo{
		{ID: string(agentruntime.Orka), DisplayName: "Orka", Kind: "native-profile", SupportTier: "supported",
			QualifiedVersions: []TargetQualification{{Version: OrkaVersion, Scope: "native Orka commands and immutable suite lift", Evidence: "SHA-256-pinned release chart; native suite and Orka runtime qualification"}},
			Capabilities:      nativeTargetCapabilities(agentruntime.Orka),
			Detection:         TargetDetection{State: "not-probed", Scope: "controller Deployments in orka-system", Detail: "use --detect with an explicit or saved Kubernetes context"}},
		{ID: string(agentruntime.Kagent), DisplayName: "Kagent", Kind: "native-profile", SupportTier: "version-scoped",
			QualifiedVersions: []TargetQualification{{Version: "v0.10.3", Scope: "exact Go-only native suite create/eval and immutable digest lift", Evidence: "compiled exact pins; fresh attributable A2A evaluation; readiness is not execution proof"}},
			Capabilities:      nativeTargetCapabilities(agentruntime.Kagent),
			Detection:         TargetDetection{State: "not-probed", Scope: "explicit version-qualified native suite commands only", Detail: "no Kagent discovery; native suite requires exact v0.10.3 Go"}},
		{ID: "agentsessions", DisplayName: "agentsessions", Kind: "integration", SupportTier: "development-only",
			QualifiedVersions: []TargetQualification{{Version: agentsessions.ReferenceRevision, Scope: "pinned chat suite authoring and explicit development-host evaluation; not remote host attestation", Evidence: "pinned Go module; full-pass packaged chat evidence and reference export tests"}},
			Capabilities: []TargetCapability{
				{Operation: "create", Supported: true, Reason: "offline pinned chat suite authoring"},
				{Operation: "eval", Supported: true, Reason: "explicit private literal-loopback development host; tool-free unauthenticated one-shot execution"},
				{Operation: "push", Supported: true, Reason: "ordinary transfer or exact-S full-passing development evidence; no deployment"},
				{Operation: "export", Supported: true, Reason: "eval from-sessions exports reference cases separately; no recorded-manifest execution"},
				{Operation: "lift", Reason: "development evidence cannot authorize remote placement"},
				{Operation: "chat", Reason: "no standalone AgentSessions chat"},
				{Operation: "run", Reason: "no standalone AgentSessions run"},
				{Operation: "inspect", Reason: "no AgentSessions live inspection"},
				{Operation: "diff", Reason: "development AgentSessions receipts are refused by eval diff"},
			},
			Detection: TargetDetection{State: "not-probed", Scope: "Sessions API only", Detail: "use --sessions host:port; HarnessRegistry is not probed or qualified"}},
	}
	return rows
}

func nativeTargetCapabilities(id agentruntime.ID) []TargetCapability {
	var result []TargetCapability
	for _, operation := range []string{"create", "eval", "push", "lift", "chat", "run", "inspect", "diff"} {
		supported := id == agentruntime.Orka || operation != "chat" && operation != "run" && operation != "inspect"
		reason := "native authored workspace and immutable suite workflow; lift requires explicit evidence and reviewed Apply/Verify"
		if operation == "chat" || operation == "run" || operation == "inspect" {
			reason = "independent live Orka Agent operation; no bundle or deployment receipt"
		}
		if !supported {
			reason = (&agentruntime.UnsupportedVerbError{Runtime: id, Verb: operation}).Error()
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
			// JSON retains even historical descriptive values. Only these owned
			// human descriptions change; observed/external text remains opaque.
			scope := q.Scope
			switch scope {
			case "native Orka commands and immutable suite lift":
				scope = "native Orka commands and immutable suite deploy"
			case "exact Go-only native suite create/eval and immutable digest lift":
				scope = "exact Go-only native suite create/eval and immutable digest deploy"
			}
			fmt.Fprintf(&buffer, "  qualified %s: %s (%s)\n", q.Version, scope, q.Evidence)
		}
		for _, c := range row.Capabilities {
			state := "unsupported"
			if c.Supported {
				state = "supported"
			}
			operation, reason := c.Operation, c.Reason
			if operation == "lift" {
				operation = "deploy"
			}
			if reason == "native authored workspace and immutable suite workflow; lift requires explicit evidence and reviewed Apply/Verify" {
				reason = "native authored workspace and immutable suite workflow; deploy requires explicit evidence and reviewed Apply/Verify"
			}
			fmt.Fprintf(&buffer, "  %s: %s — %s\n", operation, state, reason)
		}
	}
	fmt.Fprintln(&buffer, "\nStatic support is not installed-target qualification. This view does not select, install or deploy a runtime. Matrix: docs/runtime-adapters.md")
	_, err := io.Copy(out, &buffer)
	return err
}

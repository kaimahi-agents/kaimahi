// `kmx agent evaluate` — runs an Orka portable bundle's evaluation cases
// against the revision deployed at one destination and records the outcome
// against that revision's portable digest.
//
// Cases live beside the bundle in eval/*.yaml and are not part of the
// portable digest: they test a revision, they do not define it. The receipt
// records a digest of the case files that ran instead, so a later reader knows
// exactly which cases a result is for.
//
// The destination is resolved the way lift and status resolve it: an explicit
// --to-context, or the remembered target with its kube-system UID check. The
// live Agent must be owned by this bundle and carry exactly the bundle's
// current portable digest before any case runs; otherwise evaluate refuses,
// because a result recorded against one digest must never come from another.
//
// Each case is one Orka Task, created and read the way create --task does it,
// and never retried: a Task can have side effects. The command is a gate — it
// exits non-zero unless every case passed.
package app

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/cliui"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/config"
	agentruntime "github.com/kaimahi-agents/kaimahi/internal/kmx/runtime"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/scaffold"
)

// EvaluateAgentBundleOptions selects a bundle, an optional destination and
// an optional single case.
type EvaluateAgentBundleOptions struct {
	BundleDir, ToContext, Case string
	// CaseTimeout bounds each case from Task creation to a readable terminal
	// result. The result session's token lasts ten minutes, so it is capped
	// below that.
	CaseTimeout time.Duration
	ResultPort  string
}

const (
	defaultEvaluationCaseTimeout = 5 * time.Minute
	maxEvaluationCaseTimeout     = 9 * time.Minute
)

// bundleEvaluationReceipt is what evaluate writes to
// receipts/eval-<target>.json. It never holds answer text: only its SHA-256
// is recorded. Receipts remain local and are not intended to be committed.
type bundleEvaluationReceipt struct {
	Bundle         string                   `json:"bundle"`
	PortableDigest string                   `json:"portableDigest"`
	CasesDigest    string                   `json:"casesDigest"`
	FullCaseSet    bool                     `json:"fullCaseSet"`
	GitCommit      string                   `json:"gitCommit"`
	Target         bundleEvaluationTarget   `json:"target"`
	Result         string                   `json:"result"`
	Cases          []bundleEvaluationResult `json:"cases"`
}

type bundleEvaluationTarget struct {
	Runtime    agentruntime.ID `json:"runtime"`
	Context    string          `json:"context"`
	Namespace  string          `json:"namespace"`
	ClusterUID string          `json:"clusterUID"`
	Agent      string          `json:"agent"`
	AgentUID   string          `json:"agentUID"`
}

type bundleEvaluationResult struct {
	ID           string   `json:"id"`
	Verdict      string   `json:"verdict"`
	Matched      []string `json:"matched"`
	Missing      []string `json:"missing"`
	TaskName     string   `json:"taskName,omitempty"`
	TaskUID      string   `json:"taskUID,omitempty"`
	AnswerSHA256 string   `json:"answerSHA256,omitempty"`
	Detail       string   `json:"detail,omitempty"`
}

// bundleEvaluationCase is one decoded case and the file it came from.
type bundleEvaluationCase struct {
	File string
	Case agentruntime.EvaluationCase
}

// EvaluateAgentBundle runs the bundle's cases against its deployed revision,
// prints every answer, writes a receipt and fails unless every case passed.
func (a *App) EvaluateAgentBundle(opt EvaluateAgentBundleOptions) error {
	if a.Cfg == nil || a.Run == nil {
		return fmt.Errorf("evaluate requires configured kubectl and streams")
	}
	timeout := opt.CaseTimeout
	if timeout == 0 {
		timeout = defaultEvaluationCaseTimeout
	}
	if timeout < 10*time.Second || timeout > maxEvaluationCaseTimeout {
		return fmt.Errorf("--case-timeout must be between 10s and %s", maxEvaluationCaseTimeout)
	}
	if opt.ResultPort != "" {
		port, err := strconv.ParseUint(opt.ResultPort, 10, 16)
		if err != nil || port == 0 {
			return fmt.Errorf("--result-port must be a TCP port number")
		}
		opt.ResultPort = strconv.FormatUint(port, 10)
	}
	ctx, stop := signal.NotifyContext(a.operationContext(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	bundle, err := resolveOrkaPath(opt.BundleDir)
	if err != nil {
		return fmt.Errorf("resolve bundle: %w", err)
	}
	name, _, portableDigest, err := readBundlePortableAgent(bundle)
	if err != nil {
		return err
	}
	if err := checkLiftReceiptsDir(bundle); err != nil {
		return err
	}
	cases, files, err := loadBundleEvaluationCases(bundle)
	if err != nil {
		return err
	}
	if opt.Case != "" {
		for i, c := range cases {
			if c.Case.ID == opt.Case {
				cases, files = cases[i:i+1], files[i:i+1]
				break
			}
		}
		if len(cases) != 1 || cases[0].Case.ID != opt.Case {
			return fmt.Errorf("no evaluation case with id %q in %s", opt.Case, agentruntime.EvaluationCaseDir)
		}
	}
	casesDigest := agentruntime.EvaluationCasesDigest(files)

	selectionPath, err := bundleLiftSelectionPath(bundle)
	if err != nil {
		return err
	}
	remembered, err := loadBundleLiftSelection(selectionPath)
	if err != nil {
		return err
	}
	contextName := opt.ToContext
	if contextName == "" {
		contextName = remembered.Context
	}
	if contextName == "" {
		return fmt.Errorf("evaluate requires --to-context (no target remembered for this bundle)")
	}
	namespace := OrkaNamespace
	if remembered.Context == contextName && remembered.Namespace != "" {
		namespace = remembered.Namespace
	}
	worker := *a
	cfg := *a.Cfg
	cfg.KubeContext, cfg.ContextSource = contextName, config.SourceFlag
	worker.Cfg = &cfg
	worker.guarded = false
	if err := worker.preflight(depKubectl); err != nil {
		return err
	}
	uid, err := worker.liftClusterUID(ctx)
	if err != nil {
		return err
	}
	if remembered.Context == contextName && remembered.ClusterUID != uid {
		return fmt.Errorf("stale remembered target: context %s now identifies another cluster; remove local selection %s before retrying with --to-context", contextName, selectionPath)
	}
	ref, err := worker.bundleEvaluationRef(ctx, name, namespace, portableDigest)
	if err != nil {
		return err
	}
	// Every case executes a Task, which can have side effects, so evaluate
	// passes the same context guard as any other Orka mutation.
	action := fmt.Sprintf("execute %d evaluation Task(s) against Agent %s in %s", len(cases), name, namespace)
	if err := worker.guardOrkaMutation(ctx, CreateOptions{Name: name, Namespace: namespace}, action); err != nil {
		return err
	}
	worker.notef("Evaluate destination: context %s, namespace %s, Agent %s (UID %s)", contextName, namespace, name, ref.UID)
	worker.notef("Each case creates one Task (never retried) and reads its result as %s, which uses that ServiceAccount's full effective authority.", orkaResultAccount)

	adapter := orkaRuntimeAdapter{app: &worker, resultPort: opt.ResultPort}
	receipt := bundleEvaluationReceipt{
		Bundle: name, PortableDigest: portableDigest, CasesDigest: casesDigest, FullCaseSet: opt.Case == "",
		GitCommit: liftAgentCommit(ctx, bundle, portableDigest),
		Target:    bundleEvaluationTarget{Runtime: agentruntime.Orka, Context: contextName, Namespace: namespace, ClusterUID: uid, Agent: name, AgentUID: ref.UID},
	}
	ui := cliui.New(a.Out)
	for _, c := range cases {
		fmt.Fprintf(a.Out, "\n%s\n", ui.Heading("case "+c.Case.ID))
		caseCtx, cancel := context.WithTimeout(ctx, timeout)
		result, err := adapter.Evaluate(caseCtx, ref, agentruntime.EvaluationRequest{
			CaseID: c.Case.ID, Input: c.Case.Input, ExpectContains: c.Case.ExpectContains, PortableDigest: portableDigest,
		})
		cancel()
		if err != nil {
			// Refused before any Task was created: nothing ran, so nothing
			// passed or failed.
			result = agentruntime.EvaluationReceipt{CaseID: c.Case.ID, Verdict: agentruntime.EvaluationUnknown, Detail: "not run: " + err.Error()}
		}
		receipt.Cases = append(receipt.Cases, bundleEvaluationResultFrom(result))
		fmt.Fprintln(a.Out, bundleEvaluationVerdictLine(result))
	}
	receipt.Result = bundleEvaluationOverall(receipt.Cases)
	if err := writeBundleEvaluationReceipt(bundle, uid, receipt); err != nil {
		return fmt.Errorf("evaluation finished but its receipt was not saved: %w", err)
	}
	counts := map[string]int{}
	for _, c := range receipt.Cases {
		counts[c.Verdict]++
	}
	summary := fmt.Sprintf("%d passed, %d failed, %d unknown", counts["pass"], counts["fail"], counts["unknown"])
	fmt.Fprintf(a.Out, "\nevaluation %s: %s (portable digest %s)\n", receipt.Result, summary, shortSHA(portableDigest))
	if receipt.Result != string(agentruntime.EvaluationPass) {
		return fmt.Errorf("evaluation did not pass: %s", summary)
	}
	return nil
}

// bundleEvaluationRef reads the live Agent and refuses anything but this
// bundle's own Agent at exactly the given portable digest.
func (a *App) bundleEvaluationRef(ctx context.Context, name, namespace, portableDigest string) (agentruntime.AgentRef, error) {
	raw, err := a.orkaCapture(ctx, nil, "-n", namespace, "get", orkaPlural("Agent"), name, "--ignore-not-found=true", "-o", "json")
	if err != nil {
		return agentruntime.AgentRef{}, fmt.Errorf("cannot read Agent %s/%s at the destination: %w", namespace, name, err)
	}
	live := describeOrkaResource(raw)
	switch {
	case !live.Found:
		return agentruntime.AgentRef{}, fmt.Errorf("Agent %s/%s is not deployed at this destination; lift first", namespace, name)
	case !live.Marked:
		return agentruntime.AgentRef{}, fmt.Errorf("Agent %s/%s has no bundle ownership marker; lift first", namespace, name)
	case live.MarkerOwner != name:
		return agentruntime.AgentRef{}, fmt.Errorf("Agent %s/%s belongs to another bundle", namespace, name)
	case live.UID == "":
		return agentruntime.AgentRef{}, fmt.Errorf("Agent %s/%s reports no UID", namespace, name)
	case live.PortableDigest != portableDigest:
		return agentruntime.AgentRef{}, fmt.Errorf("deployed revision differs; lift first (deployed %s, bundle %s)", shortSHA(orDash(live.PortableDigest)), shortSHA(portableDigest))
	}
	ref := agentruntime.AgentRef{Runtime: agentruntime.Orka, Context: a.Cfg.KubeContext, Namespace: namespace, Kind: orkaPlural("Agent"), Name: name, UID: live.UID}
	if err := a.orkaEvaluationRevisionError(ctx, ref, portableDigest); err != nil {
		return agentruntime.AgentRef{}, err
	}
	return ref, nil
}

// readBundlePortableAgent reads and strictly parses an Orka bundle's
// agent.yaml, returning its name, exact bytes and portable digest. Bundle
// commands using this helper have not implemented Kagent lifecycle operations.
func readBundlePortableAgent(bundle string) (string, []byte, string, error) {
	if err := scaffold.RefuseKeyShapes(bundle); err != nil {
		return "", nil, "", fmt.Errorf("refusing credential-shaped bundle path")
	}
	info, err := os.Stat(bundle)
	if err != nil {
		return "", nil, "", fmt.Errorf("read bundle directory: %w", err)
	}
	if !info.IsDir() {
		return "", nil, "", fmt.Errorf("bundle must be a directory")
	}
	agentFile := filepath.Join(bundle, "agent.yaml")
	info, err = os.Lstat(agentFile)
	if err != nil {
		return "", nil, "", fmt.Errorf("read portable agent: %w", err)
	}
	if !info.Mode().IsRegular() {
		return "", nil, "", fmt.Errorf("agent.yaml must be a regular file")
	}
	source, err := os.ReadFile(agentFile)
	if err != nil {
		return "", nil, "", fmt.Errorf("read portable agent: %w", err)
	}
	portable, err := agentruntime.ParsePortableAgent(source)
	if err != nil {
		return "", nil, "", fmt.Errorf("invalid portable agent: %w", err)
	}
	// ParsePortableAgent requires exactly one extension, so a Kagent arm
	// unambiguously identifies a bundle these Orka-only callers cannot handle.
	if portable.Extensions.Kagent != nil {
		return "", nil, "", fmt.Errorf("runtime %s bundle is not supported by this Orka-only command", agentruntime.Kagent)
	}
	name := strings.TrimSpace(portable.Metadata.Name)
	if name == "" {
		return "", nil, "", fmt.Errorf("invalid portable agent: metadata.name is required")
	}
	return name, source, agentruntime.PortableBundleDigest(source), nil
}

// loadBundleEvaluationCases reads every eval/*.yaml case, in file-name order.
// The directory must be a real directory, every case file a regular file, and
// every id unique; any case that fails strict decoding fails the whole load,
// naming its file.
func loadBundleEvaluationCases(bundle string) ([]bundleEvaluationCase, []agentruntime.EvaluationCaseFile, error) {
	dir := filepath.Join(bundle, agentruntime.EvaluationCaseDir)
	info, err := os.Lstat(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil, fmt.Errorf("bundle has no %s/ directory; add at least one case (%s/<id>.yaml with id, input and expectContains)", agentruntime.EvaluationCaseDir, agentruntime.EvaluationCaseDir)
	}
	if err != nil {
		return nil, nil, fmt.Errorf("inspect %s directory: %w", agentruntime.EvaluationCaseDir, err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, nil, fmt.Errorf("bundle %s must be a directory, not a link", agentruntime.EvaluationCaseDir)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, nil, fmt.Errorf("read %s directory: %w", agentruntime.EvaluationCaseDir, err)
	}
	var cases []bundleEvaluationCase
	var files []agentruntime.EvaluationCaseFile
	seen := map[string]string{}
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".yaml") {
			continue
		}
		if err := scaffold.RefuseKeyShapes(entry.Name()); err != nil {
			return nil, nil, fmt.Errorf("refusing credential-shaped evaluation case file name")
		}
		info, err := entry.Info()
		if err != nil {
			return nil, nil, fmt.Errorf("inspect %s/%s: %w", agentruntime.EvaluationCaseDir, entry.Name(), err)
		}
		if !info.Mode().IsRegular() {
			return nil, nil, fmt.Errorf("%s/%s must be a regular file", agentruntime.EvaluationCaseDir, entry.Name())
		}
		raw, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			return nil, nil, fmt.Errorf("read %s/%s: %w", agentruntime.EvaluationCaseDir, entry.Name(), err)
		}
		c, err := agentruntime.ParseEvaluationCase(raw)
		if err != nil {
			return nil, nil, fmt.Errorf("%s/%s: %w", agentruntime.EvaluationCaseDir, entry.Name(), err)
		}
		if previous, dup := seen[c.ID]; dup {
			return nil, nil, fmt.Errorf("%s/%s repeats case id %q from %s", agentruntime.EvaluationCaseDir, entry.Name(), c.ID, previous)
		}
		seen[c.ID] = entry.Name()
		cases = append(cases, bundleEvaluationCase{File: entry.Name(), Case: c})
		files = append(files, agentruntime.EvaluationCaseFile{Name: entry.Name(), Bytes: raw})
	}
	if len(cases) == 0 {
		return nil, nil, fmt.Errorf("bundle %s/ has no *.yaml cases", agentruntime.EvaluationCaseDir)
	}
	return cases, files, nil
}

func bundleEvaluationResultFrom(result agentruntime.EvaluationReceipt) bundleEvaluationResult {
	return bundleEvaluationResult{
		ID: result.CaseID, Verdict: string(result.Verdict),
		Matched: nonNilStrings(result.Matched), Missing: nonNilStrings(result.Missing),
		TaskName: result.TaskName, TaskUID: result.TaskUID, AnswerSHA256: result.AnswerSHA256, Detail: result.Detail,
	}
}

func nonNilStrings(values []string) []string {
	if values == nil {
		return []string{}
	}
	return values
}

func bundleEvaluationVerdictLine(result agentruntime.EvaluationReceipt) string {
	line := fmt.Sprintf("%s: %s", result.CaseID, result.Verdict)
	if len(result.Missing) > 0 {
		quoted := make([]string, len(result.Missing))
		for i, missing := range result.Missing {
			quoted[i] = fmt.Sprintf("%q", missing)
		}
		line += " — missing " + strings.Join(quoted, ", ")
	} else if result.Detail != "" {
		line += " — " + result.Detail
	}
	if result.TaskName != "" {
		line += fmt.Sprintf(" (Task %s", result.TaskName)
		if result.TaskUID != "" {
			line += " UID " + result.TaskUID
		}
		line += ")"
	}
	return line
}

// bundleEvaluationOverall is pass only when every case passed, fail when any
// case failed, and otherwise unknown.
func bundleEvaluationOverall(cases []bundleEvaluationResult) string {
	overall := string(agentruntime.EvaluationPass)
	for _, c := range cases {
		switch c.Verdict {
		case string(agentruntime.EvaluationFail):
			return string(agentruntime.EvaluationFail)
		case string(agentruntime.EvaluationPass):
		default:
			overall = string(agentruntime.EvaluationUnknown)
		}
	}
	return overall
}

// bundleEvaluationReceiptPath is receipts/eval-<key>.json, where key is the
// same sha256(context, namespace, cluster UID) a lift receipt is named by, so
// an evaluation of a replaced cluster never overwrites the former one's.
func bundleEvaluationReceiptPath(bundle, context, namespace, clusterUID string) string {
	key := sha256.Sum256([]byte(context + "\x00" + namespace + "\x00" + clusterUID))
	return filepath.Join(bundle, "receipts", fmt.Sprintf("eval-%x.json", key))
}

func writeBundleEvaluationReceipt(bundle, clusterUID string, receipt bundleEvaluationReceipt) error {
	dir := filepath.Join(bundle, "receipts")
	if err := os.Mkdir(dir, 0700); err != nil && !os.IsExist(err) {
		return err
	}
	if err := checkLiftReceiptsDir(bundle); err != nil {
		return err
	}
	if receipt.Target.ClusterUID != clusterUID {
		return fmt.Errorf("evaluation receipt target cluster UID does not match its file identity")
	}
	raw, err := json.MarshalIndent(receipt, "", "  ")
	if err != nil {
		return err
	}
	path := bundleEvaluationReceiptPath(bundle, receipt.Target.Context, receipt.Target.Namespace, clusterUID)
	return writePrivateAgentFile(path, append(raw, '\n'))
}

// bundleEvaluationStatus is what `kmx agent status` reports about one
// target's evaluation: the recorded result when a receipt exists for exactly
// this cluster, this live Agent, the bundle's current digest and the current
// case set; otherwise none.
func bundleEvaluationStatus(bundle string, target bundleTargetStatus, portableDigest, casesDigest string) string {
	const none = "none"
	if casesDigest == "" || target.ObservedClusterUID == "" || target.LiveDigest != portableDigest || target.Agent.UID == "" {
		return none
	}
	evidence, err := readBundleGateEvidence(bundle)
	if err != nil {
		return none
	}
	result := ""
	for _, item := range evidence {
		receipt := item.Receipt
		if receipt.Target.ClusterUID != target.ObservedClusterUID || receipt.Target.Namespace != target.Namespace || receipt.Target.AgentUID != target.Agent.UID ||
			!receipt.FullCaseSet || receipt.PortableDigest != portableDigest || receipt.CasesDigest != casesDigest {
			continue
		}
		if receipt.Result == string(agentruntime.EvaluationPass) {
			cases, _, err := loadBundleEvaluationCases(bundle)
			if err != nil || !bundleCaseResultsPass(receipt.Cases, cases) {
				return none
			}
		} else if receipt.Result != string(agentruntime.EvaluationFail) && receipt.Result != string(agentruntime.EvaluationUnknown) {
			continue
		}
		if result != "" && result != receipt.Result {
			return none
		}
		result = receipt.Result
	}
	if result != "" {
		return result
	}
	return none
}

// currentBundleCasesDigest is the digest of the bundle's full current case
// set, or empty when the cases cannot be read — status reports none rather
// than failing.
func currentBundleCasesDigest(bundle string) string {
	_, files, err := loadBundleEvaluationCases(bundle)
	if err != nil {
		return ""
	}
	return agentruntime.EvaluationCasesDigest(files)
}

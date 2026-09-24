package app

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"time"

	yaml "go.yaml.in/yaml/v3"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/cliui"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/config"
	agentruntime "github.com/kaimahi-agents/kaimahi/internal/kmx/runtime"
)

// statusRequestTimeout bounds every read status makes.
//
// Re-landed from #37, whose reasoning holds and had nowhere to live once
// scripts/status.py went: this is the command people run when something is
// wrong, and an API server that is unreachable rather than refusing leaves a
// bare `kubectl get` waiting on TCP. A status command that hangs is the same
// failure as one that needs the thing being diagnosed.
const statusRequestTimeout = "--request-timeout=15s"

// StatusOptions controls the human table or the structured document, and
// which runtime is reported (DESIGN.md §4).
type StatusOptions struct {
	Output string
	// Runtime is the explicit runtime ID. Empty uses shared platform
	// detection, which considers only Orka and kagent-v1: a legacy-only
	// cluster therefore receives the named install error rather than
	// silently selecting legacy kagent.
	Runtime string
	// Namespace and Agent are the explicit selectors that populate the
	// singular runtime-qualified AgentRef lifecycle Status accepts. Legacy
	// kagent takes neither: it reports its own fixed namespace.
	Namespace string
	Agent     string
}

type objectList[T any] struct {
	Items []T `json:"items"`
}

type statusCondition struct {
	Type, Status string
	// LastTransitionTime is when kagent reached this verdict. Printed
	// because the column is a CACHED reconcile result and not a live check:
	// a credential written since is one this answer says nothing about, and
	// an operator reading a bare "yes" cannot tell the two apart
	// (seamverdict.go).
	LastTransitionTime string `json:"lastTransitionTime"`
}

type agentStatus struct {
	Metadata struct{ Name string } `json:"metadata"`
	Spec     struct {
		Declarative struct {
			ModelConfig string `json:"modelConfig"`
			Tools       []struct {
				MCPServer struct{ Name string } `json:"mcpServer"`
			} `json:"tools"`
		} `json:"declarative"`
	} `json:"spec"`
	Status struct{ Conditions []statusCondition } `json:"status"`
}

type modelStatus struct {
	Metadata struct{ Name string } `json:"metadata"`
	Spec     struct {
		Provider, Model string
		// The governed presets differ from the direct ones in exactly one
		// place: baseUrl is the plane's proxy. That is what makes the model
		// seam classifiable from the cluster alone.
		OpenAI struct {
			BaseURL string `json:"baseUrl"`
		} `json:"openAI"`
		APIKeySecret string `json:"apiKeySecret"`
	} `json:"spec"`
	Status struct{ Conditions []statusCondition } `json:"status"`
}

// planeDeployment is the proxy workload: its existence is what "installed"
// means, and its replicas are what "ready" means.
type planeDeployment struct {
	Metadata struct{ Name string } `json:"metadata"`
	Spec     struct {
		Replicas int `json:"replicas"`
	} `json:"spec"`
	Status struct {
		ReadyReplicas int `json:"readyReplicas"`
	} `json:"status"`
}

type podStatus struct {
	Metadata struct{ Name string } `json:"metadata"`
	Status   struct {
		Phase             string
		Conditions        []statusCondition
		ContainerStatuses []struct {
			RestartCount int `json:"restartCount"`
		} `json:"containerStatuses"`
	} `json:"status"`
}

// condition renders one condition as yes / no / unknown.
//
// `unknown` rather than `-`: a condition nothing has recorded is not a no,
// it is nothing, which is the same distinction the governance counts draw.
func condition(conditions []statusCondition, name string) string {
	for _, value := range conditions {
		if value.Type == name {
			switch value.Status {
			case "True":
				return "yes"
			case "False":
				return "no"
			}
			return "unknown"
		}
	}
	return "unknown"
}

// conditionAged is condition() plus when the verdict was reached, for the
// kagent CRD conditions that are CACHED reconcile results rather than live
// checks.
//
// The age is not decoration. kagent records these when it last tried and
// does not retry on its own, so "yes" alone reads as a live check and is not
// one — that is what let a credential written minutes ago show as working
// when it could not be used at all. A pod's Ready condition is genuinely
// live and gets plain condition(); these do not.
func age(now, then time.Time) string {
	if then.IsZero() {
		return "an unknown time"
	}
	d := now.Sub(then).Round(time.Second)
	if d < 0 {
		d = 0
	}
	return d.String()
}

func conditionAged(conditions []statusCondition, name string, now time.Time) string {
	answer := condition(conditions, name)
	for _, value := range conditions {
		if value.Type != name {
			continue
		}
		at, err := time.Parse(time.RFC3339, strings.TrimSpace(value.LastTransitionTime))
		if err != nil {
			return answer
		}
		return answer + " (" + age(now, at) + " ago)"
	}
	return answer
}

func table(out io.Writer, headers []string, rows [][]string) {
	widths := make([]int, len(headers))
	for i, header := range headers {
		widths[i] = len(header)
	}
	for _, row := range rows {
		for i, value := range row {
			if len(value) > widths[i] {
				widths[i] = len(value)
			}
		}
	}
	for rowIndex, row := range append([][]string{headers}, rows...) {
		fmt.Fprint(out, "  ")
		for i, value := range row {
			if i > 0 {
				fmt.Fprint(out, "  ")
			}
			fmt.Fprintf(out, "%-*s", widths[i], value)
		}
		if rowIndex < len(rows) {
			fmt.Fprintln(out)
		}
	}
	fmt.Fprintln(out)
}

func humanTable(out io.Writer, headers []string, rows [][]string) {
	ui := cliui.New(out)
	if !ui.Rich() {
		table(out, headers, rows)
		return
	}
	fmt.Fprintln(out, ui.Table(headers, rows))
}

// statusTolerant reads a population that may not exist on this cluster at
// all: a CRD kagent has not installed, a namespace that was never created,
// an RBAC denial. It returns a REASON rather than an error, and never turns
// a failure into an empty list — the caller reports `unknown`, which is a
// different answer from "0" and the word the audit trail already uses for it.
//
// The reason is the first line of kubectl's own complaint, so an operator
// reads what kubectl said rather than a paraphrase of it.
func (a *App) statusTolerant(namespace, resource string, target any) string {
	raw, err := a.kubectlCapture("-n", namespace, "get", resource, "-o", "json", statusRequestTimeout)
	if err == nil {
		if err := json.Unmarshal([]byte(raw), target); err != nil {
			return firstLine(err.Error())
		}
		return ""
	}
	if isNotFound(err) {
		// The namespace or the resource is genuinely absent, which for
		// these reads means an empty population rather than a mystery.
		return ""
	}
	return firstLine(err.Error())
}

func firstLine(message string) string {
	message = strings.TrimSpace(message)
	if i := strings.IndexByte(message, '\n'); i >= 0 {
		message = strings.TrimSpace(message[:i])
	}
	return message
}

// secretNames lists the Secret NAMES in a namespace. Names only: `-o name`
// returns metadata, never a value, and status reads no Secret value
// anywhere. This exists so a governed seam whose token Secret is missing is
// reported as missing instead of failing at the next call.
func (a *App) secretNames(namespace string) ([]string, string) {
	// EVERY failure is a reason, NotFound included. A namespace with no
	// Secrets succeeds and prints nothing; a NotFound here means the
	// listing did not happen, and an empty list would become a confident
	// accusation naming Secrets that may well exist.
	raw, err := a.kubectlCapture("-n", namespace, "get", "secrets", "-o", "name", statusRequestTimeout)
	if err != nil {
		return nil, firstLine(err.Error())
	}
	var names []string
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		names = append(names, strings.TrimPrefix(line, "secret/"))
	}
	return names, ""
}

func podSummary(pods []podStatus) (ready, restarts int, rows [][]string) {
	sort.Slice(pods, func(i, j int) bool { return pods[i].Metadata.Name < pods[j].Metadata.Name })
	for _, pod := range pods {
		isReady := condition(pod.Status.Conditions, "Ready") == "yes"
		if isReady {
			ready++
		}
		podRestarts := 0
		for _, container := range pod.Status.ContainerStatuses {
			podRestarts += container.RestartCount
		}
		restarts += podRestarts
		rows = append(rows, []string{pod.Metadata.Name, condition(pod.Status.Conditions, "Ready"), pod.Status.Phase, fmt.Sprint(podRestarts)})
	}
	return
}

func statusReady(allAgents, allModels bool, kReady, kTotal, oReady, oTotal, pReady, pTotal int) bool {
	ready := allAgents && allModels && kTotal > 0 && kReady == kTotal
	if oTotal > 0 {
		ready = ready && oReady == oTotal
	}
	if pTotal > 0 {
		ready = ready && pReady == pTotal
	}
	return ready
}

// lifecycleRuntimeRegistry holds the LifecycleAdapters an explicit or
// detected --runtime status dispatch looks up by ID (DESIGN.md §4). Orka
// reports its own workload state and legacy kagent its retained combined
// runtime slice; kagent-v1 joins this registry in a later task, so until
// then it resolves to the registry's own typed UnknownRuntimeError rather
// than to another runtime's implementation.
//
// snapshot is the legacy adapter's sink (runtime_kagent.go): the status path
// passes one so the app-owned aggregate sections report the same combined
// read the runtime slice came from. Every other caller passes nil.
func (a *App) lifecycleRuntimeRegistry(snapshot *kagentStatusSnapshot) (*agentruntime.Registry, error) {
	return agentruntime.NewRegistry(orkaRuntimeAdapter{app: a}, kagentRuntimeAdapter{app: a, snapshot: snapshot})
}

// statusRuntimeAdapter resolves which runtime this status reports and proves
// it declares Status. An unknown runtime and an unsupported verb are both
// typed errors from the shared registry's own model; neither ever falls back
// to a different runtime.
func (a *App) statusRuntimeAdapter(ctx context.Context, opt StatusOptions, snapshot *kagentStatusSnapshot) (agentruntime.ID, agentruntime.LifecycleAdapter, bool, error) {
	registry, err := a.lifecycleRuntimeRegistry(snapshot)
	if err != nil {
		return "", nil, false, err
	}
	id, detected := agentruntime.ID(strings.TrimSpace(opt.Runtime)), false
	if id == "" {
		if id, err = a.detectPlatformRuntime(ctx); err != nil {
			return "", nil, false, err
		}
		detected = true
	}
	adapter, err := registry.Lookup(id)
	if err != nil {
		return id, nil, detected, err
	}
	lifecycle, ok := adapter.(agentruntime.LifecycleAdapter)
	if !ok || !lifecycle.Capabilities().Status {
		return id, nil, detected, &agentruntime.UnsupportedVerbError{Runtime: id, Verb: agentruntime.VerbStatus}
	}
	return id, lifecycle, detected, nil
}

// statusSelectors proves this runtime has the selectors its lifecycle Status
// needs, and returns the singular AgentRef they populate.
//
// DESIGN.md §4 requires this to happen BEFORE anything is collected or
// printed: kmx reports the selected platform and the missing flags, emits no
// partial table or JSON, and makes no claim that the ancillary sections were
// checked. Orka watches namespaces explicitly and names its Agents
// explicitly, so both selectors are required and neither is guessed.
func (a *App) statusSelectors(id agentruntime.ID, detected bool, opt StatusOptions) (agentruntime.AgentRef, error) {
	namespace, agent := strings.TrimSpace(opt.Namespace), strings.TrimSpace(opt.Agent)
	if id == agentruntime.Kagent {
		// The legacy runtime reports its own fixed namespace and every agent
		// in it. A selector here is a conflict, not a filter.
		var supplied []string
		if namespace != "" && namespace != config_kagentNamespace {
			supplied = append(supplied, "--namespace")
		}
		if agent != "" {
			supplied = append(supplied, "--agent")
		}
		if len(supplied) > 0 {
			return agentruntime.AgentRef{}, fmt.Errorf("runtime %s reports its own fixed namespace %s and every agent in it; %s selects something it cannot report",
				id, config_kagentNamespace, strings.Join(supplied, " and "))
		}
		return agentruntime.AgentRef{Runtime: id, Context: a.Cfg.KubeContext, Namespace: config_kagentNamespace}, nil
	}
	var missing []string
	if namespace == "" {
		missing = append(missing, "--namespace")
	}
	if agent == "" {
		missing = append(missing, "--agent")
	}
	if len(missing) > 0 {
		return agentruntime.AgentRef{}, fmt.Errorf("kmx status selected runtime %s%s, which requires %s.\n"+
			"  Nothing was collected or printed, so this says nothing about the governance, Ollama, MCP or certificate sections",
			id, statusSelectionSource(detected), strings.Join(missing, " and "))
	}
	return agentruntime.AgentRef{Runtime: id, Context: a.Cfg.KubeContext, Namespace: namespace, Name: agent}, nil
}

func statusSelectionSource(detected bool) string {
	if detected {
		return " by platform detection"
	}
	return ""
}

// Status prints a grouped human view or kubectl-native JSON/YAML.
func (a *App) StatusWithOptions(opt StatusOptions) error {
	format := strings.ToLower(strings.TrimSpace(opt.Output))
	if format != "" && format != "table" && format != "json" && format != "yaml" {
		return fmt.Errorf("status output %q is not supported — use table, json, or yaml", opt.Output)
	}
	// An explicit runtime's selectors are decided from the flags alone, so a
	// conflicting or incomplete selection is refused before kmx provisions a
	// tool or contacts a cluster. A detected runtime's selectors can only be
	// checked once detection has chosen one, which is why that half runs
	// below — still before any status is collected or printed.
	if explicit := agentruntime.ID(strings.TrimSpace(opt.Runtime)); explicit != "" {
		registry, err := a.lifecycleRuntimeRegistry(nil)
		if err != nil {
			return err
		}
		if _, err := registry.Lookup(explicit); err != nil {
			return err
		}
		if _, err := a.statusSelectors(explicit, false, opt); err != nil {
			return err
		}
	}
	if err := a.preflight(depKubectl); err != nil {
		return err
	}
	if err := a.requireExistingContext(); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(a.operationContext(), 2*time.Minute)
	defer cancel()
	snapshot := &kagentStatusSnapshot{}
	id, lifecycle, detected, err := a.statusRuntimeAdapter(ctx, opt, snapshot)
	if err != nil {
		return err
	}
	ref, err := a.statusSelectors(id, detected, opt)
	if err != nil {
		return err
	}
	status, err := lifecycle.Status(ctx, ref, agentruntime.StatusOptions{})
	if err != nil {
		return err
	}
	if id == agentruntime.Kagent {
		// The legacy runtime keeps its combined view: the slice just read
		// through the adapter, plus the unchanged app-owned aggregate
		// sections and the verbatim `items` automation shape.
		return a.legacyStatus(format, snapshot, status)
	}
	return a.runtimeStatus(format, ref, status)
}

// legacyStatus is the unchanged combined view: the same aggregation, the
// same two renderers, over the one snapshot the LifecycleAdapter read.
func (a *App) legacyStatus(format string, snapshot *kagentStatusSnapshot, slice agentruntime.LifecycleStatus) error {
	data, err := a.collectStatus(snapshot, slice)
	if err != nil {
		return err
	}
	if format == "" || format == "table" {
		return a.statusTable(data)
	}
	return a.statusStructured(format, data)
}

// statusDocument is what `kmx status -o json|yaml` publishes.
//
// CHANGED, deliberately: this output used to be kubectl's own List, and the
// governance count has to survive automation or it is only a message on a
// screen — the same reasoning as #71, where the machine-readable half was
// the load-bearing one. The kubectl objects are still here, VERBATIM, under
// `items`, so the idiom that reads them (`jq '.items[]'`) is unchanged; what
// is new is the envelope around them.
type statusDocument struct {
	Context       string            `json:"context"`
	ContextSource string            `json:"contextSource"`
	Governance    governance        `json:"governance"`
	Items         []json.RawMessage `json:"items"`
}

func (a *App) statusStructured(format string, data *statusData) error {
	document := statusDocument{
		Context:       a.Cfg.KubeContext,
		ContextSource: a.Cfg.ContextSource,
		Governance:    data.governanceOf(),
		Items:         data.items,
	}
	if format == "yaml" {
		// json.Marshal first so the struct tags decide the field names
		// once: one shape, two encodings. UseNumber keeps integers exact
		// through the generic form — a round trip through float64 would
		// silently round a large counter in some CRD's status.
		intermediate, err := json.Marshal(document)
		if err != nil {
			return err
		}
		decoder := json.NewDecoder(bytes.NewReader(intermediate))
		decoder.UseNumber()
		var generic any
		if err := decoder.Decode(&generic); err != nil {
			return err
		}
		encoded, err := yaml.Marshal(exactNumbers(generic))
		if err != nil {
			return err
		}
		_, err = a.Out.Write(encoded)
		return err
	}
	encoded, err := json.MarshalIndent(document, "", "  ")
	if err != nil {
		return err
	}
	_, err = a.Out.Write(append(encoded, '\n'))
	return err
}

// exactNumbers turns json.Number back into a Go integer or float so YAML
// emits `3`, not `"3"`. Integers that do not fit an int64 keep their exact
// decimal text rather than being rounded into one.
func exactNumbers(value any) any {
	switch typed := value.(type) {
	case json.Number:
		if i, err := typed.Int64(); err == nil {
			return i
		}
		if f, err := typed.Float64(); err == nil && !strings.ContainsAny(typed.String(), "eE") {
			if json.Number(strconv.FormatFloat(f, 'f', -1, 64)) == typed {
				return f
			}
			return typed.String()
		} else if err == nil {
			return f
		}
		return typed.String()
	case map[string]any:
		for key, item := range typed {
			typed[key] = exactNumbers(item)
		}
		return typed
	case []any:
		for i, item := range typed {
			typed[i] = exactNumbers(item)
		}
		return typed
	}
	return value
}

func (a *App) Status() error { return a.StatusWithOptions(StatusOptions{}) }

// runtimeStatusDocument is what `kmx status -o json|yaml` publishes for a
// runtime reported through its LifecycleAdapter. It is a closed document,
// deliberately separate from the legacy kubectl-native `items` envelope: it
// carries exactly the runtime-qualified identity that was asked for and the
// pair/instance states that adapter returned, and nothing that would imply
// the app-owned aggregate sections were read.
type runtimeStatusDocument struct {
	Context   string                `json:"context"`
	Runtime   string                `json:"runtime"`
	Namespace string                `json:"namespace"`
	Name      string                `json:"name"`
	Pair      runtimeStatusSection  `json:"pair"`
	Instance  *runtimeStatusSection `json:"instance,omitempty"`
}

// runtimeStatusSection keeps pair and instance state side by side and never
// merges them into a single readiness boolean (DESIGN.md §1). Fields stay an
// ordered list rather than a map so an adapter's own order survives.
type runtimeStatusSection struct {
	DesiredRevision          string               `json:"desiredRevision,omitempty"`
	LatestSuccessfulRevision string               `json:"latestSuccessfulRevision,omitempty"`
	State                    string               `json:"state,omitempty"`
	PreparedRevision         string               `json:"preparedRevision,omitempty"`
	Fields                   []runtimeStatusField `json:"fields"`
}

type runtimeStatusField struct {
	Label string `json:"label"`
	Value string `json:"value"`
}

func runtimeStatusFields(fields []agentruntime.Field) []runtimeStatusField {
	out := make([]runtimeStatusField, 0, len(fields))
	for _, field := range fields {
		out = append(out, runtimeStatusField{Label: field.Label, Value: field.Value})
	}
	return out
}

// runtimeStatus reports one runtime's workload lifecycle status and nothing
// else. `kmx status`'s governance, Ollama, MCP and certificate sections are
// app-owned aggregation over the legacy runtime's own namespace; printing
// them here would claim sections this command never read.
func (a *App) runtimeStatus(format string, ref agentruntime.AgentRef, status agentruntime.LifecycleStatus) error {
	if format == "" || format == "table" {
		ui := cliui.New(a.Out)
		fmt.Fprintln(a.Out, ui.Heading(ref.Name))
		fields := []cliui.Field{{Label: "runtime", Value: string(ref.Runtime)}, {Label: "namespace", Value: ref.Namespace}}
		fields = append(fields, runtimeStatusPairFields(status.Pair)...)
		fmt.Fprintln(a.Out, ui.Fields(fields))
		if status.Instance != nil {
			instance := []cliui.Field{{Label: "state", Value: status.Instance.State}}
			if status.Instance.PreparedRevision != "" {
				instance = append(instance, cliui.Field{Label: "prepared revision", Value: status.Instance.PreparedRevision})
			}
			for _, field := range status.Instance.Fields {
				instance = append(instance, cliui.Field{Label: field.Label, Value: field.Value})
			}
			fmt.Fprintf(a.Out, "\n%s\n%s\n", ui.Heading("Instance"), ui.Fields(instance))
		}
		return nil
	}
	document := runtimeStatusDocument{
		Context:   a.Cfg.KubeContext,
		Runtime:   string(ref.Runtime),
		Namespace: ref.Namespace,
		Name:      ref.Name,
		Pair: runtimeStatusSection{
			DesiredRevision:          status.Pair.DesiredRevision,
			LatestSuccessfulRevision: status.Pair.LatestSuccessfulRevision,
			Fields:                   runtimeStatusFields(status.Pair.Fields),
		},
	}
	if status.Instance != nil {
		document.Instance = &runtimeStatusSection{
			State:            status.Instance.State,
			PreparedRevision: status.Instance.PreparedRevision,
			Fields:           runtimeStatusFields(status.Instance.Fields),
		}
	}
	return a.writeStatusDocument(format, document)
}

func runtimeStatusPairFields(pair agentruntime.PairStatus) []cliui.Field {
	var fields []cliui.Field
	if pair.DesiredRevision != "" {
		fields = append(fields, cliui.Field{Label: "desired revision", Value: pair.DesiredRevision})
	}
	if pair.LatestSuccessfulRevision != "" {
		fields = append(fields, cliui.Field{Label: "latest successful revision", Value: pair.LatestSuccessfulRevision})
	}
	for _, field := range pair.Fields {
		fields = append(fields, cliui.Field{Label: field.Label, Value: field.Value})
	}
	return fields
}

// writeStatusDocument encodes one status document in the requested format.
// json.Marshal decides the field names once for both encodings, and
// UseNumber keeps integers exact through the generic YAML form.
func (a *App) writeStatusDocument(format string, document any) error {
	encoded, err := json.MarshalIndent(document, "", "  ")
	if err != nil {
		return err
	}
	if format != "yaml" {
		_, err = a.Out.Write(append(encoded, '\n'))
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.UseNumber()
	var generic any
	if err := decoder.Decode(&generic); err != nil {
		return err
	}
	body, err := yaml.Marshal(exactNumbers(generic))
	if err != nil {
		return err
	}
	_, err = a.Out.Write(body)
	return err
}

// statusData is everything one `kmx status` reads, gathered once so the
// human table and the structured document are the same facts — not two
// reads of a cluster that may have changed between them.
type statusData struct {
	agents     objectList[agentStatus]
	models     objectList[modelStatus]
	servers    objectList[json.RawMessage]
	kagentPods objectList[podStatus]
	ollamaPods objectList[podStatus]
	planePods  objectList[podStatus]
	// items is the combined kagent read exactly as kubectl returned it,
	// carried so `-o json` can publish the objects verbatim without asking
	// the cluster a second time.
	items   []json.RawMessage
	secrets []string
	// runtime is the legacy runtime slice exactly as its LifecycleAdapter
	// reported it (DESIGN.md §3). The ancillary Ollama, governance, MCP and
	// certificate lines beside it stay app-owned and are added here.
	runtime    []agentruntime.Field
	planeThere bool
	// planeDesired and planeReady come from the proxy Deployment, so a
	// proxy scaled to zero beside a running Postgres is not reported ready.
	planeDesired int
	planeReady   int
	// certificate is what the plane serves the model seam with. Read
	// tolerantly like everything else here: an absent one is a plane that
	// has not been deployed, not a reason for status to fail.
	certificate SeamCertificate
	serverErr   string
	planeErr    string
	secretErr   string
	ollamaErr   string
}

// collectStatus assembles one status answer around the legacy runtime's own
// snapshot.
//
// The kagent objects come from ONE combined get — the read the runtime's
// LifecycleAdapter just performed — and are demultiplexed by kind there.
// That is not only three fewer calls: it is what makes `items` and the
// counts beside them a single snapshot, so a consumer cannot find an Agent
// in `items` that the count never saw. Everything added below is app-owned
// aggregation, and never comes from an adapter.
func (a *App) collectStatus(snapshot *kagentStatusSnapshot, slice agentruntime.LifecycleStatus) (*statusData, error) {
	if snapshot == nil {
		return nil, fmt.Errorf("status aggregation requires the runtime snapshot its adapter read")
	}
	d := &statusData{
		items:      snapshot.items,
		agents:     snapshot.agents,
		models:     snapshot.models,
		kagentPods: snapshot.pods,
		runtime:    slice.Pair.Fields,
	}
	// The plane, the tool servers and the Secret names are read
	// TOLERANTLY: none of them exists on the ungoverned fast path, which is
	// the default, and a status command that fails because the thing it is
	// diagnosing is absent is worthless. Each failure becomes a stated
	// `unknown`, never a silent zero — with one deliberate exception, noted
	// on statusTolerant: a NotFound namespace or resource is a genuine
	// absence and reads as an empty population.
	d.ollamaErr = a.statusTolerant("ollama", "pods", &d.ollamaPods)
	var deployments objectList[planeDeployment]
	d.planeErr = a.statusTolerant(planeNamespace, "deployments", &deployments)
	for _, deployment := range deployments.Items {
		if deployment.Metadata.Name == planeWorkload {
			d.planeThere = true
			d.planeDesired = deployment.Spec.Replicas
			d.planeReady = deployment.Status.ReadyReplicas
		}
	}
	if d.planeErr == "" {
		d.planeErr = a.statusTolerant(planeNamespace, "pods", &d.planePods)
	}
	d.serverErr = a.statusTolerant(config_kagentNamespace, "remotemcpservers", &d.servers)
	if d.serverErr != "" {
		a.notef("RemoteMCPServer inventory unavailable: %s (not model readiness evidence)", d.serverErr)
	}
	// Inventory only: preserve owner URLs verbatim, without treating old
	// gateway references as either managed or healthy direct routing.
	d.items = append(d.items, d.servers.Items...)
	d.secrets, d.secretErr = a.secretNames(config_kagentNamespace)
	d.certificate = a.seamCertificate()
	return d, nil
}

// governanceOf assembles model routing, credential evidence and plane presence.
func (d *statusData) governanceOf() governance {
	plane := planePresence{State: stateNone}
	switch {
	case d.planeErr != "":
		plane = planePresence{State: stateUnknown, Reason: d.planeErr}
	case d.planeThere || len(d.planePods.Items) > 0:
		// INSTALLED is the Deployment existing. A plane scaled to zero, or
		// mid-rollout, or with every pod evicted, is installed and DOWN —
		// telling that operator to run `kmx plane` would be a false absence
		// and the wrong instruction.
		plane = planePresence{State: stateInstalled, Ready: d.planeReady, Desired: d.planeDesired}
	}
	return governance{
		Plane:       plane,
		Certificate: d.certificate,
		ModelSeams:  modelSeams(d.agents.Items, d.models.Items),
		Credentials: credentialSeams(d.models.Items, d.secrets, d.secretErr),
	}
}

func (a *App) statusTable(data *statusData) error {
	agents, models := data.agents, data.models
	kagentPods, ollamaPods, planePods := data.kagentPods, data.ollamaPods, data.planePods
	sort.Slice(kagentPods.Items, func(i, j int) bool { return kagentPods.Items[i].Metadata.Name < kagentPods.Items[j].Metadata.Name })

	agentRows := make([][]string, 0, len(agents.Items))
	allAgents := len(agents.Items) > 0
	for _, agent := range agents.Items {
		servers := make([]string, 0, len(agent.Spec.Declarative.Tools))
		for _, tool := range agent.Spec.Declarative.Tools {
			if tool.MCPServer.Name != "" {
				servers = append(servers, tool.MCPServer.Name)
			}
		}
		now := a.timeNow()
		ready, accepted := condition(agent.Status.Conditions, "Ready"), conditionAged(agent.Status.Conditions, "Accepted", now)
		allAgents = allAgents && ready == "yes" && strings.HasPrefix(accepted, "yes")
		agentRows = append(agentRows, []string{agent.Metadata.Name, ready, accepted, agent.Spec.Declarative.ModelConfig, valueOr(strings.Join(servers, ","), "none")})
	}
	sort.Slice(agentRows, func(i, j int) bool { return agentRows[i][0] < agentRows[j][0] })

	modelRows := make([][]string, 0, len(models.Items))
	allModels := len(models.Items) > 0
	for _, model := range models.Items {
		accepted := conditionAged(model.Status.Conditions, "Accepted", a.timeNow())
		allModels = allModels && strings.HasPrefix(accepted, "yes")
		modelRows = append(modelRows, []string{model.Metadata.Name, model.Spec.Provider, model.Spec.Model, accepted})
	}
	sort.Slice(modelRows, func(i, j int) bool { return modelRows[i][0] < modelRows[j][0] })

	// The kagent restart count is now carried by the adapter's own runtime
	// slice; readiness aggregation still needs the ready/total counts.
	kReady, _, podRows := podSummary(kagentPods.Items)
	oReady, oRestarts, _ := podSummary(ollamaPods.Items)
	pReady, pRestarts, _ := podSummary(planePods.Items)
	overall := statusReady(allAgents, allModels,
		kReady, len(kagentPods.Items), oReady, len(ollamaPods.Items), pReady, len(planePods.Items))
	overall = overall && governanceReady(data.governanceOf()) && data.ollamaErr == ""

	ui := cliui.New(a.Out)
	if ui.Rich() {
		return a.statusRich(ui, data, overall, agentRows, modelRows, podRows,
			oReady, oRestarts, pReady, pRestarts)
	}
	fmt.Fprintln(a.Out, ui.Heading("Kaimahi status"))
	// The source is not decoration. `default` means nothing named this
	// cluster and kmx picked the name, which is a different fact from an
	// operator having typed it, and status is where a confused operator
	// looks first.
	//
	// It states that fact and does not predict the guard's decision. Whether
	// a mutation is refused depends on what else is in the kubeconfig, and
	// restating that rule here would be a second copy of it in the one
	// command that deliberately reads no kubeconfig — free to drift, and
	// wrong the moment the rule moves, which it already has once.
	if a.Cfg.ContextSource == config.SourceDefault {
		fmt.Fprintf(a.Out, "  context: %s (nothing chose this — pick one with `kmx ctx <name>`)\n",
			a.Cfg.KubeContext)
	} else {
		fmt.Fprintf(a.Out, "  context: %s (from %s)\n", a.Cfg.KubeContext, a.Cfg.ContextSource)
	}
	if overall {
		fmt.Fprintf(a.Out, "  result:  %s (%d agents available)\n", ui.Success("ready"), len(agents.Items))
	} else {
		fmt.Fprintf(a.Out, "  result:  %s\n", ui.Warning("attention required"))
	}
	fmt.Fprintf(a.Out, "\n%s\n", ui.Heading("Agents"))
	humanTable(a.Out, []string{"NAME", "READY", "ACCEPTED", "MODEL CONFIG", "TOOL SERVER"}, agentRows)
	fmt.Fprintln(a.Out, "  Ready = can serve requests; Accepted = kagent accepted the configuration.")
	fmt.Fprintln(a.Out, "  Accepted is what kagent decided when it last looked, not a live check: a credential")
	fmt.Fprintln(a.Out, "  written since then has not been tested, however old that answer is.")
	fmt.Fprintf(a.Out, "\n%s\n", ui.Heading("Models"))
	humanTable(a.Out, []string{"CONFIG", "PROVIDER", "MODEL", "ACCEPTED"}, modelRows)
	fmt.Fprintf(a.Out, "\n%s\n", ui.Heading("Runtime"))
	// The runtime line is the adapter's own slice, printed in the column
	// layout the ancillary lines below already use.
	for _, field := range data.runtime {
		fmt.Fprintf(a.Out, "  %-11s %s\n", field.Label+":", field.Value)
	}
	switch {
	case data.ollamaErr != "":
		fmt.Fprintf(a.Out, "  ollama:     unknown — %s\n", data.ollamaErr)
	case len(ollamaPods.Items) > 0:
		fmt.Fprintf(a.Out, "  ollama:     %d/%d pods ready, %d restarts\n", oReady, len(ollamaPods.Items), oRestarts)
	default:
		fmt.Fprintln(a.Out, "  ollama:     not installed")
	}
	// Presence comes from the SAME fact the Governance section below uses —
	// the proxy Deployment — or the two lines contradict each other for a
	// plane that is installed and scaled to zero, and this one tells the
	// operator to install what they already have.
	switch {
	case data.planeErr != "":
		// Not "not installed": we could not look. Saying the plane is
		// absent here would be the same false zero the governance counts
		// below refuse to print.
		fmt.Fprintf(a.Out, "  governance: unknown — %s\n", data.planeErr)
	case data.planeThere || len(planePods.Items) > 0:
		fmt.Fprintf(a.Out, "  governance: %d/%d pods ready, %d restarts\n", pReady, len(planePods.Items), pRestarts)
	default:
		fmt.Fprintln(a.Out, "  governance: not installed (run `kmx plane` for budgets and audit)")
	}
	fmt.Fprintf(a.Out, "\n%s\n", ui.Heading("Runtime pods"))
	humanTable(a.Out, []string{"NAME", "READY", "PHASE", "RESTARTS"}, podRows)
	writeGovernance(a.Out, data.governanceOf())
	fmt.Fprintf(a.Out, "\n%s\n", ui.Accent("Next"))
	if overall {
		fmt.Fprintf(a.Out, "  kmx agent chat %s\n", agentRows[0][0])
	} else {
		fmt.Fprintf(a.Out, "  kubectl --context %s -n kagent get agents.kagent.dev,pods\n", a.Cfg.KubeContext)
	}
	return nil
}

func (a *App) statusRich(ui cliui.Output, data *statusData, overall bool,
	agentRows, modelRows, podRows [][]string, oReady, oRestarts, pReady, pRestarts int) error {
	result := ui.Warning("attention required")
	if overall {
		result = ui.Success(fmt.Sprintf("ready (%d agents available)", len(agentRows)))
	}
	contextValue := fmt.Sprintf("%s (from %s)", a.Cfg.KubeContext, a.Cfg.ContextSource)
	if a.Cfg.ContextSource == config.SourceDefault {
		contextValue = a.Cfg.KubeContext + " (nothing chose this — pick one with `kmx ctx <name>`)"
	}
	fmt.Fprintln(a.Out, ui.Heading("Kaimahi status"))
	fmt.Fprintln(a.Out, ui.Fields([]cliui.Field{{Label: "context", Value: contextValue}, {Label: "result", Value: result}}))

	fmt.Fprintf(a.Out, "\n%s\n", ui.Report("Agents", []string{"NAME", "READY", "ACCEPTED", "MODEL CONFIG", "TOOL SERVER"}, agentRows, cliui.ColumnText, cliui.ColumnState, cliui.ColumnState))
	fmt.Fprintln(a.Out, ui.Muted("Ready = can serve requests; Accepted = kagent's last configuration decision."))
	fmt.Fprintln(a.Out, ui.Muted("A credential written since that decision has not yet been tested."))
	fmt.Fprintf(a.Out, "\n%s\n", ui.Report("Models", []string{"CONFIG", "PROVIDER", "MODEL", "ACCEPTED"}, modelRows, cliui.ColumnText, cliui.ColumnText, cliui.ColumnText, cliui.ColumnState))

	runtime := make([]cliui.Field, 0, len(data.runtime)+2)
	for _, field := range data.runtime {
		runtime = append(runtime, cliui.Field{Label: field.Label, Value: field.Value})
	}
	ollama := "not installed"
	if data.ollamaErr != "" {
		ollama = "unknown — " + data.ollamaErr
	} else if len(data.ollamaPods.Items) > 0 {
		ollama = fmt.Sprintf("%d/%d pods ready, %d restarts", oReady, len(data.ollamaPods.Items), oRestarts)
	}
	plane := "not installed (run `kmx plane` for budgets and audit)"
	if data.planeErr != "" {
		plane = "unknown — " + data.planeErr
	} else if data.planeThere || len(data.planePods.Items) > 0 {
		plane = fmt.Sprintf("%d/%d pods ready, %d restarts", pReady, len(data.planePods.Items), pRestarts)
	}
	runtime = append(runtime, cliui.Field{Label: "ollama", Value: ollama}, cliui.Field{Label: "governance", Value: plane})
	fmt.Fprintf(a.Out, "\n%s\n%s\n", ui.Heading("Runtime"), ui.Fields(runtime))
	fmt.Fprintf(a.Out, "\n%s\n", ui.Report("Runtime pods", []string{"NAME", "READY", "PHASE", "RESTARTS"}, podRows, cliui.ColumnText, cliui.ColumnState, cliui.ColumnState, cliui.ColumnNumber))

	g := data.governanceOf()
	fmt.Fprintf(a.Out, "\n%s\n%s\n", ui.Heading("Governance"), ui.Fields(governanceFields(g)))
	fmt.Fprintln(a.Out, ui.Muted("Governed means the cluster object points at the plane; the plane field says whether enforcement is available."))

	next := cliui.Action{Label: "Inspect the runtime", Command: fmt.Sprintf("kubectl --context %s -n kagent get agents.kagent.dev,pods", a.Cfg.KubeContext)}
	if overall && len(agentRows) > 0 {
		next = cliui.Action{Label: "Chat with an agent", Command: "kmx agent chat " + agentRows[0][0]}
	}
	fmt.Fprintf(a.Out, "\n%s\n", ui.Actions("Next", []cliui.Action{next}))
	return nil
}

func governanceFields(g governance) []cliui.Field {
	plane := "not installed — nothing is enforced in front of these seams (`kmx plane`)"
	switch g.Plane.State {
	case stateUnknown:
		plane = "unknown — " + g.Plane.Reason
	case stateInstalled:
		switch {
		case g.Plane.Desired == 0:
			plane = "installed but SCALED TO ZERO — nothing behind it is being enforced"
		case g.Plane.Ready == 0:
			plane = fmt.Sprintf("installed but DOWN (0/%d replicas ready) — nothing behind it is being enforced", g.Plane.Desired)
		default:
			plane = fmt.Sprintf("installed (%d/%d replicas ready)", g.Plane.Ready, g.Plane.Desired)
		}
	}
	return []cliui.Field{
		{Label: "plane", Value: plane},
		{Label: "model seams", Value: seamLine(g.ModelSeams, "agents", "agent")},
		{Label: "credentials", Value: credentialLine(g.Credentials)},
	}
}

func valueOr(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}

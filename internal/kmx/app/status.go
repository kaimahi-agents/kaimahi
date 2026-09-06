package app

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"

	yaml "go.yaml.in/yaml/v3"
)

// statusRequestTimeout bounds every read status makes.
//
// Re-landed from #37, whose reasoning holds and had nowhere to live once
// scripts/status.py went: this is the command people run when something is
// wrong, and an API server that is unreachable rather than refusing leaves a
// bare `kubectl get` waiting on TCP. A status command that hangs is the same
// failure as one that needs the thing being diagnosed.
const statusRequestTimeout = "--request-timeout=15s"

// StatusOptions controls the human table or the structured document.
type StatusOptions struct {
	Output string
}

type objectList[T any] struct {
	Items []T `json:"items"`
}

type statusCondition struct {
	Type, Status string
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

// toolServerStatus is the tool seam: a RemoteMCPServer's URL is either the
// plane's MCP gateway or it is not, and headersFrom names the Secret the
// governed one authenticates with.
type toolServerStatus struct {
	Metadata struct{ Name string } `json:"metadata"`
	Spec     struct {
		URL         string `json:"url"`
		HeadersFrom []struct {
			ValueFrom struct {
				Type string `json:"type"`
				Name string `json:"name"`
			} `json:"valueFrom"`
		} `json:"headersFrom"`
	} `json:"spec"`
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

func condition(conditions []statusCondition, name string) string {
	for _, value := range conditions {
		if value.Type == name {
			if value.Status == "True" {
				return "yes"
			}
			return "no"
		}
	}
	return "-"
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

func (a *App) statusJSON(namespace, resource string, optional bool, target any) error {
	raw, err := a.kubectlCapture("-n", namespace, "get", resource, "-o", "json", statusRequestTimeout)
	if err != nil {
		if optional && isNotFound(err) {
			return nil
		}
		return err
	}
	return json.Unmarshal([]byte(raw), target)
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
		rows = append(rows, []string{pod.Metadata.Name, map[bool]string{true: "yes", false: "no"}[isReady], pod.Status.Phase, fmt.Sprint(podRestarts)})
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

// Status prints a grouped human view or kubectl-native JSON/YAML.
func (a *App) StatusWithOptions(opt StatusOptions) error {
	format := strings.ToLower(strings.TrimSpace(opt.Output))
	if format != "" && format != "table" && format != "json" && format != "yaml" {
		return fmt.Errorf("status output %q is not supported — use table, json, or yaml", opt.Output)
	}
	if err := a.preflight(depKubectl); err != nil {
		return err
	}
	if format == "" || format == "table" {
		return a.statusTable()
	}
	return a.statusStructured(format)
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

func (a *App) statusStructured(format string) error {
	data, err := a.collectStatus()
	if err != nil {
		return err
	}
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

// statusData is everything one `kmx status` reads, gathered once so the
// human table and the structured document are the same facts — not two
// reads of a cluster that may have changed between them.
type statusData struct {
	agents     objectList[agentStatus]
	models     objectList[modelStatus]
	servers    objectList[toolServerStatus]
	kagentPods objectList[podStatus]
	ollamaPods objectList[podStatus]
	planePods  objectList[podStatus]
	// items is the combined kagent read exactly as kubectl returned it,
	// carried so `-o json` can publish the objects verbatim without asking
	// the cluster a second time.
	items      []json.RawMessage
	secrets    []string
	planeThere bool
	// planeDesired and planeReady come from the proxy Deployment, so a
	// proxy scaled to zero beside a running Postgres is not reported ready.
	planeDesired int
	planeReady   int
	serverErr    string
	planeErr     string
	secretErr    string
	ollamaErr    string
}

// collectStatus reads the cluster once.
//
// The kagent objects come from ONE combined get — the same one the old
// kubectl-native output printed — and are demultiplexed by kind here. That
// is not only three fewer calls: it is what makes `items` and the counts
// beside them a single snapshot, so a consumer cannot find an Agent in
// `items` that the count never saw.
func (a *App) collectStatus() (*statusData, error) {
	d := &statusData{}
	raw, err := a.kubectlCapture("-n", config_kagentNamespace, "get",
		"agents,modelconfigs,pods", "-o", "json", statusRequestTimeout)
	if err != nil {
		return nil, err
	}
	var combined struct {
		Items []json.RawMessage `json:"items"`
	}
	if err := json.Unmarshal([]byte(raw), &combined); err != nil {
		return nil, err
	}
	d.items = combined.Items
	if d.items == nil {
		// An empty cluster publishes `[]`, not `null`: a consumer iterates
		// items, and null makes them vanish with a zero exit code.
		d.items = []json.RawMessage{}
	}
	for _, item := range d.items {
		var kind struct {
			Kind string `json:"kind"`
		}
		if err := json.Unmarshal(item, &kind); err != nil {
			return nil, err
		}
		switch kind.Kind {
		case "Agent":
			var agent agentStatus
			if err := json.Unmarshal(item, &agent); err != nil {
				return nil, err
			}
			d.agents.Items = append(d.agents.Items, agent)
		case "ModelConfig":
			var model modelStatus
			if err := json.Unmarshal(item, &model); err != nil {
				return nil, err
			}
			d.models.Items = append(d.models.Items, model)
		case "Pod":
			var pod podStatus
			if err := json.Unmarshal(item, &pod); err != nil {
				return nil, err
			}
			d.kagentPods.Items = append(d.kagentPods.Items, pod)
		}
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
	d.secrets, d.secretErr = a.secretNames(config_kagentNamespace)
	return d, nil
}

// governanceOf assembles the three counts and the plane's presence.
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
	seamErr := ""
	if d.serverErr != "" {
		seamErr = "the tool seams could not be read, so what they require is not counted here"
	}
	return governance{
		Plane:       plane,
		ModelSeams:  modelSeams(d.agents.Items, d.models.Items),
		ToolSeams:   toolSeams(d.servers.Items, d.serverErr),
		Credentials: credentialSeams(d.models.Items, d.servers.Items, d.secrets, d.secretErr, seamErr),
	}
}

func (a *App) statusTable() error {
	data, err := a.collectStatus()
	if err != nil {
		return err
	}
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
		ready, accepted := condition(agent.Status.Conditions, "Ready"), condition(agent.Status.Conditions, "Accepted")
		allAgents = allAgents && ready == "yes" && accepted == "yes"
		agentRows = append(agentRows, []string{agent.Metadata.Name, ready, accepted, agent.Spec.Declarative.ModelConfig, valueOr(strings.Join(servers, ","), "none")})
	}
	sort.Slice(agentRows, func(i, j int) bool { return agentRows[i][0] < agentRows[j][0] })

	modelRows := make([][]string, 0, len(models.Items))
	allModels := len(models.Items) > 0
	for _, model := range models.Items {
		accepted := condition(model.Status.Conditions, "Accepted")
		allModels = allModels && accepted == "yes"
		modelRows = append(modelRows, []string{model.Metadata.Name, model.Spec.Provider, model.Spec.Model, accepted})
	}
	sort.Slice(modelRows, func(i, j int) bool { return modelRows[i][0] < modelRows[j][0] })

	kReady, kRestarts, podRows := podSummary(kagentPods.Items)
	oReady, oRestarts, _ := podSummary(ollamaPods.Items)
	pReady, pRestarts, _ := podSummary(planePods.Items)
	overall := statusReady(allAgents, allModels,
		kReady, len(kagentPods.Items), oReady, len(ollamaPods.Items), pReady, len(planePods.Items))

	fmt.Fprintln(a.Out, "Kaimahi status")
	fmt.Fprintf(a.Out, "  context: %s (from %s)\n", a.Cfg.KubeContext, a.Cfg.ContextSource)
	if overall {
		fmt.Fprintf(a.Out, "  result:  ready (%d agents available)\n", len(agents.Items))
	} else {
		fmt.Fprintln(a.Out, "  result:  attention required")
	}
	fmt.Fprintln(a.Out, "\nAgents")
	table(a.Out, []string{"NAME", "READY", "ACCEPTED", "MODEL CONFIG", "TOOL SERVER"}, agentRows)
	fmt.Fprintln(a.Out, "  Ready = can serve requests; Accepted = kagent accepted the configuration.")
	fmt.Fprintln(a.Out, "\nModels")
	table(a.Out, []string{"CONFIG", "PROVIDER", "MODEL", "ACCEPTED"}, modelRows)
	fmt.Fprintln(a.Out, "\nRuntime")
	fmt.Fprintf(a.Out, "  kagent:     %d/%d pods ready, %d restarts\n", kReady, len(kagentPods.Items), kRestarts)
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
	fmt.Fprintln(a.Out, "\nRuntime pods")
	table(a.Out, []string{"NAME", "READY", "PHASE", "RESTARTS"}, podRows)
	writeGovernance(a.Out, data.governanceOf())
	fmt.Fprintln(a.Out, "\nNext")
	if overall {
		fmt.Fprintf(a.Out, "  kmx agent chat %s\n", agentRows[0][0])
	} else {
		fmt.Fprintf(a.Out, "  kubectl --context %s -n kagent get agents,pods\n", a.Cfg.KubeContext)
	}
	return nil
}

func valueOr(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}

package app

// `kmx cluster orka show` — one screen for "will this agent actually work, and if
// not, which hop is broken".
//
// An Orka Agent depends on a Provider, which depends on a Secret, and the
// failure that matters is almost never in the Agent itself: a Provider that
// is not ready refuses every model call the agent makes, and the agent's own
// object says nothing about it. Answering that today means `kubectl get`
// three kinds and joining them by hand, which is the kind of assembly a
// person does wrong at the moment they are least able to afford it.
//
// So this reads the chain and renders it as a chain. It asks for nothing an
// operator could not ask for themselves; what it adds is the join, the
// readiness at each hop, and the refusal to report an unread hop as a
// healthy one.
//
// What it deliberately does not do: read a Secret's VALUE. Presence is the
// only fact about a credential this command needs, and the only one it takes.

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/cliui"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/scaffold"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/secretshapes"
)

var errUnsafeOrkaReference = errors.New("unsafe Orka resource reference")

// ShowOptions are `kmx cluster orka show`'s knobs.
type ShowOptions struct {
	// Namespace the Orka controller watches. Orka watches namespaces explicitly,
	// and a default that guessed wrong would report "not found" about a namespace
	// the operator never meant.
	Namespace string
	// Output selects table (default) or json.
	Output string
	// Tasks bounds how many recent Tasks are read back.
	Tasks int
}

// orkaAgentSpec is the half of an Orka Agent this view needs. The fields are
// named exactly as their CRD names them, so a reader can check this against
// `kubectl explain` rather than against our vocabulary.
type orkaAgentSpec struct {
	Metadata struct {
		Name      string `json:"name"`
		Namespace string `json:"namespace"`
		// UID is decoded, not displayed: it is the one identity that does
		// not survive a delete and recreate, so a lifecycle caller holding a
		// reference can prove this is still the same Agent.
		UID        string `json:"uid"`
		Generation int64  `json:"generation"`
	} `json:"metadata"`
	Spec struct {
		ProviderRef struct {
			Name      string `json:"name"`
			Namespace string `json:"namespace"`
		} `json:"providerRef"`
		Model struct {
			Name      string `json:"name"`
			Provider  string `json:"provider"`
			MaxTokens int    `json:"maxTokens"`
		} `json:"model"`
		SystemPrompt struct {
			Inline       string `json:"inline"`
			ConfigMapRef struct {
				Name string `json:"name"`
			} `json:"configMapRef"`
		} `json:"systemPrompt"`
		Tools []struct {
			Name    string `json:"name"`
			Enabled *bool  `json:"enabled"`
		} `json:"tools"`
		Skills []struct {
			Name string `json:"name"`
		} `json:"skills"`
		Runtime json.RawMessage `json:"runtime"`
	} `json:"spec"`
	Status struct {
		Ready       bool              `json:"ready"`
		ActiveTasks int               `json:"activeTasks"`
		LastUsed    string            `json:"lastUsed"`
		Conditions  []serverCondition `json:"conditions"`
	} `json:"status"`
}

type orkaProviderSpec struct {
	Metadata struct {
		Name      string `json:"name"`
		Namespace string `json:"namespace"`
	} `json:"metadata"`
	Spec struct {
		Type         string `json:"type"`
		BaseURL      string `json:"baseURL"`
		DefaultModel string `json:"defaultModel"`
		Azure        struct {
			DeploymentName string `json:"deploymentName"`
			APIVersion     string `json:"apiVersion"`
		} `json:"azure"`
		SecretRef struct {
			Name string `json:"name"`
			Key  string `json:"key"`
		} `json:"secretRef"`
	} `json:"spec"`
	Status struct {
		Ready         bool   `json:"ready"`
		Message       string `json:"message"`
		LastValidated string `json:"lastValidated"`
	} `json:"status"`
}

type orkaTaskSummary struct {
	Metadata struct {
		Name              string    `json:"name"`
		Namespace         string    `json:"namespace"`
		CreationTimestamp time.Time `json:"creationTimestamp"`
	} `json:"metadata"`
	Spec struct {
		AgentRef struct {
			Name      string `json:"name"`
			Namespace string `json:"namespace"`
		} `json:"agentRef"`
	} `json:"spec"`
	Status struct {
		Phase   string `json:"phase"`
		Message string `json:"message"`
	} `json:"status"`
}

// unknown is what every hop reports when it could not be read. It is a
// distinct third state on purpose: an unreachable API server and a Provider
// that is genuinely not ready have opposite fixes, and a view that collapsed
// them would send an operator to rebuild something that was fine.
const unknownHop = "unknown"

// ShowAgent renders one Orka Agent and the chain it depends on.
func (a *App) ShowAgent(name string, opt ShowOptions) error {
	if strings.TrimSpace(opt.Namespace) == "" {
		return fmt.Errorf("kmx cluster orka show %s: --namespace is required.\n"+
			"  Orka watches namespaces explicitly, so there is no safe default to guess:\n"+
			"  a wrong one would report \"not found\" about a namespace you never meant", name)
	}
	format := strings.ToLower(strings.TrimSpace(opt.Output))
	if format == "" {
		format = "table"
	}
	if format != "table" && format != "json" {
		return fmt.Errorf("orka show output %q is not supported — use table or json", opt.Output)
	}
	if scaffold.ValidateObjectName(name) != nil || scaffold.ValidateNamespace(opt.Namespace) != nil {
		return fmt.Errorf("kmx cluster orka show requires a valid Agent name and namespace")
	}
	if opt.Tasks <= 0 {
		opt.Tasks = 5
	}
	if err := a.preflightOrkaInspection(); err != nil {
		return err
	}

	agent, err := a.readOrkaAgent(opt.Namespace, name)
	if err != nil {
		return err
	}
	providerNamespace := agent.Spec.ProviderRef.Namespace
	if providerNamespace == "" {
		providerNamespace = agent.Metadata.Namespace
	}
	provider, providerErr := a.readOrkaProvider(providerNamespace, agent.Spec.ProviderRef.Name)
	if errors.Is(providerErr, errUnsafeOrkaReference) {
		return providerErr
	}
	secretPresent, secretErr := a.secretPresence(providerNamespace, provider)
	tasks := a.recentOrkaTasks(opt.Namespace, name, opt.Tasks)

	if format == "json" {
		return a.showAgentJSON(agent, provider, providerErr, secretPresent, secretErr, tasks)
	}
	a.showAgentTable(agent, provider, providerErr, secretPresent, secretErr, tasks)
	return nil
}

func (a *App) readOrkaAgent(namespace, name string) (*orkaAgentSpec, error) {
	raw, err := a.orkaCapture(a.operationContext(), nil, "-n", namespace, "get", "agents.core.orka.ai", name, "-o", "json")
	if err != nil {
		if noSuchResourceType(err) {
			return nil, a.missingOrkaInspectionKind()
		}
		if isNotFound(err) {
			return nil, fmt.Errorf("no Orka Agent %q in namespace %s.\n"+
				"  `%s` shows what is there. Author workspace source with `%s`, prove it with eval, then Lift its immutable digest",
				name, namespace,
				a.operationCommand("cluster", "orka", "list", "--namespace", namespace),
				a.operationCommand("create", "<name>", "--workspace", "<directory>", "--model", "<exact-model>", "--instructions", "<file>"))
		}
		return nil, fmt.Errorf("cannot read Orka Agent %q in namespace %s: %w", name, namespace, err)
	}
	var agent orkaAgentSpec
	if err := json.Unmarshal(raw, &agent); err != nil {
		return nil, fmt.Errorf("Orka Agent %q returned invalid JSON: %w", name, err)
	}
	if agent.Metadata.Name != name || agent.Metadata.Namespace != namespace ||
		agent.Spec.ProviderRef.Name != "" && scaffold.ValidateObjectName(agent.Spec.ProviderRef.Name) != nil ||
		agent.Spec.ProviderRef.Namespace != "" && scaffold.ValidateNamespace(agent.Spec.ProviderRef.Namespace) != nil {
		return nil, fmt.Errorf("%w: Orka Agent %q returned invalid identity or Provider reference", errUnsafeOrkaReference, name)
	}
	return &agent, nil
}

// readOrkaProvider returns the Provider the Agent names. A missing name is
// not an error here — it is a fact the view reports, because an Agent whose
// providerRef is empty is exactly the broken state this command exists to
// make visible.
func (a *App) readOrkaProvider(namespace, name string) (*orkaProviderSpec, error) {
	if strings.TrimSpace(name) == "" {
		return nil, fmt.Errorf("the Agent names no Provider")
	}
	raw, err := a.orkaCapture(a.operationContext(), nil, "-n", namespace, "get", "providers.core.orka.ai", name, "-o", "json")
	if err != nil {
		if noSuchResourceType(err) {
			return nil, fmt.Errorf("the Orka Provider resource type is unavailable")
		}
		if isNotFound(err) {
			return nil, fmt.Errorf("Provider %q does not exist, so every model call is refused", name)
		}
		return nil, fmt.Errorf("could not be read: %w", err)
	}
	var provider orkaProviderSpec
	if err := json.Unmarshal(raw, &provider); err != nil {
		return nil, fmt.Errorf("returned invalid JSON: %w", err)
	}
	if provider.Metadata.Name != name || provider.Metadata.Namespace != namespace ||
		provider.Spec.SecretRef.Name != "" && scaffold.ValidateObjectName(provider.Spec.SecretRef.Name) != nil {
		return nil, fmt.Errorf("%w: Provider returned an invalid Secret reference", errUnsafeOrkaReference)
	}
	return &provider, nil
}

// secretPresence reports whether the Provider's credential Secret exists.
//
// Presence only. The value is never read, never printed and never held: what
// this view needs to answer is "is the thing the Provider names there", and
// reading a credential to answer a question about its existence would be a
// worse trade than leaving the question unanswered.
func (a *App) secretPresence(namespace string, provider *orkaProviderSpec) (bool, error) {
	if provider == nil || strings.TrimSpace(provider.Spec.SecretRef.Name) == "" {
		return false, fmt.Errorf("no Secret named")
	}
	_, err := a.orkaCapture(a.operationContext(), nil, "-n", namespace, "get", "secret", provider.Spec.SecretRef.Name, "-o", "name")
	switch {
	case err == nil:
		return true, nil
	case isNotFound(err):
		return false, nil
	default:
		return false, err
	}
}

// recentOrkaTasks reads the Tasks that name this Agent, newest first.
//
// Failures are swallowed into an empty list on purpose: Tasks are history,
// not health, and a cluster that cannot list them should not stop the view
// that answers whether the agent is wired correctly. The absence is shown as
// "unread" rather than as "none".
func (a *App) recentOrkaTasks(namespace, agent string, limit int) []orkaTaskSummary {
	// The Task CRD has no selectable Agent field, so retain the complete scan
	// semantics within fixed page, byte and continuation-token bounds.
	const pageLimit, pageCountLimit = 128, 32
	const historyLimit = 4 << 20
	ctx := a.operationContext()
	mine := make([]orkaTaskSummary, 0, min(limit, pageLimit))
	cursor := ""
	seen := map[string]bool{}
	total := 0
	for pageCount := 0; pageCount < pageCountLimit; pageCount++ {
		path := fmt.Sprintf("/apis/core.orka.ai/v1alpha1/namespaces/%s/tasks?limit=%d", url.PathEscape(namespace), pageLimit)
		if cursor != "" {
			path += "&continue=" + url.QueryEscape(cursor)
		}
		raw, err := a.orkaCapture(ctx, nil, "get", "--raw", path)
		if err != nil || len(raw) > historyLimit-total {
			return nil
		}
		total += len(raw)
		var page struct {
			Metadata json.RawMessage   `json:"metadata"`
			Items    []orkaTaskSummary `json:"items"`
		}
		if json.Unmarshal(raw, &page) != nil || len(page.Items) > pageLimit {
			return nil
		}
		for _, task := range page.Items {
			refNamespace := task.Spec.AgentRef.Namespace
			if refNamespace == "" {
				refNamespace = task.Metadata.Namespace
			}
			if task.Metadata.Namespace == namespace && task.Spec.AgentRef.Name == agent && refNamespace == namespace {
				mine = append(mine, task)
			}
		}
		var metadata map[string]json.RawMessage
		if len(page.Metadata) != 0 && (json.Unmarshal(page.Metadata, &metadata) != nil || metadata == nil) {
			return nil
		}
		next := ""
		if value, ok := metadata["continue"]; ok {
			if len(value) > 4096 || strings.TrimSpace(string(value)) == "null" || json.Unmarshal(value, &next) != nil {
				return nil
			}
		}
		if next == "" {
			break
		}
		if len(next) > 4096 || !utf8.ValidString(next) || strings.IndexFunc(next, unicode.IsControl) >= 0 || seen[next] || next == cursor {
			return nil
		}
		seen[next] = true
		cursor = next
		if pageCount == pageCountLimit-1 {
			return nil
		}
	}
	sort.Slice(mine, func(i, j int) bool {
		return mine[i].Metadata.CreationTimestamp.After(mine[j].Metadata.CreationTimestamp)
	})
	if len(mine) > limit {
		mine = mine[:limit]
	}
	return mine
}

func (a *App) showAgentTable(agent *orkaAgentSpec, provider *orkaProviderSpec, providerErr error,
	secretPresent bool, secretErr error, tasks []orkaTaskSummary) {
	ui := cliui.New(a.Out)
	providerNamespace := valueOr(agent.Spec.ProviderRef.Namespace, agent.Metadata.Namespace)
	providerName := qualifiedOrkaName(providerNamespace, agent.Metadata.Namespace, agent.Spec.ProviderRef.Name)

	fmt.Fprintf(a.Out, "\n%s\n%s\n", ui.Heading(orkaHumanField(agent.Metadata.Name)),
		ui.Fields([]cliui.Field{
			{Label: "namespace", Value: orkaHumanField(agent.Metadata.Namespace)},
			{Label: "ready", Value: readyWord(agent.Status.Ready)},
			{Label: "model", Value: orkaHumanField(agentModelName(agent, provider))},
			{Label: "active tasks", Value: fmt.Sprintf("%d", agent.Status.ActiveTasks)},
			{Label: "last used", Value: orDash(orkaHumanField(agent.Status.LastUsed))},
		}))

	// The chain, as a chain. Each hop carries the one fact that decides
	// whether the next one can work.
	chain := cliui.Action{Label: "model path", Detail: "what a prompt travels through"}
	switch {
	case providerErr != nil:
		chain.Children = append(chain.Children, cliui.Action{
			Label:  "provider " + orDash(orkaHumanField(providerName)),
			Detail: "UNREADY — " + orkaHumanField(providerErr.Error()),
		})
	default:
		detail := orkaHumanField(fmt.Sprintf("%s → %s", orDash(provider.Spec.Type), safeOrkaProviderEndpoint(provider.Spec.BaseURL)))
		if !provider.Status.Ready {
			detail = orkaHumanField("NOT READY — " + detail)
		}
		providerNode := cliui.Action{Label: "provider " + orkaHumanField(providerName), Detail: detail}
		providerNode.Children = append(providerNode.Children, cliui.Action{
			Label:  "secret " + orDash(orkaHumanField(qualifiedOrkaName(providerNamespace, agent.Metadata.Namespace, provider.Spec.SecretRef.Name))),
			Detail: orkaHumanField(secretWord(secretPresent, secretErr)),
		})
		chain.Children = append(chain.Children, providerNode)
	}
	fmt.Fprintf(a.Out, "\n%s\n", ui.Actions("Depends on", []cliui.Action{chain}))

	if len(agent.Spec.Tools) > 0 || len(agent.Spec.Skills) > 0 {
		fmt.Fprintf(a.Out, "%s\n", ui.Fields([]cliui.Field{
			{Label: "tools", Value: orNone(orkaHumanField(strings.Join(agentToolNames(agent), ", ")))},
			{Label: "skills", Value: orNone(orkaHumanField(strings.Join(agentSkillNames(agent), ", ")))},
		}))
	}

	if len(tasks) == 0 {
		fmt.Fprintf(a.Out, "\n%s\n  %s\n", ui.Heading("Recent tasks"),
			"none read — no Task names this Agent, or they could not be listed")
		return
	}
	rows := make([][]string, 0, len(tasks))
	for _, task := range tasks {
		rows = append(rows, []string{
			orkaHumanField(task.Metadata.Name),
			orDash(orkaHumanField(task.Status.Phase)),
			orkaHumanText(task.Status.Message, 48),
		})
	}
	fmt.Fprintf(a.Out, "\n%s\n", ui.Report("Recent tasks",
		[]string{"NAME", "PHASE", "MESSAGE"}, rows, cliui.ColumnText, cliui.ColumnState))
}

func (a *App) showAgentJSON(agent *orkaAgentSpec, provider *orkaProviderSpec, providerErr error,
	secretPresent bool, secretErr error, tasks []orkaTaskSummary) error {
	type hop struct {
		Name   string `json:"name"`
		Ready  string `json:"ready"`
		Detail string `json:"detail,omitempty"`
	}
	out := struct {
		Agent    hop                 `json:"agent"`
		Provider hop                 `json:"provider"`
		Secret   hop                 `json:"secret"`
		Model    string              `json:"model"`
		Tools    []string            `json:"tools,omitempty"`
		Tasks    []map[string]string `json:"recent_tasks"`
	}{
		Agent: hop{Name: agent.Metadata.Name, Ready: readyWord(agent.Status.Ready)},
		Model: agentModelName(agent, provider),
		Tools: agentToolNames(agent),
	}
	providerNamespace := valueOr(agent.Spec.ProviderRef.Namespace, agent.Metadata.Namespace)
	providerName := qualifiedOrkaName(providerNamespace, agent.Metadata.Namespace, agent.Spec.ProviderRef.Name)
	if providerErr != nil {
		out.Provider = hop{Name: providerName, Ready: unknownHop, Detail: providerErr.Error()}
		out.Secret = hop{Ready: unknownHop, Detail: "not reached: the Provider was not read"}
	} else {
		out.Provider = hop{Name: providerName, Ready: readyWord(provider.Status.Ready),
			Detail: safeOrkaProviderEndpoint(provider.Spec.BaseURL)}
		out.Secret = hop{Name: qualifiedOrkaName(providerNamespace, agent.Metadata.Namespace, provider.Spec.SecretRef.Name), Ready: secretWord(secretPresent, secretErr)}
	}
	for _, task := range tasks {
		out.Tasks = append(out.Tasks, map[string]string{"name": task.Metadata.Name, "phase": task.Status.Phase})
	}
	body, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(a.Out, string(body))
	return err
}

func qualifiedOrkaName(namespace, ownerNamespace, name string) string {
	if name != "" && namespace != "" && namespace != ownerNamespace {
		return namespace + "/" + name
	}
	return name
}

func safeOrkaProviderEndpoint(value string) string {
	if value == "" {
		return "-"
	}
	if len(value) > 2048 || !utf8.ValidString(value) || secretshapes.Match(value) != nil ||
		strings.IndexFunc(value, func(r rune) bool { return unicode.IsControl(r) || unicode.Is(unicode.Cf, r) }) >= 0 {
		return "endpoint withheld: invalid or credential-bearing"
	}
	u, err := url.Parse(value)
	if err != nil || u.Opaque != "" || u.Scheme != "http" && u.Scheme != "https" || u.Hostname() == "" ||
		u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.RawFragment != "" {
		return "endpoint withheld: invalid or credential-bearing"
	}
	if secretshapes.Match(u.Path) != nil {
		return "endpoint withheld: invalid or credential-bearing"
	}
	safe := u.Scheme + "://" + u.Host + u.EscapedPath()
	if secretshapes.Match(safe) != nil {
		return "endpoint withheld: invalid or credential-bearing"
	}
	return safe
}

// agentModelName resolves what the agent will actually ask for. An Agent may
// name no model, in which case Orka falls back to the Provider's
// defaultModel — so a view that printed an empty cell would hide the answer
// rather than report it.
func agentModelName(agent *orkaAgentSpec, provider *orkaProviderSpec) string {
	if name := strings.TrimSpace(agent.Spec.Model.Name); name != "" {
		return name
	}
	if provider != nil {
		if name := strings.TrimSpace(provider.Spec.DefaultModel); name != "" {
			return name + " (the Provider's default)"
		}
	}
	return "- (the Agent names none and no Provider default was read)"
}

// agentToolNames reports a disabled tool as disabled rather than dropping it:
// a tool that is listed and off is a different state from one that was never
// wired, and only one of them is somebody's mistake.
func agentToolNames(agent *orkaAgentSpec) []string {
	names := make([]string, 0, len(agent.Spec.Tools))
	for _, tool := range agent.Spec.Tools {
		if tool.Enabled != nil && !*tool.Enabled {
			names = append(names, tool.Name+" (disabled)")
			continue
		}
		names = append(names, tool.Name)
	}
	return names
}

func agentSkillNames(agent *orkaAgentSpec) []string {
	names := make([]string, 0, len(agent.Spec.Skills))
	for _, skill := range agent.Spec.Skills {
		names = append(names, skill.Name)
	}
	return names
}

func readyWord(ready bool) string {
	if ready {
		return "yes"
	}
	return "no"
}

// secretWord keeps "absent" and "unread" apart. A Secret that is missing is
// an operator's to create; one that could not be read may be RBAC, and
// telling them to create it would be wrong.
func secretWord(present bool, err error) string {
	switch {
	case err != nil:
		return unknownHop + " — " + err.Error()
	case present:
		return "present"
	default:
		return "MISSING — the Provider cannot authenticate"
	}
}

func orDash(value string) string {
	if strings.TrimSpace(value) == "" {
		return "-"
	}
	return value
}

func orNone(value string) string {
	if strings.TrimSpace(value) == "" {
		return "none"
	}
	return value
}

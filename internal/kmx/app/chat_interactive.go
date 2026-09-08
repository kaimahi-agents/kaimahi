package app

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"regexp"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/guard"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/scaffold"
)

const maxControllerResponse = 10 << 20

var controllerClient = &http.Client{Timeout: 30 * time.Second}
var agentNameRE = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`)

type actorColor string

const (
	colorBlue    actorColor = "\033[34;1m"
	colorCyan    actorColor = "\033[36;1m"
	colorGreen   actorColor = "\033[32;1m"
	colorMagenta actorColor = "\033[35;1m"
	colorYellow  actorColor = "\033[33;1m"
	colorRed     actorColor = "\033[31;1m"
	colorReset              = "\033[0m"
)

type chatRenderer struct {
	out        io.Writer
	mu         sync.Mutex
	color      bool
	cursor     bool
	openActor  string
	actorLine  bool
	promptOpen bool
}

func newChatRenderer(out io.Writer) *chatRenderer {
	terminal := isInteractiveTerminal(out) && os.Getenv("TERM") != "dumb"
	plain := os.Getenv("NO_COLOR") != ""
	return &chatRenderer{out: out, color: terminal && !plain, cursor: terminal && !plain}
}

func (r *chatRenderer) label(text string, color actorColor) string {
	text = strings.Join(strings.Fields(safeTerminal(text)), " ")
	if !r.color {
		return text
	}
	return string(color) + text + colorReset
}

func indentPayload(value string) string {
	value = strings.TrimSuffix(safeTerminal(value), "\n")
	if value == "" {
		return "  (none)"
	}
	return "  " + strings.ReplaceAll(value, "\n", "\n  ")
}

func assistantPayload(value string) string {
	// The rail preserves authored indentation while ensuring response text can
	// never occupy the renderer-owned four-space action-label position.
	return strings.ReplaceAll(safeTerminal(value), "\n", "\n  | ")
}

func (r *chatRenderer) clearLocked() {
	if r.cursor {
		fmt.Fprint(r.out, "\r\033[2K")
	}
}

func (r *chatRenderer) closeLocked() {
	if r.promptOpen {
		fmt.Fprintln(r.out)
		r.promptOpen = false
	}
	if r.openActor != "" {
		if r.actorLine {
			fmt.Fprintln(r.out)
		}
		fmt.Fprintln(r.out)
		r.openActor = ""
		r.actorLine = false
	}
}

func (r *chatRenderer) block(label string, color actorColor, payload string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.clearLocked()
	r.closeLocked()
	fmt.Fprintf(r.out, "%s\n%s\n\n", r.label(label, color), indentPayload(payload))
}

func (r *chatRenderer) statusStart(agent string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.clearLocked()
	r.closeLocked()
	fmt.Fprintf(r.out, "CHAT STATUS\n------------\n  Agent: %s\n  Commands: %s\n", safeTerminal(agent), slashCommandSummary())
}

func (r *chatRenderer) statusSection(label, payload string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	label = strings.Join(strings.Fields(safeTerminal(label)), " ")
	payload = strings.TrimSuffix(safeTerminal(payload), "\n")
	fmt.Fprintf(r.out, "  %s\n", label)
	if payload != "" {
		fmt.Fprintf(r.out, "    %s\n", strings.ReplaceAll(payload, "\n", "\n    "))
	}
}

func (r *chatRenderer) statusEnd() {
	r.mu.Lock()
	defer r.mu.Unlock()
	fmt.Fprintln(r.out, "------------------------------------------------------------")
	fmt.Fprintln(r.out)
}

func (r *chatRenderer) operation(kind, subject string, color actorColor, payload string) {
	label := "[" + kind + "]"
	if subject != "" {
		payload = "Tool: " + safeTerminal(subject) + "\n" + payload
	}
	r.block(label, color, payload)
}

func (r *chatRenderer) operationPrompt(kind string, color actorColor, payload, prompt string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.clearLocked()
	r.closeLocked()
	fmt.Fprintf(r.out, "%s\n%s\n  %s ", r.label("["+kind+"]", color), indentPayload(payload), safeTerminal(prompt))
	r.promptOpen = true
}

func (r *chatRenderer) beginAssistant(agent string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.clearLocked()
	if r.openActor == agent {
		return
	}
	r.closeLocked()
	fmt.Fprintln(r.out, r.label("AGENT ("+safeTerminal(agent)+")", colorGreen))
	r.openActor = agent
}

func (r *chatRenderer) assistantOperation(agent, kind, subject string, color actorColor, payload string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.clearLocked()
	if r.openActor != agent {
		r.closeLocked()
		fmt.Fprintln(r.out, r.label("AGENT ("+safeTerminal(agent)+")", colorGreen))
		r.openActor = agent
	}
	if r.actorLine {
		fmt.Fprintln(r.out)
		r.actorLine = false
	}
	if subject != "" {
		payload = "Tool: " + safeTerminal(subject) + "\n" + payload
	}
	payload = strings.TrimSuffix(safeTerminal(payload), "\n")
	fmt.Fprintf(r.out, "    %s\n      %s\n\n", r.label("["+kind+"]", color), strings.ReplaceAll(payload, "\n", "\n      "))
}

func (r *chatRenderer) assistant(agent, text string, start bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.clearLocked()
	if r.openActor != agent {
		r.closeLocked()
		fmt.Fprintln(r.out, r.label("AGENT ("+safeTerminal(agent)+")", colorGreen))
		r.openActor = agent
	}
	if start && r.actorLine {
		fmt.Fprintln(r.out)
		r.actorLine = false
	}
	if !r.actorLine {
		fmt.Fprint(r.out, "  | ")
		r.actorLine = true
	}
	fmt.Fprint(r.out, assistantPayload(text))
}

func (r *chatRenderer) clearTransient() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.clearLocked()
}

func (r *chatRenderer) prompt() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.clearLocked()
	r.closeLocked()
	fmt.Fprintf(r.out, "%s ", r.label("YOU >", colorCyan))
	r.promptOpen = true
}

func (r *chatRenderer) submitted(inputWasTerminal bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.promptOpen && !inputWasTerminal {
		fmt.Fprintln(r.out)
	}
	r.promptOpen = false
}

func (r *chatRenderer) spinner(agent, frame string, elapsed time.Duration) {
	if !r.cursor {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.actorLine {
		return
	}
	r.clearLocked()
	fmt.Fprintf(r.out, "%s %s %ds", r.label("WORKING", colorBlue), safeTerminal(agent)+" "+frame, int(elapsed.Seconds()))
}

func (r *chatRenderer) finish() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.clearLocked()
	r.closeLocked()
}

func safeTerminal(s string) string {
	var out strings.Builder
	const (
		normal = iota
		escape
		csi
		osc
		oscEscape
	)
	state := normal
	for _, r := range s {
		switch state {
		case escape:
			switch r {
			case '[':
				state = csi
			case ']':
				state = osc
			default:
				state = normal
			}
			continue
		case csi:
			if r >= '@' && r <= '~' {
				state = normal
			}
			continue
		case osc:
			if r == '\a' {
				state = normal
			} else if r == '\x1b' {
				state = oscEscape
			}
			continue
		case oscEscape:
			if r == '\\' {
				state = normal
			} else {
				state = osc
			}
			continue
		}
		if r == '\x1b' {
			state = escape
			continue
		}
		if r == '\n' || r == '\t' || (!unicode.IsControl(r) && r != '\u007f') {
			out.WriteRune(r)
		}
	}
	return out.String()
}

func controllerRequest(ctx context.Context, method, endpoint string, body io.Reader) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, endpoint, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-user-id", "admin@kagent.dev")
	return controllerClient.Do(req)
}

// seamScheme accepts both schemes a plane's seam can be addressed by.
//
// https is what this repository writes now, and what an operator should see.
// http is still recognised as GOVERNED, because it is: the seam enforces on
// the credential, and a cluster deployed before the certificate existed is
// metered, allowlisted, bound and audited exactly as it was. Refusing to call
// it governed would report a plane that is enforcing as one that is not,
// which is a worse error than the one it would be guarding against — and it
// would say so at the moment an operator is least able to check.
//
// What plaintext costs is confidentiality of the content on the wire, and
// that is a different sentence from "not governed". `kmx status` names the
// certificate; this answers the routing question only.
func seamScheme(scheme string) bool { return scheme == "https" || scheme == "http" }

func usesKaimahiModelProxy(spec map[string]any) bool {
	var visit func(any) bool
	visit = func(value any) bool {
		switch value := value.(type) {
		case map[string]any:
			for key, child := range value {
				if strings.EqualFold(key, "baseUrl") || strings.EqualFold(key, "base_url") {
					if raw, ok := child.(string); ok {
						parsed, err := url.Parse(raw)
						if err == nil && parsed != nil {
							host := parsed.Host
							validHost := host == "kaimahi-proxy.kaimahi:8080" || host == "kaimahi-proxy.kaimahi.svc.cluster.local:8080"
							if seamScheme(parsed.Scheme) && validHost && strings.HasPrefix(parsed.Path, "/upstream/") {
								return true
							}
						}
						continue
					}
				}
				if visit(child) {
					return true
				}
			}
		case []any:
			for _, child := range value {
				if visit(child) {
					return true
				}
			}
		}
		return false
	}
	return visit(spec)
}

type streamView struct {
	agent, context, taskID, state, reply string
	toolCalls                            map[string]string
	messageText                          map[string]string
	toolMode                             string
	denied                               bool
	requestFiled                         bool
	approval                             *hitlRequest
	partials                             string
	approvalErr                          error
	visible                              atomic.Bool
	renderer                             *chatRenderer
	modelGoverned                        bool
	governedTools                        map[string]bool
	modelDenialShown                     bool
	seenToolCalls                        map[string]bool
	seenToolResponses                    map[string]bool
	ambiguousToolCalls                   map[string]bool
}

type chatGovernancePosture struct {
	modelGoverned bool
	governedTools map[string]bool
	toolRoutes    map[string]uint8
}

type hitlRequest struct {
	TaskID, ContextID string
	Hint              string
	Calls             []hitlCall
}

type hitlCall struct {
	ID, Name string
	Args     json.RawMessage
}

type askUserQuestion struct {
	Question string   `json:"question"`
	Choices  []string `json:"choices"`
}

type streamEvent struct {
	Kind      string          `json:"kind"`
	ContextID string          `json:"contextId"`
	TaskID    string          `json:"taskId"`
	Final     bool            `json:"final"`
	LastChunk bool            `json:"lastChunk"`
	Status    json.RawMessage `json:"status"`
	Artifact  struct {
		Parts []struct {
			Kind, Text string
		} `json:"parts"`
	} `json:"artifact"`
}

func (a *App) interactiveChat(kagent, agent, initialTask, session string) error {
	if len(agent) > 63 || !agentNameRE.MatchString(agent) {
		return fmt.Errorf("agent name %q is not a valid Kubernetes name", agent)
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()
	if err := a.waitServable(agent); err != nil {
		return err
	}
	port, stop, err := a.portForward()
	if err != nil {
		return err
	}
	defer stop()
	base := "http://127.0.0.1:" + port
	renderer := newChatRenderer(a.Out)
	toolMode := "summary"
	posture, err := a.refreshChatPosture(agent, renderer)
	if err != nil {
		return err
	}
	if session != "" {
		if err := a.showSessionHistory(base, session, agent, toolMode); err != nil {
			return err
		}
	}

	reader := bufio.NewScanner(a.Stdin)
	input := newChatInput(reader, a.Stdin, a.Out, renderer)
	last := ""
	for {
		if err := ctx.Err(); err != nil {
			renderer.operation("CHAT", "", colorBlue, "Status: ended")
			return nil
		}
		message := ""
		if initialTask != "" {
			message, initialTask = initialTask, ""
			renderer.block("YOU", colorCyan, message)
		} else {
			renderer.prompt()
			line, err := input.readLine(ctx, true)
			if err != nil {
				if err == io.EOF || ctx.Err() != nil {
					renderer.operation("CHAT", "", colorBlue, "Status: ended")
					return nil
				}
				return err
			}
			message = strings.TrimSpace(line)
			renderer.submitted(isInteractiveTerminal(a.Stdin))
		}
		switch {
		case message == "/exit" || message == "/quit" || message == "\x1b":
			renderer.operation("CHAT", "", colorBlue, "Status: ended")
			return nil
		case message == "/session":
			renderer.operation("CHAT", "", colorBlue, map[bool]string{true: "Session: " + session, false: "Session: none"}[session != ""])
			continue
		case message == "/new":
			session, last = "", ""
			renderer.operation("CHAT", "", colorBlue, "Session: new")
			continue
		case message == "/sessions":
			if err := a.showSessions(base); err != nil {
				fmt.Fprintf(a.Err, "sessions: %v\n", err)
			}
			continue
		case message == "/history":
			if session == "" {
				renderer.operation("CHAT", "", colorBlue, "Session: none")
			} else if err := a.showSessionHistory(base, session, agent, toolMode); err != nil {
				fmt.Fprintf(a.Err, "history: %s\n", safeTerminal(err.Error()))
			}
			continue
		case strings.HasPrefix(message, "/tools "):
			mode := strings.TrimSpace(strings.TrimPrefix(message, "/tools "))
			if mode != "off" && mode != "summary" && mode != "verbose" {
				fmt.Fprintln(a.Out, "Usage: /tools off|summary|verbose")
			} else {
				toolMode = mode
				renderer.operation("CHAT", "", colorBlue, "Tool display: "+mode)
			}
			continue
		case message == "/govern":
			last = ""
			if err := a.prepareInteractiveMutation(); err != nil {
				renderer.operation("CHAT MUTATION", "", colorRed, "Model governance: unchanged\nError: "+err.Error())
				continue
			}
			a.guarded = false
			credential := governedResourceName("kmx-model", agent)
			renderer.operation("CHAT MUTATION", "", colorYellow, "Model governance: enabling\nCredential: "+credential+"\nTools: unchanged\nRetry history: cleared")
			if err := a.GovernInteractiveModel(agent); err != nil {
				renderer.operation("CHAT MUTATION", "", colorRed, "Model governance: change failed\nError: "+err.Error()+"\nPosture: revalidating")
				_, _ = a.refreshChatPosture(agent, renderer)
				return fmt.Errorf("model governance may have changed incompletely; chat stopped: %w", err)
			}
			refreshed, err := a.refreshChatPosture(agent, renderer)
			if err != nil {
				return fmt.Errorf("model governance changed, but refreshed posture could not be verified: %w", err)
			}
			if !refreshed.modelGoverned {
				return fmt.Errorf("model governance switch completed, but the refreshed serving posture is direct; chat stopped")
			}
			posture = refreshed
			renderer.operation("CHAT MUTATION", "", colorYellow, "Model governance: enabled\nCredential: "+credential+"\nTools: unchanged\nRetry history: cleared")
			continue
		case message == "/ungovern":
			last = ""
			if err := a.prepareInteractiveMutation(); err != nil {
				renderer.operation("CHAT MUTATION", "", colorRed, "Model governance: unchanged\nError: "+err.Error())
				continue
			}
			a.guarded = false
			renderer.operation("CHAT MUTATION", "", colorYellow, "Model governance: disabling\nTarget: keyless Ollama\nTools: unchanged\nRetry history: cleared")
			if err := a.UngovernModel(agent); err != nil {
				renderer.operation("CHAT MUTATION", "", colorRed, "Model governance: change failed\nError: "+err.Error()+"\nPosture: revalidating")
				_, _ = a.refreshChatPosture(agent, renderer)
				return fmt.Errorf("model governance may have changed incompletely; chat stopped: %w", err)
			}
			refreshed, err := a.refreshChatPosture(agent, renderer)
			if err != nil {
				return fmt.Errorf("model governance changed, but refreshed posture could not be verified: %w", err)
			}
			if refreshed.modelGoverned {
				return fmt.Errorf("model ungovern switch completed, but the refreshed serving posture is still governed; chat stopped")
			}
			posture = refreshed
			renderer.operation("CHAT MUTATION", "", colorYellow, "Model governance: disabled\nModel: ollama (direct)\nCredentials and history: retained\nTools: unchanged\nRetry history: cleared")
			continue
		case strings.HasPrefix(message, "/resume "):
			candidate := strings.TrimSpace(strings.TrimPrefix(message, "/resume "))
			if err := a.showSessionHistory(base, candidate, agent, toolMode); err != nil {
				fmt.Fprintf(a.Err, "cannot resume: %v\n", err)
				continue
			}
			session = candidate
			continue
		case message == "/retry":
			if last == "" {
				renderer.operation("CHAT", "", colorBlue, "Retry: no previous message")
				continue
			}
			message = last
		case message == "":
			continue
		case strings.HasPrefix(message, "/"):
			renderer.operation("CHAT", "", colorBlue, "Commands: "+slashCommandSummary())
			continue
		default:
			last = message
		}
		renderer.beginAssistant(agent)
		if posture.modelGoverned {
			renderer.assistantOperation(agent, "KAIMAHI ROUTE", "", colorYellow, "Seam: model proxy\nConfiguration: verified through ready plane at chat start\nPer-call decision: not exposed by kagent stream")
		}
		view, err := a.invokeStream(ctx, kagent, base, agent, message, session, toolMode, renderer, posture)
		if err != nil {
			if !isInteractiveTerminal(a.Stdin) {
				return err
			}
			fmt.Fprintf(a.Err, "chat: %v\n", err)
			continue
		}
		if view.context != "" {
			session = view.context
		}
		for view.approval != nil {
			if view.approvalErr != nil {
				return view.approvalErr
			}
			if view.approval.TaskID == "" || view.approval.ContextID == "" {
				return fmt.Errorf("HITL request is missing its task or context ID")
			}
			if len(view.approval.Calls) > 1 {
				for _, call := range view.approval.Calls {
					if call.Name == "ask_user" {
						return fmt.Errorf("ask_user arrived with other pending approvals — refusing a decision that could approve an unseen tool")
					}
				}
			}
			decision, err := a.promptHITL(ctx, input, view.approval, renderer)
			if err != nil {
				return err
			}
			view, err = a.sendHITL(ctx, base, agent, view, decision, toolMode, renderer, posture)
			if err != nil {
				return err
			}
			if view.context != "" {
				session = view.context
			}
		}
		renderer.finish()
	}
}

func (a *App) refreshChatPosture(agent string, renderer *chatRenderer) (*chatGovernancePosture, error) {
	posture := &chatGovernancePosture{governedTools: map[string]bool{}, toolRoutes: map[string]uint8{}}
	renderer.statusStart(agent)
	if err := a.showChatPosture(agent, renderer, posture); err != nil {
		renderer.statusSection("Status", "Incomplete: "+err.Error())
		renderer.statusEnd()
		return nil, err
	}
	renderer.statusEnd()
	return posture, nil
}

func (a *App) prepareInteractiveMutation() error {
	cfg, err := a.kubeconfig()
	if err != nil {
		return err
	}
	posture, err := guard.Classify(cfg, a.Cfg.KubeContext)
	if err != nil {
		return err
	}
	if !posture.Local && a.Cfg.Confirm != a.Cfg.KubeContext {
		return fmt.Errorf("in-chat mutations on context %q require KAIMAHI_CONFIRM=%s before chat starts", a.Cfg.KubeContext, a.Cfg.KubeContext)
	}
	return nil
}

func scanLine(ctx context.Context, reader lineScanner) (string, error) {
	type result struct {
		line string
		err  error
	}
	done := make(chan result, 1)
	go func() {
		if reader.Scan() {
			done <- result{line: reader.Text()}
			return
		}
		err := reader.Err()
		if err == nil {
			err = io.EOF
		}
		done <- result{err: err}
	}()
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case value := <-done:
		return value.line, value.err
	}
}

func (a *App) invokeStream(ctx context.Context, kagent, base, agent, task, session, toolMode string, renderer *chatRenderer, posture *chatGovernancePosture) (*streamView, error) {
	args := []string{"--kagent-url", base, "invoke", "--stream", "--agent", agent, "--task", task}
	if session != "" {
		args = append(args, "--session", session)
	}
	cmd := exec.CommandContext(ctx, kagent, args...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	stderr, err := os.CreateTemp("", "kmx-chat-stderr-*.log")
	if err != nil {
		return nil, err
	}
	defer func() { stderr.Close(); os.Remove(stderr.Name()) }()
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	view := newStreamView(agent, toolMode, renderer, posture)
	done := make(chan struct{})
	spinnerDone := make(chan struct{})
	started := time.Now()
	spinner := isInteractiveTerminal(a.Err)
	if spinner {
		go func() {
			defer close(spinnerDone)
			frames := []string{"|", "/", "-", "\\"}
			for i := 0; ; i++ {
				select {
				case <-done:
					return
				case <-time.After(250 * time.Millisecond):
					if !view.visible.Load() {
						renderer.spinner(agent, frames[i%len(frames)], time.Since(started))
					}
				}
			}
		}()
	} else {
		close(spinnerDone)
	}
	decodeErr := a.consumeStream(stdout, view)
	close(done)
	<-spinnerDone
	if spinner {
		renderer.clearTransient()
	}
	if decodeErr != nil && cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
	waitErr := cmd.Wait()
	_, _ = stderr.Seek(0, io.SeekStart)
	errText, _ := io.ReadAll(io.LimitReader(stderr, 64<<10))
	if decodeErr != nil {
		return nil, decodeErr
	}
	if waitErr != nil {
		return nil, fmt.Errorf("kagent invoke: %v: %s", waitErr, safeTerminal(strings.TrimSpace(string(errText))))
	}
	if view.state == "working" || view.state == "submitted" {
		if err := a.waitExistingTask(ctx, base, view); err != nil {
			return nil, err
		}
	}
	if view.state == "input-required" && view.approval != nil {
		if view.approvalErr != nil {
			return nil, view.approvalErr
		}
		return view, nil
	}
	if view.state == "input-required" {
		return nil, fmt.Errorf("task %s requires input, but its HITL request could not be decoded", view.taskID)
	}
	if view.state != "completed" || view.reply == "" {
		return nil, fmt.Errorf("task did not complete with a reply (state %q)", view.state)
	}
	return view, nil
}

func newStreamView(agent, toolMode string, renderer *chatRenderer, posture *chatGovernancePosture) *streamView {
	view := &streamView{
		agent: agent, toolCalls: map[string]string{}, messageText: map[string]string{}, toolMode: toolMode,
		renderer: renderer, governedTools: map[string]bool{}, seenToolCalls: map[string]bool{}, seenToolResponses: map[string]bool{}, ambiguousToolCalls: map[string]bool{},
	}
	if posture != nil {
		view.modelGoverned = posture.modelGoverned
		view.governedTools = posture.governedTools
	}
	return view
}

func isInteractiveTerminal(writer io.Writer) bool {
	file, ok := writer.(*os.File)
	if !ok {
		return false
	}
	info, err := file.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}

func (a *App) consumeStream(r io.Reader, view *streamView) error {
	dec := json.NewDecoder(r)
	for {
		var event streamEvent
		if err := dec.Decode(&event); err != nil {
			if err == io.EOF {
				return nil
			}
			return fmt.Errorf("invalid kagent stream: %w", err)
		}
		view.consume(event, a.Out)
	}
}

func (v *streamView) consume(event streamEvent, out io.Writer) {
	if event.ContextID != "" {
		v.context = event.ContextID
	}
	if event.TaskID != "" {
		v.taskID = event.TaskID
	}
	if len(event.Status) > 0 {
		var status struct {
			State   string `json:"state"`
			Message struct {
				Role      string `json:"role"`
				MessageID string `json:"messageId"`
				Metadata  struct {
					Partial    bool `json:"kagent_adk_partial"`
					ADKPartial bool `json:"adk_partial"`
				} `json:"metadata"`
				Parts []struct {
					Kind     string          `json:"kind"`
					Text     string          `json:"text"`
					Data     json.RawMessage `json:"data"`
					Metadata struct {
						Type           string `json:"kagent_type"`
						ADKType        string `json:"adk_type"`
						LongRunning    bool   `json:"kagent_is_long_running"`
						ADKLongRunning bool   `json:"adk_is_long_running"`
					} `json:"metadata"`
				} `json:"parts"`
			} `json:"message"`
		}
		if json.Unmarshal(event.Status, &status) == nil {
			v.state = status.State
			for _, part := range status.Message.Parts {
				if status.State == "failed" && status.Message.Role == "agent" && part.Kind == "text" && v.modelGoverned && !v.modelDenialShown {
					if reason, filed, ok := modelGovernanceDenial(part.Text); ok {
						payload := "Seam: model proxy\nSignal: response text matches a Kaimahi denial\nProvenance: unverified; kagent exposes no plane receipt\nReason: " + reason
						if filed {
							payload += "\nApproval request: reported in response text\nNext: run `make approvals` and verify the request"
						}
						if v.renderer != nil {
							v.renderer.assistantOperation(v.agent, "POSSIBLE KAIMAHI DENIAL", "", colorYellow, payload)
						}
						v.modelDenialShown = true
					}
				}
				if part.Kind == "text" && status.Message.Role == "agent" && part.Text != "" {
					previous := v.messageText[status.Message.MessageID]
					addition := part.Text
					if strings.HasPrefix(part.Text, previous) {
						addition = strings.TrimPrefix(part.Text, previous)
					}
					if previous == "" {
						v.visible.Store(true)
					}
					if v.partials != "" && strings.HasPrefix(part.Text, v.partials) {
						addition = strings.TrimPrefix(part.Text, v.partials)
					}
					if v.renderer != nil {
						v.renderer.assistant(v.agent, addition, previous == "")
					} else {
						if previous == "" {
							fmt.Fprintf(out, "%s: ", safeTerminal(v.agent))
						}
						fmt.Fprint(out, safeTerminal(addition))
					}
					v.messageText[status.Message.MessageID] = part.Text
					v.reply += addition
					if status.Message.Metadata.Partial || status.Message.Metadata.ADKPartial {
						v.partials += addition
					} else {
						v.partials = ""
					}
				}
				if part.Kind == "data" {
					kind := part.Metadata.Type
					if kind == "" {
						kind = part.Metadata.ADKType
					}
					v.consumeTool(kind, part.Metadata.LongRunning || part.Metadata.ADKLongRunning, part.Data, out)
				}
			}
		}
	}
	var artifactText strings.Builder
	for _, part := range event.Artifact.Parts {
		if part.Kind != "text" || part.Text == "" {
			continue
		}
		artifactText.WriteString(part.Text)
	}
	text := artifactText.String()
	addition := text
	if strings.HasPrefix(text, v.reply) {
		addition = strings.TrimPrefix(text, v.reply)
	} else if event.LastChunk && strings.HasSuffix(v.reply, text) {
		addition = ""
	}
	if addition != "" {
		v.visible.Store(true)
		if v.renderer != nil {
			v.renderer.assistant(v.agent, addition, v.reply == "")
		} else {
			if v.reply == "" {
				fmt.Fprintf(out, "%s: ", safeTerminal(v.agent))
			}
			fmt.Fprint(out, safeTerminal(addition))
		}
		v.reply += addition
	}
	if event.LastChunk && len(text) >= len(v.reply) {
		v.reply = text
	}
	if event.Final && v.reply != "" {
		if v.renderer == nil {
			fmt.Fprintln(out)
		}
	}
}

func (v *streamView) consumeTool(kind string, longRunning bool, raw json.RawMessage, out io.Writer) {
	if v.seenToolCalls == nil {
		v.seenToolCalls = map[string]bool{}
	}
	if v.seenToolResponses == nil {
		v.seenToolResponses = map[string]bool{}
	}
	if v.ambiguousToolCalls == nil {
		v.ambiguousToolCalls = map[string]bool{}
	}
	if v.governedTools == nil {
		v.governedTools = map[string]bool{}
	}
	var data struct {
		ID       string          `json:"id"`
		Name     string          `json:"name"`
		Args     json.RawMessage `json:"args"`
		Response struct {
			IsError bool            `json:"isError"`
			Content json.RawMessage `json:"content"`
		} `json:"response"`
	}
	if json.Unmarshal(raw, &data) != nil {
		return
	}
	if kind == "function_call" && longRunning && data.Name == "adk_request_confirmation" {
		var args struct {
			Original     hitlCall `json:"originalFunctionCall"`
			Confirmation struct {
				Hint    string `json:"hint"`
				Payload struct {
					Parts []struct {
						Original hitlCall `json:"originalFunctionCall"`
					} `json:"hitl_parts"`
				} `json:"payload"`
			} `json:"toolConfirmation"`
		}
		if json.Unmarshal(data.Args, &args) == nil {
			if v.approval == nil {
				v.approval = &hitlRequest{TaskID: v.taskID, ContextID: v.context, Hint: args.Confirmation.Hint}
			}
			if len(args.Confirmation.Payload.Parts) > 0 {
				for _, part := range args.Confirmation.Payload.Parts {
					if part.Original.ID != "" {
						v.approval.Calls = append(v.approval.Calls, part.Original)
					}
				}
			} else if args.Original.ID != "" {
				v.approval.Calls = append(v.approval.Calls, args.Original)
			} else {
				v.approvalErr = fmt.Errorf("HITL request contained a confirmation without a tool-call ID")
			}
			seen := map[string]bool{}
			for _, call := range v.approval.Calls {
				if seen[call.ID] {
					v.approvalErr = fmt.Errorf("HITL request repeated tool-call ID %q", call.ID)
				}
				seen[call.ID] = true
			}
		}
		return
	}
	if data.Name == "adk_request_confirmation" {
		return
	}
	switch kind {
	case "function_call":
		if data.ID != "" {
			if previous, exists := v.toolCalls[data.ID]; exists && previous != data.Name {
				v.ambiguousToolCalls[data.ID] = true
			} else if !exists {
				v.toolCalls[data.ID] = data.Name
			}
		}
		validID := data.ID != "" && !v.ambiguousToolCalls[data.ID]
		if validID && v.governedTools[data.Name] && v.renderer != nil && !v.seenToolCalls[data.ID] {
			v.renderer.assistantOperation(v.agent, "KAIMAHI ROUTE", "", colorYellow, "Seam: MCP gateway\nTool: "+safeTerminal(data.Name)+"\nConfiguration: verified through ready plane at chat start\nPer-call decision: not exposed by kagent stream")
		}
		if data.ID != "" {
			v.seenToolCalls[data.ID] = true
		}
		if v.toolMode != "off" {
			v.visible.Store(true)
			payload := "Status: running"
			if v.toolMode == "verbose" {
				payload += "\nArguments:\n" + indentPayload(truncatePayload(strings.TrimSpace(string(data.Args)), 16<<10))
			}
			if v.renderer != nil {
				v.renderer.assistantOperation(v.agent, "TOOL CALL", data.Name, colorMagenta, payload)
			} else {
				fmt.Fprintf(out, "Tool: %s %s\n", safeTerminal(data.Name), safeTerminal(truncatePayload(strings.TrimSpace(string(data.Args)), 16<<10)))
			}
		}
	case "function_response":
		name, correlated := v.toolCalls[data.ID]
		if name == "" {
			name = data.Name
		}
		correlated = correlated && data.ID != "" && !v.ambiguousToolCalls[data.ID] && (data.Name == "" || data.Name == name)
		state := "completed"
		if data.Response.IsError {
			state = "failed"
		}
		body := string(data.Response.Content)
		governanceDenied, requestFiled := governanceDenial(data.Response.IsError, body)
		if governanceDenied && correlated && v.governedTools[name] && !v.seenToolResponses[data.ID] {
			v.denied, v.requestFiled = true, requestFiled
			if v.renderer != nil {
				payload := "Seam: MCP gateway\nSignal: response text matches a Kaimahi denial\nProvenance: unverified; kagent exposes no plane receipt"
				if requestFiled {
					payload += "\nApproval request: reported in response text\nNext: run `make approvals` and verify the request"
				} else {
					payload += "\nApproval request: not mentioned in response text"
				}
				v.renderer.assistantOperation(v.agent, "POSSIBLE KAIMAHI DENIAL", name, colorYellow, payload)
			}
		}
		if data.ID != "" {
			v.seenToolResponses[data.ID] = true
		}
		if v.toolMode != "off" {
			v.visible.Store(true)
			payload := "Status: " + state
			if v.toolMode == "verbose" {
				payload += "\nResult:\n" + indentPayload(truncatePayload(body, 16<<10))
			}
			if v.renderer != nil {
				v.renderer.assistantOperation(v.agent, "TOOL RESULT", name, colorMagenta, payload)
			} else {
				fmt.Fprintf(out, "Tool: %s %s\n", safeTerminal(name), state)
				if v.toolMode == "verbose" {
					fmt.Fprintf(out, "  result: %s\n", safeTerminal(truncatePayload(body, 16<<10)))
				}
			}
		}
	}
}

func governanceDenial(isError bool, body string) (denied, requestFiled bool) {
	if !isError {
		return false, false
	}
	lower := strings.ToLower(body)
	filed := strings.Contains(lower, "approval request filed")
	denied = filed || strings.Contains(lower, "tool not permitted") || strings.Contains(lower, "tool call not permitted")
	return denied, filed
}

func modelGovernanceDenial(message string) (reason string, requestFiled, ok bool) {
	lower := strings.ToLower(message)
	filed := strings.Contains(lower, "approval request filed")
	for _, signature := range []string{
		"monthly token budget reached",
		"monthly budget reached",
		"metering unavailable",
		"spend ledger unavailable",
		"model has no configured price",
	} {
		if strings.Contains(lower, signature) {
			return signature, filed, true
		}
	}
	return "", false, false
}

func truncatePayload(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	return value[:limit] + "\n[truncated; use one-shot --json for full output]"
}

func (a *App) waitExistingTask(ctx context.Context, base string, view *streamView) error {
	for i := 0; i < 300; i++ {
		task, err := getTask(ctx, base, view.agent, view.taskID)
		if err != nil {
			return err
		}
		state := task.Status.State
		if state != "working" && state != "submitted" {
			status, _ := json.Marshal(task.Status)
			view.consume(streamEvent{ContextID: task.ContextID, TaskID: task.ID, Status: status}, a.Out)
			var text strings.Builder
			for _, artifact := range task.Artifacts {
				for _, part := range artifact.Parts {
					if part.Kind == "text" {
						text.WriteString(part.Text)
					}
				}
			}
			if value := text.String(); value != "" {
				addition := value
				if strings.HasPrefix(value, view.reply) {
					addition = strings.TrimPrefix(value, view.reply)
				}
				if addition != "" {
					if view.renderer != nil {
						view.renderer.assistant(view.agent, addition, view.reply == "")
						view.renderer.finish()
					} else {
						if view.reply == "" {
							fmt.Fprintf(a.Out, "%s: ", safeTerminal(view.agent))
						}
						fmt.Fprintln(a.Out, safeTerminal(addition))
					}
					view.reply += addition
				}
			}
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
		}
	}
	return fmt.Errorf("task %s remained working for 5m", view.taskID)
}

type interactiveTask struct {
	ID        string `json:"id"`
	ContextID string `json:"contextId"`
	Status    struct {
		State   string          `json:"state"`
		Message json.RawMessage `json:"message"`
	} `json:"status"`
	Artifacts []struct {
		Parts []struct{ Kind, Text string } `json:"parts"`
	} `json:"artifacts"`
}

func getTask(ctx context.Context, base, agent, id string) (*interactiveTask, error) {
	body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": "kmx", "method": "tasks/get", "params": map[string]string{"id": id}})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/api/a2a/kagent/"+url.PathEscape(agent)+"/", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-user-id", "admin@kagent.dev")
	resp, err := controllerClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("tasks/get answered HTTP %d", resp.StatusCode)
	}
	var envelope struct {
		Result interactiveTask `json:"result"`
		Error  map[string]any  `json:"error"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxControllerResponse)).Decode(&envelope); err != nil {
		return nil, err
	}
	if envelope.Error != nil {
		return nil, fmt.Errorf("tasks/get: %s", safeTerminal(fmt.Sprint(envelope.Error)))
	}
	return &envelope.Result, nil
}

func (a *App) promptHITL(ctx context.Context, input *chatInput, request *hitlRequest, renderer *chatRenderer) (map[string]any, error) {
	decisions := map[string]string{}
	reasons := map[string]string{}
	if request.Hint != "" {
		renderer.operation("NATIVE APPROVAL", "", colorYellow, "Human approval required\nHint: "+request.Hint)
	}
	for _, call := range request.Calls {
		if call.Name == "ask_user" {
			var request struct {
				Questions []askUserQuestion `json:"questions"`
			}
			if err := json.Unmarshal(call.Args, &request); err != nil || len(request.Questions) == 0 {
				return nil, fmt.Errorf("ask_user request has no valid questions")
			}
			answers := make([]map[string][]string, 0, len(request.Questions))
			for _, question := range request.Questions {
				payload := "Question: " + question.Question
				if len(question.Choices) > 0 {
					payload += "\nChoices: " + strings.Join(question.Choices, " | ")
				}
				renderer.operationPrompt("NATIVE QUESTION", colorYellow, payload, "Answer:")
				answer, err := input.readLine(ctx, false)
				if err != nil {
					return nil, err
				}
				renderer.submitted(isInteractiveTerminal(a.Stdin))
				var values []string
				for _, value := range strings.Split(answer, ",") {
					if value = strings.TrimSpace(value); value != "" {
						values = append(values, value)
					}
				}
				answers = append(answers, map[string][]string{"answer": values})
			}
			return map[string]any{"decision_type": "approve", "ask_user_answers": answers}, nil
		}
		payload := "Tool: " + safeTerminal(call.Name) + "\nArguments:\n" + indentPayload(truncatePayload(string(call.Args), 16<<10))
		renderer.operationPrompt("NATIVE APPROVAL", colorYellow, payload, "Approve? [y/N]:")
		answerLine, err := input.readLine(ctx, false)
		if err != nil {
			return nil, err
		}
		renderer.submitted(isInteractiveTerminal(a.Stdin))
		answer := strings.ToLower(strings.TrimSpace(answerLine))
		if answer == "y" || answer == "yes" {
			decisions[call.ID] = "approve"
		} else {
			decisions[call.ID] = "reject"
			renderer.operationPrompt("NATIVE APPROVAL", colorYellow, "Decision: reject", "Rejection reason (optional):")
			reason, err := input.readLine(ctx, false)
			if err != nil {
				return nil, err
			}
			renderer.submitted(isInteractiveTerminal(a.Stdin))
			if reason = strings.TrimSpace(reason); reason != "" {
				reasons[call.ID] = reason
			}
		}
	}
	if len(decisions) == 1 && len(reasons) == 0 {
		for _, decision := range decisions {
			return map[string]any{"decision_type": decision}, nil
		}
	}
	result := map[string]any{"decision_type": "batch", "decisions": decisions}
	if len(reasons) > 0 {
		result["rejection_reasons"] = reasons
	}
	return result, nil
}

func (a *App) sendHITL(ctx context.Context, base, agent string, previous *streamView, decision map[string]any, toolMode string, renderer *chatRenderer, posture *chatGovernancePosture) (*streamView, error) {
	approval := previous.approval
	if approval == nil || approval.TaskID == "" || approval.ContextID == "" {
		return nil, fmt.Errorf("HITL request is missing its task or context ID")
	}
	messageID := fmt.Sprintf("kmx-%d", time.Now().UnixNano())
	body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": messageID, "method": "message/stream", "params": map[string]any{"message": map[string]any{
		"kind": "message", "role": "user", "messageId": messageID, "taskId": approval.TaskID, "contextId": approval.ContextID,
		"parts": []map[string]any{{"kind": "data", "data": decision, "metadata": map[string]any{}}, {"kind": "text", "text": "HITL decision submitted"}},
	}}})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/api/a2a/kagent/"+url.PathEscape(agent)+"/", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("x-user-id", "admin@kagent.dev")
	client := &http.Client{Timeout: 5 * time.Minute}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HITL decision answered HTTP %d", resp.StatusCode)
	}
	view := newStreamView(agent, toolMode, renderer, posture)
	view.toolCalls = previous.toolCalls
	view.seenToolCalls = previous.seenToolCalls
	view.seenToolResponses = previous.seenToolResponses
	view.ambiguousToolCalls = previous.ambiguousToolCalls
	view.modelDenialShown = previous.modelDenialShown
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 64<<10), 4<<20)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == "" || payload == "[DONE]" {
			continue
		}
		var envelope struct {
			Result *streamEvent   `json:"result"`
			Error  map[string]any `json:"error"`
		}
		if err := json.Unmarshal([]byte(payload), &envelope); err != nil {
			return nil, fmt.Errorf("invalid HITL stream: %w", err)
		}
		if envelope.Error != nil {
			return nil, fmt.Errorf("HITL decision: %v", envelope.Error)
		}
		if envelope.Result != nil {
			view.consume(*envelope.Result, a.Out)
			continue
		}
		var event streamEvent
		if err := json.Unmarshal([]byte(payload), &event); err != nil {
			return nil, fmt.Errorf("invalid HITL event: %w", err)
		}
		view.consume(event, a.Out)
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	if view.state == "working" || view.state == "submitted" {
		if err := a.waitExistingTask(ctx, base, view); err != nil {
			return nil, err
		}
	}
	if view.state != "completed" && view.approval == nil {
		return nil, fmt.Errorf("HITL task ended in state %q", view.state)
	}
	return view, nil
}

func (a *App) showSessions(base string) error {
	resp, err := controllerRequest(context.Background(), http.MethodGet, base+"/api/sessions", nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("sessions answered HTTP %d", resp.StatusCode)
	}
	type sessionItem struct{ ID, Name, AgentID string }
	var envelope struct {
		Data json.RawMessage `json:"data"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxControllerResponse)).Decode(&envelope); err != nil {
		return err
	}
	var items []sessionItem
	if err := json.Unmarshal(envelope.Data, &items); err != nil {
		var wrapped struct {
			Sessions []sessionItem `json:"sessions"`
		}
		if err := json.Unmarshal(envelope.Data, &wrapped); err != nil {
			return fmt.Errorf("sessions returned an unknown shape")
		}
		items = wrapped.Sessions
	}
	renderer := newChatRenderer(a.Out)
	for _, item := range items {
		agent := strings.ReplaceAll(strings.TrimPrefix(item.AgentID, "kagent__NS__"), "_", "-")
		renderer.block("SESSION "+safeTerminal(item.ID), colorBlue, "Agent: "+safeTerminal(agent)+"\nName: "+safeTerminal(item.Name))
	}
	return nil
}

func (a *App) showSessionHistory(base, id, agent, toolMode string) error {
	if strings.TrimSpace(id) == "" {
		return fmt.Errorf("session ID is empty")
	}
	resp, err := controllerRequest(context.Background(), http.MethodGet, base+"/api/sessions/"+url.PathEscape(id), nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("session %q answered HTTP %d", id, resp.StatusCode)
	}
	var envelope struct {
		Data struct {
			Session struct {
				AgentID string `json:"agent_id"`
			} `json:"session"`
			Events []struct {
				CreatedAt string `json:"created_at"`
				Data      string `json:"data"`
			} `json:"events"`
		} `json:"data"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxControllerResponse)).Decode(&envelope); err != nil {
		return err
	}
	want := "kagent__NS__" + strings.ReplaceAll(agent, "-", "_")
	if envelope.Data.Session.AgentID != want {
		return fmt.Errorf("session belongs to %s, not %s", safeTerminal(envelope.Data.Session.AgentID), safeTerminal(agent))
	}
	sort.SliceStable(envelope.Data.Events, func(i, j int) bool { return envelope.Data.Events[i].CreatedAt < envelope.Data.Events[j].CreatedAt })
	renderer := newChatRenderer(a.Out)
	renderer.block("HISTORY", colorBlue, "Agent: "+safeTerminal(agent)+"\nSession: "+safeTerminal(id))
	for _, wrapper := range envelope.Data.Events {
		var event struct {
			Author  string `json:"author"`
			Content struct {
				Role  string `json:"role"`
				Parts []struct {
					Text         string `json:"text"`
					FunctionCall *struct {
						Name string
						Args json.RawMessage
					} `json:"function_call"`
					FunctionResponse *struct {
						Name     string
						Response json.RawMessage
					} `json:"function_response"`
				} `json:"parts"`
			} `json:"content"`
		}
		if json.Unmarshal([]byte(wrapper.Data), &event) != nil {
			continue
		}
		sender := event.Author
		if event.Content.Role == "user" && event.Author == "user" {
			sender = "You"
		}
		if sender == "" && event.Content.Role == "model" {
			sender = agent
		}
		for _, part := range event.Content.Parts {
			if part.Text != "" {
				if sender == "You" {
					renderer.block("YOU", colorCyan, part.Text)
				} else {
					renderer.assistant(sender, part.Text, true)
				}
			}
			if part.FunctionCall != nil {
				if toolMode == "verbose" {
					renderer.assistantOperation(agent, "TOOL CALL", part.FunctionCall.Name, colorMagenta, "Status: called\nArguments:\n"+indentPayload(truncatePayload(string(part.FunctionCall.Args), 16<<10)))
				} else if toolMode == "summary" {
					renderer.assistantOperation(agent, "TOOL CALL", part.FunctionCall.Name, colorMagenta, "Status: called")
				}
			}
			if part.FunctionResponse != nil {
				if toolMode == "verbose" {
					renderer.assistantOperation(agent, "TOOL RESULT", part.FunctionResponse.Name, colorMagenta, "Status: result\nPayload:\n"+indentPayload(truncatePayload(string(part.FunctionResponse.Response), 16<<10)))
				} else if toolMode == "summary" {
					renderer.assistantOperation(agent, "TOOL RESULT", part.FunctionResponse.Name, colorMagenta, "Status: result")
				}
			}
		}
		if event.Content.Role == "model" {
			renderer.finish()
		}
	}
	return nil
}

func (a *App) showChatPosture(agent string, renderer *chatRenderer, posture *chatGovernancePosture) error {
	raw, err := a.kubectlCapture("-n", "kagent", "get", "agent", agent, "-o", "json")
	if err != nil {
		return fmt.Errorf("cannot validate active agent: %w", err)
	}
	var resource struct {
		Metadata struct {
			Generation int64 `json:"generation"`
		} `json:"metadata"`
		Spec struct {
			Declarative struct {
				ModelConfig string `json:"modelConfig"`
				Tools       []struct {
					MCPServer struct {
						Name      string
						ToolNames []string
					} `json:"mcpServer"`
				}
			} `json:"declarative"`
		} `json:"spec"`
		Status struct {
			ObservedGeneration int64 `json:"observedGeneration"`
		} `json:"status"`
	}
	if err := json.Unmarshal([]byte(raw), &resource); err != nil {
		return fmt.Errorf("agent %q returned invalid JSON: %w", agent, err)
	}
	if resource.Metadata.Generation == 0 || resource.Status.ObservedGeneration != resource.Metadata.Generation {
		return fmt.Errorf("agent %q is still reconciling (generation %d, observed %d)", agent, resource.Metadata.Generation, resource.Status.ObservedGeneration)
	}
	if !a.agentDeploymentCurrent(agent) || !a.singleCurrentAgentPod(agent) {
		return fmt.Errorf("agent %q deployment is not fully rolled out; refusing to describe stale serving posture", agent)
	}
	model := resource.Spec.Declarative.ModelConfig
	modelRaw, err := a.kubectlCapture("-n", "kagent", "get", "modelconfig", model, "-o", "json")
	if err != nil {
		return fmt.Errorf("cannot validate ModelConfig %q: %w", model, err)
	}
	var modelResource struct {
		Metadata struct {
			Generation int64 `json:"generation"`
		} `json:"metadata"`
		Spec   map[string]any `json:"spec"`
		Status struct {
			ObservedGeneration int64             `json:"observedGeneration"`
			Conditions         []serverCondition `json:"conditions"`
		} `json:"status"`
	}
	if err := json.Unmarshal([]byte(modelRaw), &modelResource); err != nil {
		return fmt.Errorf("ModelConfig %q returned invalid JSON: %w", model, err)
	}
	accepted := false
	for _, condition := range modelResource.Status.Conditions {
		if condition.Type == "Accepted" && condition.Status == "True" &&
			(condition.ObservedGeneration == 0 || condition.ObservedGeneration == modelResource.Metadata.Generation) {
			accepted = true
		}
	}
	if modelResource.Status.ObservedGeneration != modelResource.Metadata.Generation || !accepted {
		return fmt.Errorf("ModelConfig %q is not currently Accepted", model)
	}
	modelPosture := "direct, not Kaimahi-governed"
	direct := true
	if usesKaimahiModelProxy(modelResource.Spec) {
		if !a.planeReady("kaimahi-proxy") {
			return fmt.Errorf("agent uses %q but the Kaimahi plane is not Ready", model)
		}
		modelPosture = "governed by Kaimahi; plane Ready"
		posture.modelGoverned = true
		direct = false
	}
	renderer.statusSection("Model", "Name: "+safeTerminal(model)+"\nPosture: "+modelPosture)
	if len(resource.Spec.Declarative.Tools) == 0 {
		renderer.statusSection("Tools", "None")
		if direct {
			renderer.statusSection("Warning", "Model is direct. Kaimahi budgets and spend ledger do not apply.")
		}
		return nil
	}
	for _, tool := range resource.Spec.Declarative.Tools {
		serverRaw, err := a.kubectlCapture("-n", "kagent", "get", "remotemcpserver", tool.MCPServer.Name, "-o", "json")
		if err != nil {
			return fmt.Errorf("cannot validate RemoteMCPServer %q: %w", tool.MCPServer.Name, err)
		}
		var server struct {
			Metadata struct {
				Generation int64 `json:"generation"`
			} `json:"metadata"`
			Spec struct {
				URL string `json:"url"`
			} `json:"spec"`
			Status struct {
				ObservedGeneration int64                                `json:"observedGeneration"`
				Conditions         []serverCondition                    `json:"conditions"`
				Discovered         []struct{ Name, Description string } `json:"discoveredTools"`
			} `json:"status"`
		}
		if err := json.Unmarshal([]byte(serverRaw), &server); err != nil {
			return fmt.Errorf("RemoteMCPServer %q returned invalid JSON: %w", tool.MCPServer.Name, err)
		}
		discovered := map[string]string{}
		for _, item := range server.Status.Discovered {
			discovered[item.Name] = item.Description
		}
		serverPosture := "direct, not Kaimahi-governed"
		governedServer := false
		parsed, _ := url.Parse(server.Spec.URL)
		gatewayHost := parsed != nil && (parsed.Host == "kaimahi-mcp-gateway.kaimahi:8081" || parsed.Host == "kaimahi-mcp-gateway.kaimahi.svc.cluster.local:8081")
		if parsed != nil && seamScheme(parsed.Scheme) && gatewayHost && strings.HasPrefix(parsed.Path, "/upstream/") {
			if !a.planeReady("kaimahi-mcp-gateway") {
				return fmt.Errorf("RemoteMCPServer %q points at Kaimahi but the plane is not Ready", tool.MCPServer.Name)
			}
			serverPosture = "governed by Kaimahi; plane Ready"
			governedServer = true
		} else {
			direct = true
		}
		selected := append([]string(nil), tool.MCPServer.ToolNames...)
		if len(selected) == 0 {
			for name := range discovered {
				selected = append(selected, name)
			}
			sort.Strings(selected)
		}
		wiring := &scaffold.ToolWiring{Server: tool.MCPServer.Name, Tools: selected}
		discoveredSet := map[string]bool{}
		for name := range discovered {
			discoveredSet[name] = true
		}
		if err := validateToolServer(wiring, server.Metadata.Generation, server.Status.ObservedGeneration, server.Status.Conditions, discoveredSet); err != nil {
			return err
		}
		for _, name := range selected {
			if governedServer {
				posture.toolRoutes[name] |= 1
			} else {
				posture.toolRoutes[name] |= 2
			}
			posture.governedTools[name] = posture.toolRoutes[name] == 1
		}
		var details strings.Builder
		fmt.Fprintf(&details, "Server: %s\nPosture: %s\nAllowed:", safeTerminal(tool.MCPServer.Name), serverPosture)
		for _, name := range selected {
			if description, ok := discovered[name]; ok {
				fmt.Fprintf(&details, "\n  - %s - %s", safeTerminal(name), safeTerminal(description))
			} else {
				fmt.Fprintf(&details, "\n  - %s - selected but not currently discovered", safeTerminal(name))
			}
		}
		renderer.statusSection("Tools", details.String())
	}
	if direct {
		renderer.statusSection("Warning", "One or more seams are direct. Kaimahi budgets, gateway policy, or audit may not apply.")
	}
	return nil
}

func (a *App) agentDeploymentCurrent(agent string) bool {
	raw, err := a.kubectlCapture("-n", "kagent", "get", "deployment", agent, "-o", "json")
	if err != nil {
		return false
	}
	var deployment struct {
		Metadata struct {
			Generation int64 `json:"generation"`
		} `json:"metadata"`
		Spec struct {
			Replicas int32 `json:"replicas"`
		} `json:"spec"`
		Status struct {
			ObservedGeneration  int64 `json:"observedGeneration"`
			UpdatedReplicas     int32 `json:"updatedReplicas"`
			ReadyReplicas       int32 `json:"readyReplicas"`
			AvailableReplicas   int32 `json:"availableReplicas"`
			UnavailableReplicas int32 `json:"unavailableReplicas"`
		} `json:"status"`
	}
	return json.Unmarshal([]byte(raw), &deployment) == nil && deployment.Spec.Replicas > 0 &&
		deployment.Status.ObservedGeneration == deployment.Metadata.Generation &&
		deployment.Status.UpdatedReplicas == deployment.Spec.Replicas &&
		deployment.Status.ReadyReplicas == deployment.Spec.Replicas &&
		deployment.Status.AvailableReplicas == deployment.Spec.Replicas &&
		deployment.Status.UnavailableReplicas == 0
}

func (a *App) singleCurrentAgentPod(agent string) bool {
	revision, err := a.deploymentRevision(agent)
	if err != nil || revision == "" {
		return false
	}
	hash, err := a.templateHash(agent, revision)
	if err != nil || hash == "" {
		return false
	}
	pods, err := a.kubectlCapture("-n", config_kagentNamespace, "get", "pods", "-l", "kagent="+agent,
		"-o", `jsonpath={range .items[*]}{.metadata.labels.pod-template-hash}{"\n"}{end}`)
	return err == nil && strings.TrimSpace(pods) == hash
}

func (a *App) planeReady(service string) bool {
	raw, err := a.kubectlCapture("-n", "kaimahi", "get", "deployment", "kaimahi-proxy", "-o", "json")
	if err != nil {
		return false
	}
	var deployment struct {
		Spec struct {
			Replicas int32 `json:"replicas"`
		} `json:"spec"`
		Status struct {
			ReadyReplicas int32 `json:"readyReplicas"`
		} `json:"status"`
	}
	if json.Unmarshal([]byte(raw), &deployment) != nil || deployment.Spec.Replicas == 0 || deployment.Status.ReadyReplicas != deployment.Spec.Replicas {
		return false
	}
	endpoints, err := a.kubectlCapture("-n", "kaimahi", "get", "endpoints", service, "-o", "json")
	if err != nil {
		return false
	}
	var ready struct {
		Subsets []struct {
			Addresses []json.RawMessage `json:"addresses"`
		} `json:"subsets"`
	}
	if json.Unmarshal([]byte(endpoints), &ready) != nil {
		return false
	}
	for _, subset := range ready.Subsets {
		if len(subset.Addresses) > 0 {
			return true
		}
	}
	return false
}

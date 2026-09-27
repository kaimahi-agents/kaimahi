package app

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/cliui"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/guard"
	"golang.org/x/term"
)

var agentNameRE = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`)

type actorColor int

const (
	colorBlue actorColor = iota
	colorCyan
	colorGreen
	colorMagenta
	colorYellow
	colorRed
)

type chatRenderer struct {
	timeline                   func(chatTimelineEvent)
	slashCommands              []slashCommand
	out                        io.Writer
	mu                         sync.Mutex
	color                      bool
	ui                         cliui.Output
	cursor                     bool
	openActor                  string
	actorLine                  bool
	promptOpen                 bool
	promptText                 string
	promptIndent               int
	promptKind                 cliui.FocusKind
	promptHint                 string
	commandSummary             string
	alternateScreen            bool
	transient                  bool
	transientWidth             int
	spinnerDisabled            bool
	spinnerPaused              bool
	pendingGap                 bool
	verbose                    bool
	stickyHeader               bool
	headerRows                 int
	headerAgent, headerContext string
	headerFields               []cliui.Field
	headerCollecting           bool
}

func (r *chatRenderer) enterFullScreen() {
	if r == nil || !r.cursor || r.alternateScreen {
		return
	}
	fmt.Fprint(r.out, "\x1b[?1049h\x1b[H\x1b[2J")
	r.alternateScreen = true
}

func (r *chatRenderer) leaveFullScreen() {
	if r == nil || !r.alternateScreen {
		return
	}
	r.finish()
	if r.headerRows > 0 {
		fmt.Fprint(r.out, "\x1b[r")
		r.headerRows = 0
	}
	fmt.Fprint(r.out, "\x1b[?1049l")
	r.alternateScreen = false
}

func newChatRenderer(out io.Writer) *chatRenderer {
	terminal := isInteractiveTerminal(out) && os.Getenv("TERM") != "dumb"
	plain := os.Getenv("NO_COLOR") != ""
	return &chatRenderer{out: out, ui: cliui.New(out), color: terminal && !plain, cursor: terminal && !plain, commandSummary: slashCommandSummary()}
}

func (r *chatRenderer) label(text string, color actorColor) string {
	text = strings.Join(strings.Fields(safeTerminal(text)), " ")
	if !r.color {
		return text
	}
	ui := r.ui
	if !ui.Rich() {
		ui = cliui.WithCapabilities(cliui.Capabilities{Rich: true, Color: true})
	}
	switch color {
	case colorBlue:
		return ui.Info(text)
	case colorCyan:
		return ui.Heading(text)
	case colorGreen:
		return ui.Success(text)
	case colorMagenta:
		return ui.Accent(text)
	case colorYellow:
		return ui.Warning(text)
	case colorRed:
		return ui.Failure(text)
	}
	return text
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
	if r.cursor && r.transient {
		if file, ok := r.out.(*os.File); ok {
			if width, _, err := term.GetSize(int(file.Fd())); err == nil && width != r.transientWidth {
				// Reflow makes the old row unsafe to erase. Leave it durable.
				fmt.Fprintln(r.out)
				r.transient = false
				r.spinnerDisabled = true
				return
			}
		}
		fmt.Fprint(r.out, "\r\033[2K")
		r.transient = false
	}
}

func (r *chatRenderer) closeLocked() {
	if r.promptOpen {
		fmt.Fprintln(r.out)
		r.promptOpen = false
		r.pendingGap = r.ui.Rich()
	}
	if r.pendingGap {
		fmt.Fprintln(r.out)
		r.pendingGap = false
	}
	if r.openActor != "" {
		if r.actorLine {
			fmt.Fprintln(r.out)
		}
		if r.actorLine || !r.ui.Rich() {
			fmt.Fprintln(r.out)
		}
		r.openActor = ""
		r.actorLine = false
	}
}

func (r *chatRenderer) block(label string, color actorColor, payload string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.timeline != nil {
		r.timeline(chatTimelineEvent{kind: "operation", label: label, text: payload})
		return
	}
	r.clearLocked()
	r.closeLocked()
	if label == "YOU" && r.ui.Rich() && r.ui.Width() >= 16 {
		fmt.Fprintln(r.out, strings.Join(r.ui.UserMessage(safeTerminal(payload), r.ui.Width()-1), "\n"))
		fmt.Fprintln(r.out)
		return
	}
	fmt.Fprintf(r.out, "%s\n%s\n\n", r.label(label, color), indentPayload(payload))
}

func (r *chatRenderer) statusStart(agent, kubeContext string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.timeline != nil {
		r.timeline(chatTimelineEvent{kind: "status", agent: agent, text: kubeContext})
		return
	}
	r.clearLocked()
	r.closeLocked()
	if r.stickyHeader && r.alternateScreen {
		r.headerAgent, r.headerContext = agent, kubeContext
		r.headerFields = nil
		r.headerCollecting = true
		return
	}
	if r.ui.Rich() {
		fmt.Fprintln(r.out, r.ui.Heading(r.wrap("KMX  /  INTERACTIVE CHAT", 0)))
		fmt.Fprintln(r.out)
		title := strings.Join(strings.Fields(safeTerminal(agent)), " ")
		title = r.wrap(title, 0)
		if r.color {
			title = lipgloss.NewStyle().Bold(true).Render(title)
		}
		fmt.Fprintln(r.out, title)
		if kubeContext != "" {
			context := "Context: " + strings.Join(strings.Fields(safeTerminal(kubeContext)), " ")
			fmt.Fprintln(r.out, r.ui.Muted(r.wrap(context, 0)))
		}
		return
	}
	fmt.Fprintf(r.out, "CHAT STATUS\n------------\n  Agent: %s\n", safeTerminal(agent))
	if kubeContext != "" {
		fmt.Fprintf(r.out, "  Context: %s\n", strings.Join(strings.Fields(safeTerminal(kubeContext)), " "))
	}
	commands := r.commandSummary
	if commands == "" {
		commands = slashCommandSummary()
	}
	fmt.Fprintf(r.out, "  Commands: %s\n", commands)
}

func (r *chatRenderer) wrap(text string, indent int) string {
	if r.ui.Width() <= 0 {
		return text
	}
	return ansi.Hardwrap(text, max(1, r.ui.Width()-indent), true)
}

func (r *chatRenderer) statusSection(label, payload string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.timeline != nil {
		return
	}
	label = strings.Join(strings.Fields(safeTerminal(label)), " ")
	payload = strings.TrimSuffix(safeTerminal(payload), "\n")
	if r.stickyHeader && r.alternateScreen {
		r.updateHeaderFieldLocked(label, payload)
		if !r.headerCollecting {
			r.drawStickyHeaderLocked()
		}
		return
	}
	if r.ui.Rich() {
		fmt.Fprintln(r.out, r.ui.Fields([]cliui.Field{{Label: label, Value: payload}}))
		return
	}
	fmt.Fprintf(r.out, "  %s\n", label)
	if payload != "" {
		fmt.Fprintf(r.out, "    %s\n", strings.ReplaceAll(payload, "\n", "\n    "))
	}
}

func (r *chatRenderer) statusEnd() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.timeline != nil {
		return
	}
	if r.stickyHeader && r.alternateScreen {
		r.headerCollecting = false
		r.drawStickyHeaderLocked()
		return
	}
	if r.ui.Rich() {
		fmt.Fprintln(r.out, r.wrap("Type a message. /help for commands; /exit to leave", 0))
	} else {
		fmt.Fprintln(r.out, "------------------------------------------------------------")
	}
	fmt.Fprintln(r.out)
}

func (r *chatRenderer) exit(reason string) {
	if !r.ui.Rich() {
		r.operation("CHAT", "", colorBlue, "Status: ended")
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.clearLocked()
	r.closeLocked()
	fmt.Fprintln(r.out, r.ui.Muted("Chat ended ("+reason+")."))
}

// Durable feedback is safe while commands or resumed streams may also write.
func (r *chatRenderer) working(message string) {
	if r == nil || !r.ui.Rich() || !r.verboseEnabled() {
		return
	}
	r.operation("WORKING", "", colorBlue, message)
}

func (r *chatRenderer) operation(kind, subject string, color actorColor, payload string) {
	if (kind == "WORKING" || kind == "TIMING") && !r.verboseEnabled() {
		return
	}
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
	r.spinnerPaused = true
	r.closeLocked()
	r.promptText = strings.Join(strings.Fields(safeTerminal(prompt)), " ") + " "
	r.promptIndent = 2
	r.promptKind = cliui.FocusQuestion
	if strings.Contains(strings.ToLower(kind), "approval") {
		r.promptKind = cliui.FocusApproval
	}
	if r.ui.Rich() {
		fmt.Fprintf(r.out, "%s\n%s\n", r.label("["+kind+"]", color), indentPayload(payload))
		if !r.cursor || !isTerminal(r.out) {
			fmt.Fprintf(r.out, "  %s", r.promptText)
		}
	} else {
		fmt.Fprintf(r.out, "%s\n%s\n  %s", r.label("["+kind+"]", color), indentPayload(payload), r.promptText)
	}
	r.promptOpen = true
}

func (r *chatRenderer) beginAssistant(agent string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.timeline != nil {
		if r.openActor != agent {
			r.timeline(chatTimelineEvent{kind: "agent", agent: agent})
			r.openActor = agent
		}
		return
	}
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
	if (kind == "WORKING" || kind == "TIMING") && !r.verbose {
		return
	}
	if r.timeline != nil {
		if r.openActor != agent {
			r.timeline(chatTimelineEvent{kind: "agent", agent: agent})
			r.openActor = agent
		}
		if subject != "" {
			payload = "Tool: " + subject + "\n" + payload
		}
		r.timeline(chatTimelineEvent{kind: "agent-operation", agent: agent, label: kind, text: payload})
		return
	}
	r.clearLocked()
	if r.openActor != agent {
		r.closeLocked()
		fmt.Fprintln(r.out, r.label("AGENT ("+safeTerminal(agent)+")", colorGreen))
		r.openActor = agent
	}
	if r.actorLine {
		fmt.Fprintln(r.out)
		if r.ui.Rich() {
			fmt.Fprintln(r.out)
		}
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
	if r.timeline != nil {
		if r.openActor != agent {
			r.timeline(chatTimelineEvent{kind: "agent", agent: agent})
			r.openActor = agent
		}
		r.timeline(chatTimelineEvent{kind: "assistant", agent: agent, text: text, start: start})
		return
	}
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
		if r.ui.Rich() {
			fmt.Fprint(r.out, "  ")
		} else {
			fmt.Fprint(r.out, "  | ")
		}
		r.actorLine = true
	}
	if r.ui.Rich() {
		fmt.Fprint(r.out, strings.ReplaceAll(safeTerminal(text), "\n", "\n  "))
	} else {
		fmt.Fprint(r.out, assistantPayload(text))
	}
}

func (r *chatRenderer) clearTransient() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.clearLocked()
}

func (r *chatRenderer) pauseSpinner(paused bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.spinnerPaused = paused
	if paused {
		r.clearLocked()
	}
}

func (r *chatRenderer) prompt() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.clearLocked()
	r.closeLocked()
	r.promptText, r.promptIndent, r.promptKind = r.label("YOU >", colorCyan)+" ", 0, cliui.FocusMessage
	if !r.cursor || !isTerminal(r.out) {
		fmt.Fprint(r.out, r.promptText)
	}
	r.promptOpen = true
}

type interactiveChatBackend interface {
	Agent() string
	Connect(context.Context, *chatRenderer) ([]cliui.Field, error)
	Send(context.Context, string, *chatRenderer) error
}

func (a *App) runInteractiveChatBackend(backend interactiveChatBackend) error {
	return a.runInteractiveChatBackendInitial(backend, "")
}

func (a *App) runInteractiveChatBackendInitial(backend interactiveChatBackend, initial string) error {
	if isInteractiveTerminal(a.Stdin) && isInteractiveTerminal(a.Out) && os.Getenv("TERM") != "dumb" && os.Getenv("NO_COLOR") == "" {
		return a.runChatTimeline(backend, initial)
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()
	renderer := newChatRenderer(a.Out)
	renderer.verbose = a.chatVerbose
	renderer.stickyHeader = true
	if closer, ok := backend.(interface{ Close() }); ok {
		defer closer.Close()
	}
	if !newChatInput(nil, a.Stdin, a.Out, renderer).enhanced {
		renderer.ui = cliui.WithCapabilities(cliui.Capabilities{})
		renderer.cursor = false
	}
	renderer.enterFullScreen()
	defer renderer.leaveFullScreen()
	// Entering the alternate screen can itself change terminal dimensions.
	// Establish the first prompt's baseline only after that transition settles.
	if renderer.alternateScreen {
		if err := waitForStableTerminalSizeContext(ctx, a.Stdin, a.Out); err != nil {
			if errors.Is(err, context.Canceled) {
				return nil
			}
			return err
		}
	}
	renderer.promptHint = "/help  /retry  /exit"
	renderer.slashCommands = chatBackendCommands(backend)
	renderer.commandSummary = chatCommandsSummary(renderer.slashCommands)
	renderer.working("Connecting to " + backend.Agent())
	fields, err := backend.Connect(ctx, renderer)
	if err != nil {
		return err
	}
	for _, field := range fields {
		renderer.statusSection(field.Label, field.Value)
	}
	renderer.statusEnd()
	input := newChatInput(bufio.NewScanner(a.Stdin), a.Stdin, a.Out, renderer)
	last := ""
	for {
		line := ""
		var err error
		if initial != "" {
			line, initial = initial, ""
			renderer.block("YOU", colorCyan, line)
		} else {
			renderer.prompt()
			line, err = input.readLine(ctx, true)
		}
		if err != nil {
			if err == io.EOF || errors.Is(err, context.Canceled) || ctx.Err() != nil {
				reason := "end of input"
				if errors.Is(err, context.Canceled) || ctx.Err() != nil {
					reason = "cancelled"
				}
				renderer.exit(reason)
				return nil
			}
			return err
		}
		message := strings.TrimSpace(line)
		renderer.submitted(isInteractiveTerminal(a.Stdin))
		commandKey := message
		if containsChatCommand(renderer.slashCommands, message) && !containsChatCommand(commonChatCommands(), message) {
			commandKey = "runtime-command"
		}
		switch commandKey {
		case "", "\x1b":
			if message == "\x1b" {
				renderer.exit("exit requested")
				return nil
			}
			continue
		case "/exit", "/quit":
			renderer.exit("exit requested")
			return nil
		case "/help":
			renderer.operation("CHAT HELP", "", colorBlue, chatCommandsHelp(renderer.slashCommands))
			continue
		case "runtime-command":
			controls, ok := backend.(configurableChatBackend)
			if !ok {
				renderer.operation("CHAT", "", colorBlue, "Agent configuration is unavailable for this backend.")
				continue
			}
			renderer.suspendStickyHeader()
			reset, err := controls.Configure(ctx, message, renderer)
			if errors.Is(err, context.Canceled) || ctx.Err() != nil {
				renderer.exit("cancelled")
				return nil
			}
			if err != nil {
				if renderer.stickyHeader {
					renderer.mu.Lock()
					renderer.drawStickyHeaderLocked()
					renderer.mu.Unlock()
				}
				renderer.operation("CHAT", "", colorRed, safeTerminal(err.Error()))
				continue
			}
			if reset {
				last = ""
			}
			{
				// Tool-save feedback may already have repainted the sticky header.
				// Reset its row ownership before clearing/home; otherwise the next
				// header redraw restores the cursor to row 1 and input overwrites it.
				renderer.suspendStickyHeader()
				if renderer.alternateScreen {
					fmt.Fprint(a.Out, "\x1b[H\x1b[2J")
				}
				fields, err := backend.Connect(ctx, renderer)
				if err != nil {
					return err
				}
				for _, field := range fields {
					renderer.statusSection(field.Label, field.Value)
				}
				renderer.statusEnd()
				renderer.slashCommands = chatBackendCommands(backend)
			}
			continue
		case "/verbose-on", "/verbose-off":
			renderer.setVerbose(message == "/verbose-on")
			continue
		case "/retry":
			if last == "" {
				renderer.operation("CHAT", "", colorBlue, "Retry: no previous message")
				continue
			}
			message = last
		default:
			if strings.HasPrefix(message, "/") {
				renderer.operation("CHAT", "", colorBlue, "Unknown command. Use /help for commands.")
				continue
			}
			last = message
		}
		restoreEcho := quietChatWait(a.Stdin)
		err = sendInteractiveChatMessage(ctx, backend, message, renderer)
		restoreEcho()
		if err != nil {
			if ctx.Err() != nil {
				renderer.exit("cancelled")
				return nil
			}
			renderer.operation("CHAT", "", colorRed, "Request failed: "+safeTerminal(err.Error()))
		}
		renderer.finish()
	}
}

func sendInteractiveChatMessage(ctx context.Context, backend interactiveChatBackend, message string, renderer *chatRenderer) error {
	done := make(chan struct{})
	spinnerDone := make(chan struct{})
	started := time.Now()
	spinner := renderer != nil && renderer.cursor
	if spinner {
		renderer.pauseSpinner(false)
		go func() {
			defer close(spinnerDone)
			frames := []string{"|", "/", "-", "\\"}
			for i := 0; ; i++ {
				select {
				case <-done:
					return
				case <-time.After(250 * time.Millisecond):
					renderer.spinner(backend.Agent(), frames[i%len(frames)], time.Since(started))
				}
			}
		}()
	} else {
		close(spinnerDone)
	}
	err := backend.Send(ctx, message, renderer)
	close(done)
	<-spinnerDone
	if spinner {
		renderer.clearTransient()
	}
	if err == nil && renderer != nil {
		renderer.responseTime(time.Since(started))
	}
	return err
}

// responseTime records end-to-end latency beneath a completed reply, not an
// inference estimate. Failed and cancelled requests never get a success footer.
func (r *chatRenderer) responseTime(elapsed time.Duration) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.timeline != nil {
		r.timeline(chatTimelineEvent{kind: "timing", text: "Responded in " + formatElapsed(elapsed) + " · total request time"})
		return
	}
	r.clearLocked()
	if r.actorLine {
		fmt.Fprintln(r.out)
	}
	duration := elapsed.Round(time.Millisecond).String()
	if elapsed >= time.Second {
		duration = elapsed.Round(100 * time.Millisecond).String()
	}
	text := r.wrap("Responded in "+duration+" · total request time", 2)
	if r.ui.Rich() {
		text = r.ui.Muted(text)
	}
	fmt.Fprintln(r.out, "  "+strings.ReplaceAll(text, "\n", "\n  "))
	fmt.Fprintln(r.out)
	r.openActor, r.actorLine, r.pendingGap = "", false, false
}

func (r *chatRenderer) submitted(inputWasTerminal bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.promptOpen && (!inputWasTerminal || !isTerminal(r.out)) {
		fmt.Fprintln(r.out)
	}
	if r.promptOpen && r.ui.Rich() {
		r.pendingGap = true
	}
	r.promptOpen = false
}

func (r *chatRenderer) spinner(agent, frame string, elapsed time.Duration) {
	if !r.cursor {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.actorLine || r.promptOpen || r.spinnerDisabled || r.spinnerPaused {
		return
	}
	r.clearLocked()
	if r.spinnerDisabled {
		return
	}
	label := "RESPONDING"
	if r.verbose {
		label = "WORKING"
	}
	text := fmt.Sprintf("%s %s %ds", r.label(label, colorBlue), strings.Join(strings.Fields(safeTerminal(agent)+" "+safeTerminal(frame)), " "), int(elapsed.Seconds()))
	// Keep the transient on one physical row so clearing it cannot erase history.
	width := r.ui.Width()
	if file, ok := r.out.(*os.File); ok {
		if current, _, err := term.GetSize(int(file.Fd())); err == nil {
			width = current
		}
	}
	if width <= 0 {
		width = 80
	}
	fmt.Fprint(r.out, ansi.Truncate(text, max(0, width-1), ""))
	r.transient = true
	r.transientWidth = width
}

func (r *chatRenderer) finish() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.clearLocked()
	r.closeLocked()
}

func (r *chatRenderer) verboseEnabled() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.verbose
}

func (r *chatRenderer) setVerbose(enabled bool) {
	r.mu.Lock()
	r.clearLocked()
	r.verbose = enabled
	r.mu.Unlock()
	state := "off"
	if enabled {
		state = "on"
	}
	r.operation("CHAT", "", colorBlue, "Verbose: "+state)
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

// isTerminal reports whether w is a terminal, not merely a character device.
// Anything that is not an *os.File — a test buffer, a pipe wrapper — is
// treated as not a terminal, which is the safe default.
func isTerminal(w io.Writer) bool {
	f, ok := w.(*os.File)
	if !ok || f == nil {
		return false
	}
	return term.IsTerminal(int(f.Fd()))
}

func isInteractiveTerminal(writer io.Writer) bool {
	return isTerminal(writer)
}

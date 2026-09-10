package app

import (
	"fmt"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/config"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/kagentcli"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/run"
)

// The controller's transport failures, and which of them may be retried.
//
// The predicate is anchored to the controller's WHOLE error line, not to
// transport text anywhere in the output. The output being matched is the
// combined stdout+stderr, and stdout carries the A2A task JSON — including
// the model's own reply. An agent asked to explain one of these errors (the
// FAQ documents them) would echo the words and, with a loose match, trigger a
// second invoke: duplicate spend, and for tool calls a burned grant. kagent
// prints the failure as one line, "Error invoking session: <wrapped error>",
// ending in Go's net error; both ends are anchored so a line that starts with
// `{` can never match.
//
// Two classes, because they are not equally safe to retry:
//
//	REFUSED   kube-proxy REJECTed; nothing reached the agent. Always safe.
//	AMBIGUOUS EOF / connection reset: the request may have reached the agent
//	          and been acted on before the connection dropped.
//
// `chat` retries both — re-asking a question is acceptable. A caller whose
// turn can ACT retries only the refused class, because re-invoking after an
// ambiguous drop can repeat something the agent already did.
//
// Which class applies is the caller's to state: askAgent takes the matcher
// rather than choosing one. `chat` and `quickstart` pass ChatRetryable;
// `kmx workflow run` passes ChatRetryableSafe for its bounded and
// consequential steps, and ChatRetryable for the turns that only read and
// draft.
//
// The step that made this matter is a BOUNDED one. A consequential step's
// grant is USES-bounded, so a repeated call is refused and the run fails
// rather than doing it twice; a bounded step has no use count — a standing
// constraint admits repeatedly — so a retried build pipeline really would
// run again. Failing the step is the safe direction: a stopped release is
// recoverable and a doubled one is not.
const (
	chatErrorLine = `^Error invoking session: .*failed to send HTTP request: Post "[^"]*": `
	chatRefused   = `dial tcp [^ ]*: connect: connection refused`
	chatAmbiguous = `EOF|(read|write) tcp [^ ]*: (read|write): connection reset by peer`
)

// ChatRetryable matches the transport failures `chat` retries.
var ChatRetryable = regexp.MustCompile(`(?m)` + chatErrorLine + `(` + chatRefused + `|` + chatAmbiguous + `)$`)

// ChatRetryableSafe matches only the failures where nothing reached the
// agent. Kept beside its sibling because the distinction is the point: it is
// what a caller whose turn can act on the world must use.
var ChatRetryableSafe = regexp.MustCompile(`(?m)` + chatErrorLine + `(` + chatRefused + `)$`)

// ChatOptions selects one-shot or session-preserving chat behavior.
type ChatOptions struct {
	Agent       string
	Task        string
	Interactive bool
	Session     string
}

// ChatJSON forces the raw A2A task even when a terminal is attached.
// Piped output is raw regardless — see chatview.go.
func (a *App) ChatJSON(v bool) { a.chatJSON = v }

// Chat asks one question of an agent through the kagent CLI.
//
// Unguarded, like the Makefile's `chat`. Calling it "read-only" would be
// wrong — it spends budget, writes a ledger row, and can burn a grant. It is
// unguarded because the line being drawn is not "mutates" but "can be aimed
// somewhere unintended": chat runs through kubectl carrying an explicit
// --context, so it lands wherever the rest of the invocation was already
// going to land. Prompting on the most-used command would buy nothing and
// teach people to type past confirmations.
func (a *App) Chat(agent, task string) error {
	return a.ChatWithOptions(ChatOptions{Agent: agent, Task: task})
}

// ChatWithOptions asks one question or starts an interactive session.
func (a *App) ChatWithOptions(opt ChatOptions) error {
	agent, task := opt.Agent, opt.Task
	if agent == "" {
		agent = config.DefaultAgent
	}
	if task == "" && !opt.Interactive {
		task = config.DefaultTask
	}
	if err := a.preflight(depKubectl); err != nil {
		return err
	}

	out, status, err := a.askAgent(agent, task, opt.Session, opt.Interactive, ChatRetryable)
	if err != nil || opt.Interactive {
		return err
	}

	// A terminal gets the readable view; a pipe gets the bytes, because CI
	// and scripts/verify-chat.py parse them. Rendering is best-effort: if
	// this is not a task we recognise, print what kagent printed.
	if status == 0 && !a.chatJSON && isTerminal(a.Out) && renderChat(a.Out, out) {
		return nil
	}
	fmt.Fprint(a.Out, out)
	if status != 0 {
		return fmt.Errorf("kagent invoke exited %d", status)
	}
	return nil
}

// waitServable proves the agent is actually SERVABLE before invoking — it
// does not infer it.
//
// `make use` already waits (kagent reconcile, rollout status, the single-pod
// wait; then the Agent's Ready condition) and none of it is sufficient during
// a preset-switch rollout: CI failed here twice with
//
//	dial tcp <clusterIP>:8080: connect: connection refused
//
// because at the moment of the call the Service had no ready backend (the old
// pod removed, the new one not yet propagated) — kube-proxy REJECTs that, so
// it looks like a broken agent rather than a race. Checking the endpoint list
// is also too weak: it can read ready one instant and be empty the next.
//
// So the check is the same thing the caller needs: fetch the agent's own A2A
// card THROUGH the Service, via the API server's service proxy. That resolves
// endpoints server-side and returns a real HTTP body, so it fails while there
// is no ready backend and succeeds only once the agent answers. (The kagent
// readiness probe uses the same path.) One kubectl call — no extra port, no
// second forward. This replaced a `sleep 3`; padding is not a readiness
// check.
func (a *App) waitServable(agent string) error {
	if err := a.ensureAgentExists(agent); err != nil {
		return err
	}
	path := fmt.Sprintf("/api/v1/namespaces/kagent/services/%s:8080/proxy/.well-known/agent-card.json", agent)
	// 120 tries a second apart — `for _ in $(seq 1 120); do … sleep 1; done`.
	ok := run.Poll(120, time.Second, func() bool {
		return a.kubectlQuiet("-n", "kagent", "get", "--raw", path)
	})
	if !ok {
		return fmt.Errorf("agent %q is not answering through its Service after 120s — refusing to invoke\n"+
			"  (invoking now would fail with a transport error from the controller)", agent)
	}
	return nil
}

func (a *App) ensureAgentExists(agent string) error {
	if _, err := a.kubectlCapture("-n", "kagent", "get", "agents.kagent.dev", agent, "-o", "name"); err != nil {
		if !isNotFound(err) {
			return fmt.Errorf("cannot verify agent %q before chat: %w", agent, err)
		}
		available, listErr := a.kubectlCapture("-n", "kagent", "get", "agents.kagent.dev", "-o", "name")
		if listErr != nil {
			return fmt.Errorf("agent %q does not exist in namespace kagent", agent)
		}
		var names []string
		for _, line := range strings.Split(available, "\n") {
			if _, name, ok := strings.Cut(strings.TrimSpace(line), "/"); ok && name != "" {
				names = append(names, name)
			}
		}
		if len(names) == 0 {
			return fmt.Errorf("agent %q does not exist in namespace kagent; no agents are installed", agent)
		}
		return fmt.Errorf("agent %q does not exist in namespace kagent; available agents: %s", agent, strings.Join(names, ", "))
	}
	return nil
}

// portForward opens the controller forward and WAITS for it, returning a stop
// function.
//
// The old shell form was `port-forward ... >/dev/null 2>&1 & sleep 3`, and an
// invoke that trusted the CLI's default localhost:8083. If the bind failed
// because ANOTHER cluster's forward already held the port, the error went to
// /dev/null and `kagent invoke` connected to that forward instead — returning
// a real, plausible reply from the WRONG cluster. It does not fail closed:
// the controller on that forward answers happily, and --context cannot
// protect this path, because the aiming happens at the socket, not at
// kubectl. So: wait for kubectl's own "Forwarding from" line, and refuse if
// it never appears.
func (a *App) portForward() (string, func(), error) {
	log, err := os.CreateTemp("", "kmx-port-forward-*.log")
	if err != nil {
		return "", nil, err
	}
	logPath := log.Name()
	log.Close()

	sink, err := os.OpenFile(logPath, os.O_WRONLY, 0o600)
	if err != nil {
		os.Remove(logPath)
		return "", nil, err
	}

	forward := *a.Run
	forward.Echo = false
	portSpec := a.Cfg.ChatPort + ":8083"
	if a.Cfg.ChatPort == "auto" {
		portSpec = ":8083"
	}
	cmd := forward.Command("kubectl", a.kubectl("-n", "kagent", "port-forward",
		"--address", "127.0.0.1", "svc/kagent-controller", portSpec)...)
	cmd.Stdout, cmd.Stderr = sink, sink
	if err := cmd.Start(); err != nil {
		sink.Close()
		os.Remove(logPath)
		return "", nil, err
	}
	// Waited on in the background so the readiness loop can tell "not up
	// yet" from "already dead" — the shell's `kill -0 $pf || break`.
	done := make(chan struct{})
	go func() {
		_ = cmd.Wait()
		close(done)
	}()
	stopped := false
	stop := func() {
		if stopped {
			return
		}
		stopped = true
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		<-done
		sink.Close()
		os.Remove(logPath)
	}

	want := "Forwarding from 127.0.0.1:"
	if a.Cfg.ChatPort != "auto" {
		want += a.Cfg.ChatPort
	}
	// 80 tries a quarter-second apart — `for _ in $(seq 1 80); do … sleep 0.25`.
	run.Poll(80, 250*time.Millisecond, func() bool {
		body, _ := os.ReadFile(logPath)
		if strings.Contains(string(body), want) {
			return true
		}
		select {
		case <-done:
			return true // it exited; the check below reports the failure
		default:
			return false
		}
	})
	body, _ := os.ReadFile(logPath)
	if !strings.Contains(string(body), want) {
		stop()
		return "", nil, fmt.Errorf("port-forward to kagent-controller never came up on 127.0.0.1:%s:\n  %s\n"+
			"  Refusing to invoke: if another cluster's forward holds this port,\n"+
			"  the task would have run THERE. Use CHAT_PORT=<free port>.",
			a.Cfg.ChatPort, string(body))
	}
	port, err := forwardedPort(string(body))
	if err != nil {
		stop()
		return "", nil, err
	}
	return port, stop, nil
}

var forwardingLine = regexp.MustCompile(`(?m)^Forwarding from 127\.0\.0\.1:([0-9]+) -> 8083$`)

func forwardedPort(output string) (string, error) {
	match := forwardingLine.FindStringSubmatch(output)
	if len(match) != 2 {
		return "", fmt.Errorf("kubectl did not report the selected loopback port")
	}
	return match[1], nil
}

// askAgent runs one non-interactive invoke and returns kagent's combined
// output and exit status. It is the body `chat` always had; `quickstart`
// needs the same bytes to report the answer it just proved, and a second
// invoke path would be a second set of retry rules to keep in step.
//
// The interactive flag is handled here rather than by the caller so that the
// kagent CLI is fetched once, in one place, whichever mode is asked for.
//
// retryable is the CALLER's transport-retry policy, because only the caller
// knows whether repeating this task is safe: ChatRetryable for a question,
// ChatRetryableSafe for anything whose turn can act on the world. Passing it
// in rather than choosing here is what keeps the two classes from drifting
// back together — a new caller has to state which it is.
func (a *App) askAgent(agent, task, session string, interactive bool, retryable *regexp.Regexp) (string, int, error) {
	cache, err := config.CacheDir()
	if err != nil {
		return "", 0, err
	}
	kagent, err := kagentcli.Ensure(kagentcli.Options{
		Version:  a.Cfg.KagentVersion,
		CacheDir: cache,
		Existing: a.Cfg.KagentBin,
		Log:      a.Err,
	})
	if err != nil {
		return "", 0, err
	}
	if interactive {
		return "", 0, a.interactiveChat(kagent, agent, task, session)
	}

	if err := a.waitServable(agent); err != nil {
		return "", 0, err
	}

	port, stop, err := a.portForward()
	if err != nil {
		return "", 0, err
	}
	defer stop()

	url := "http://127.0.0.1:" + port
	// The CLI defaults to localhost:8083; name the port we actually opened
	// so it can never fall back to someone else's.
	args := []string{"--kagent-url", url, "invoke", "--agent", agent, "--task", task}
	if session != "" {
		args = append(args, "--session", session)
	}
	fmt.Fprintf(a.Err, "%s %s\n", kagent, "invoke --agent "+agent)

	var out string
	var status int
	// Two reasons to invoke again, kept apart because they are different
	// failures: the controller could not reach the agent (a race, retried
	// after a pause), and the agent asked a question nothing here can answer
	// (a model sample, re-asked immediately). Neither can turn a failure into
	// a success — the last output is returned unchanged either way.
	transportRetries, questions := 0, 0
	for {
		out, status, err = a.Run.CaptureCombined(kagent, args...)
		if err != nil {
			return "", 0, err
		}
		if retryable.MatchString(out) {
			if transportRetries == 3 {
				break
			}
			transportRetries++
			a.notef("kagent could not reach agent %q yet (transport error); retry %d/3 in 5s", agent, transportRetries)
			time.Sleep(5 * time.Second)
			continue
		}
		if !chatShouldResample(session, out) {
			break
		}
		if questions == chatQuestionResamples {
			a.notef("agent %q asked a question on all %d attempts; reporting the task as it is — "+
				"answer it yourself with `kmx agent chat --interactive %s`",
				agent, chatQuestionResamples+1, agent)
			break
		}
		questions++
		a.notef("agent %q asked the user a question instead of answering, and nothing here can answer it; "+
			"chat re-sample %d of %d", agent, questions, chatQuestionResamples)
	}
	return out, status, nil
}

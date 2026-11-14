package app

import (
	"io"
	"strings"
	"testing"
)

func TestRunAgentRemoteTypedConfirmationKeepsFullTargetDisclosure(t *testing.T) {
	f := runFixture(t)
	t.Setenv("KMX_EVAL_REMOTE", "1")
	f.answers["private prompt"] = "remote answer"
	master, slave := chatPTY(t, 120)
	f.app.Stdin, f.app.Err = slave, slave

	done := make(chan error, 1)
	go func() { done <- f.app.RunAgent(runOption(f)) }()
	var transcript strings.Builder
	chatPTYReadUntil(t, master, &transcript, func(text string) bool {
		return strings.Contains(text, "Type the context name to continue (anything else aborts): ")
	})
	before := transcript.String()
	for _, want := range []string{
		"about to:", "context:  kind-test", "chosen by: --context",
		"server:   managed.example.invalid", "namespace(s): orka-system",
		"posture:  REMOTE / non-kind",
	} {
		if !strings.Contains(before, want) {
			t.Errorf("full disclosure missing %q: %s", want, before)
		}
	}
	if strings.Contains(before, "Run target") || f.taskCreates(t) != 0 {
		t.Fatalf("unconfirmed Run compacted or created a Task: creates=%d transcript=%q", f.taskCreates(t), before)
	}
	if _, err := io.WriteString(master, "kind-test\n"); err != nil {
		t.Fatal(err)
	}
	chatPTYReadUntil(t, master, &transcript, func(text string) bool {
		return strings.Contains(text, "Recovery:")
	})
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if f.out.String() != "remote answer\n" || f.taskCreates(t) != 1 {
		t.Fatalf("stdout=%q creates=%d", f.out, f.taskCreates(t))
	}
	after := transcript.String()
	if !strings.Contains(after, "kube-guard: confirmed.") ||
		!strings.Contains(after, "authority details:") ||
		strings.Contains(after, "private prompt") || strings.Contains(after, "remote answer") {
		t.Fatalf("typed confirmation diagnostics changed or exposed content: %q", after)
	}
}

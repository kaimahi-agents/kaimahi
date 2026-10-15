package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/guard"
)

func TestWorkspaceLiftSourceEditDuringTerminalConsent(t *testing.T) {
	a, opt, state, _, _, writes := workspaceLiftFixture(t)
	p, err := PrepareWorkspaceLift(t.Context(), opt)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	master, slave := chatPTY(t, 200)
	a.Stdin = slave
	a.Err = slave
	a.Cfg.Confirm = ""
	done := make(chan error, 1)
	go func() { _, err := a.LiftWorkspace(t.Context(), p); done <- err }()
	var captured strings.Builder
	chatPTYReadUntil(t, master, &captured, func(s string) bool { return strings.Contains(s, "type kind-test: ") })
	if strings.Contains(captured.String(), "\n") || !strings.Contains(captured.String(), "namespace=orka-system") || !strings.Contains(captured.String(), "server=127.0.0.1") {
		t.Fatal("Plan/prompt not one concrete-context line", captured.String())
	}
	writeFile(t, filepath.Join(opt.Workspace, "eval/example.yaml"), "id: changed\ninput: changed\nexpectContains: [changed]\n")
	io.WriteString(master, "kind-test\n")
	select {
	case err := <-done:
		if err == nil || len(state.creates) != 0 || *writes != 0 {
			t.Fatal("source edit retained consent", err)
		}
	case <-time.After(time.Second):
		t.Fatal("consent did not finish")
	}
}

func TestWorkspaceLiftJSONConsentShowsConcretePlanBeforePrompt(t *testing.T) {
	a, opt, state, _, _, writes := workspaceLiftFixture(t)
	opt.JSON = true
	p, err := PrepareWorkspaceLift(t.Context(), opt)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	master, slave := chatPTY(t, 200)
	var stdout bytes.Buffer
	a.Out, a.Err, a.Stdin, a.Cfg.Confirm = &stdout, slave, slave, ""
	done := make(chan error, 1)
	go func() { _, err := a.LiftWorkspace(t.Context(), p); done <- err }()
	var captured strings.Builder
	chatPTYReadUntil(t, master, &captured, func(s string) bool { return strings.Contains(s, "type kind-test: ") })
	_, _ = io.WriteString(master, "decline\n")
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("incorrect challenge accepted")
		}
	case <-time.After(time.Second):
		t.Fatal("JSON consent did not finish")
	}
	before := captured.String()
	if !strings.HasPrefix(before, "Plan · ") || strings.Count(before, "namespace=orka-system") != 2 || strings.Count(before, "server=127.0.0.1") != 2 || strings.Index(before, "Plan · ") > strings.Index(before, "Confirm ") {
		t.Fatalf("concrete plan missing before JSON consent: %q", before)
	}
	var result WorkspaceLiftResult
	decoder := json.NewDecoder(&stdout)
	if decoder.Decode(&result) != nil || decoder.Decode(&result) != io.EOF || len(state.creates) != 0 || *writes != 0 {
		t.Fatal("JSON result/effect contract changed", stdout.String())
	}
}

func TestWorkspaceLiftTerminalCombinedConsentAndCancellation(t *testing.T) {
	for _, cancelled := range []bool{false, true} {
		t.Run(map[bool]string{false: "one prompt", true: "cancel blocked input"}[cancelled], func(t *testing.T) {
			cfg, err := guard.ParseKubeconfig([]byte(`{"clusters":[{"name":"remote","cluster":{"server":"https://cluster.example.invalid"}}],"contexts":[{"name":"dev-remote","context":{"cluster":"remote"}},{"name":"prod-remote","context":{"cluster":"remote"}}]}`))
			if err != nil {
				t.Fatal(err)
			}
			master, slave := chatPTY(t, 160)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan error, 1)
			go func() {
				done <- guard.ConfirmPipeline(ctx, cfg, []string{"dev-remote", "prod-remote", "dev-remote"}, "", "eval,push,lift; source-only promotion", slave, slave)
			}()
			var captured strings.Builder
			chatPTYReadUntil(t, master, &captured, func(s string) bool { return strings.Contains(s, "type dev-remote,prod-remote: ") })
			if cancelled {
				cancel()
			} else {
				io.WriteString(master, "dev-remote,prod-remote\n")
			}
			select {
			case err := <-done:
				if cancelled && !errors.Is(err, context.Canceled) || !cancelled && err != nil {
					t.Fatal(err)
				}
			case <-time.After(250 * time.Millisecond):
				io.WriteString(master, "\n")
				<-done
				t.Fatal("terminal confirmation ignored cancellation")
			}
			if strings.Count(captured.String(), "type dev-remote,prod-remote:") != 1 {
				t.Fatal("not one combined question", captured.String())
			}
		})
	}
}

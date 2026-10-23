package mcp

import (
	"context"
	"os/exec"
	"time"
)

// limitedSquadOutput drains every write, but retains at most 32KiB per stream.
// It never exposes child content in results, errors, logs or MCP stdout.
type limitedSquadOutput struct {
	data     []byte
	overflow bool
}

func (w *limitedSquadOutput) Write(p []byte) (int, error) {
	n := len(p)
	remaining := (32 << 10) - len(w.data)
	if len(p) > remaining {
		w.overflow = true
		p = p[:remaining]
	}
	w.data = append(w.data, p...)
	return n, nil
}

func (c *SquadCreator) cast(ctx context.Context, m SquadMember) (bool, error) {
	return c.invoke(ctx, []string{"cast", "--name", m.ID, "--role", m.Role})
}

// argv is formed only by the two fixed internal init/cast call sites.
func (c *SquadCreator) invoke(ctx context.Context, argv []string) (bool, error) {
	if err := c.checkProgram(); err != nil {
		return false, err
	}
	bounded, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(bounded, c.program, argv...)
	cmd.Dir = c.root.Name()
	cmd.Env = squadEnvironment()
	if c.allowSetup {
		cmd.Env = append(cmd.Env, c.setupEnvironment()...)
	}
	// Nil stdin is closed/non-TTY. No parent-added shell or arbitrary arguments;
	// the qualified installed launcher may itself use its native interpreter.
	var stdout, stderr limitedSquadOutput
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	cmd.WaitDelay = time.Second
	err := cmd.Run()
	if err != nil {
		primarySucceeded := cmd.ProcessState != nil && cmd.ProcessState.Success()
		if bounded.Err() != nil {
			return primarySucceeded, cancelled(bounded)
		}
		if primarySucceeded {
			// ErrWaitDelay can mean a descendant still holds output pipes. Do
			// not call that a nonzero primary exit or enrich on uncertain IO.
			return true, refusal("squad_child_io_unknown")
		}
		if argv[0] == "init" {
			return false, refusal("squad_setup_failed")
		}
		return false, refusal("squad_cast_failed")
	}
	if stdout.overflow || stderr.overflow {
		return true, refusal("squad_output_limit")
	}
	return true, nil
}

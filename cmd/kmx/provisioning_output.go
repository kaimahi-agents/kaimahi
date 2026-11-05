package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/google/uuid"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/app"
	"github.com/spf13/cobra"
)

type provisioningSelection struct {
	mode           string
	stages         []string
	coarse         bool
	deferPreflight bool
	operationID    *string
	model          *app.OrkaModelResult
}
type provisioningOutput struct {
	mode                         string
	effects                      []app.MachineEffect
	entered, preflight, prepared bool
	stage                        string
	model                        *app.OrkaModelResult
}

func upSelection(opt app.UpOptions) provisioningSelection {
	stages := []string{"cluster", "ollama", "model", "orka"}
	if opt.Step != "" {
		stages = nil
		switch opt.Step {
		case "cluster", "ollama", "model", "orka":
			stages = []string{opt.Step}
		}
	}
	return provisioningSelection{mode: "apply", stages: stages}
}
func installSelection(opt app.OrkaOptions) provisioningSelection {
	s := provisioningSelection{mode: "apply", stages: []string{"installer", "runtime", "result_reader", "provider"}}
	if opt.Provider == "-" {
		s.stages = s.stages[:3]
	}
	if opt.NoApply {
		s.mode, s.stages = "no_apply", nil
	} else if opt.DryRun {
		s.mode, s.stages = "dry_run", s.stages[:2]
	}
	if opt.OfflineBundle != "" {
		s.coarse = true
		s.stages = []string{"runtime"}
	}
	return s
}

func newProvisioningOutput(out io.Writer, operation string, parent context.Context) *machineOutput {
	m := newMachineOutput(out, operation)
	signals, stop := signal.NotifyContext(parent, os.Interrupt, syscall.SIGTERM)
	m.ctx, m.cancel = context.WithCancel(signals)
	m.stop = func() { m.cancel(); stop() }
	m.setup = &provisioningOutput{mode: "apply", effects: []app.MachineEffect{}}
	return m
}
func (m *machineOutput) selectSetup(s provisioningSelection) {
	m.setup.mode = s.mode
	m.setup.model = s.model
	for _, stage := range s.stages {
		m.setup.effects = append(m.setup.effects, app.MachineEffect{Stage: stage, Attempt: "not_started", Mutation: "none"})
	}
}
func (m *machineOutput) beginPreflight() error {
	if m.setup.preflight {
		return m.err
	}
	m.setup.preflight = true
	m.setup.stage = "preflight"
	return m.event("preflight", "active")
}
func (m *machineOutput) endPreflight(status string) error {
	m.setup.prepared = true
	err := m.event("preflight", status)
	if status == "done" {
		m.setup.stage = ""
	}
	return err
}
func (m *machineOutput) markEffect(stage, status string) {
	index := -1
	for i := range m.setup.effects {
		if m.setup.effects[i].Stage == stage {
			index = i
			break
		}
	}
	if index < 0 {
		m.setup.effects = append(m.setup.effects, app.MachineEffect{Stage: stage, Attempt: "not_started", Mutation: "none"})
		index = len(m.setup.effects) - 1
	}
	e := &m.setup.effects[index]
	if status == "active" {
		e.Attempt = "started"
		if m.setup.mode == "apply" && stage != "installer" && !(strings.HasPrefix(m.operation, "cluster.orka.model.") && (stage == "model" || stage == "finalize")) {
			e.Mutation = "possible"
		}
	} else if status == "done" {
		e.Attempt = "completed"
	}
}
func (m *machineOutput) observe(label, status string, cause error) {
	if m.err != nil {
		return
	}
	stage := app.MachineProvisioningStage(m.operation, label)
	if stage == "preflight" && strings.HasPrefix(m.operation, "cluster.orka.model.") {
		if status == "active" {
			_ = m.beginPreflight()
		} else if !m.setup.prepared {
			if m.ctx.Err() != nil || errors.Is(cause, context.Canceled) {
				status = "cancelled"
			}
			_ = m.endPreflight(status)
		}
		return
	}
	m.setup.stage = stage
	m.markEffect(stage, status)
	if status != "active" && (m.ctx.Err() != nil || errors.Is(cause, context.Canceled)) {
		status = "cancelled"
	}
	_ = m.event(stage, status)
	if status == "done" {
		m.setup.stage = ""
	}
}

func (m *machineOutput) finishSetup(err error) error {
	if m.err != nil {
		return &machineCommandError{cause: m.err, safe: app.NewMachineError("output_failed", m.setup.stage)}
	}
	if err == nil && !m.setup.entered && !m.setup.preflight {
		return nil
	} // successful help/completion
	if err == nil {
		err = m.ctx.Err()
	}
	code, outcome := "operation_failed", "failed"
	if !m.setup.entered {
		code = "invalid_arguments"
	}
	var modelError *app.OrkaModelError
	if errors.As(err, &modelError) {
		code = modelError.Code()
	}
	var reported *machineCommandError
	if errors.As(err, &reported) && reported.code != "" {
		code = reported.code
	}
	if errors.Is(err, errWindowsOperationUnsupported) {
		code = "unsupported"
	}
	switch {
	case errors.Is(err, context.DeadlineExceeded), errors.Is(m.ctx.Err(), context.DeadlineExceeded):
		code = "deadline_exceeded"
	case errors.Is(err, context.Canceled), errors.Is(m.ctx.Err(), context.Canceled):
		code, outcome = "cancelled", "cancelled"
	case err == nil:
		outcome = "complete"
	}
	var safe *app.MachineError
	if err != nil {
		safe = app.NewMachineError(code, m.setup.stage)
	}
	if !m.setup.preflight {
		if e := m.beginPreflight(); e != nil {
			return &machineCommandError{cause: e, safe: app.NewMachineError("output_failed", "")}
		}
	}
	if !m.setup.prepared {
		status := "failed"
		if outcome == "cancelled" {
			status = "cancelled"
		}
		if e := m.endPreflight(status); e != nil {
			return &machineCommandError{cause: e, safe: app.NewMachineError("output_failed", "")}
		}
	}
	exit := 0
	if err != nil {
		exit = exitCode(err)
	}
	if e := m.setupResult(m.setup.mode, m.setup.effects, outcome, exit, safe); e != nil {
		return &machineCommandError{cause: e, safe: app.NewMachineError("output_failed", "")}
	}
	if err != nil {
		return &machineCommandError{cause: err, safe: safe}
	}
	return nil
}

func bindProvisioningOutput(cmd *cobra.Command, state *commandState, operation string, selection func() provisioningSelection, handler func(*app.App) error) {
	var output string
	cmd.Flags().StringVarP(&output, "output", "o", "text", "Output: text or jsonl")
	_ = cmd.RegisterFlagCompletionFunc("output", staticCompletion([]string{"text", "jsonl"}))
	args := cmd.Args
	cmd.Args = func(cmd *cobra.Command, values []string) error {
		if output != "text" && output != "jsonl" {
			return fmt.Errorf("unsupported output format %q", output)
		}
		if args != nil {
			return args(cmd, values)
		}
		return nil
	}
	cmd.RunE = func(cmd *cobra.Command, _ []string) error {
		if output != "jsonl" {
			if id := selection().operationID; id != nil {
				*id = uuid.NewString()
			}
			return appRun(state, handler)(cmd, nil)
		}
		m := state.machine
		own := m == nil
		if own {
			m = newProvisioningOutput(state.deps.stdout, operation, cmd.Context())
			defer m.stop()
			cmd.SetContext(m.ctx)
		}
		run := func() error {
			m.setup.entered = true
			selected := selection()
			m.selectSetup(selected)
			if selected.operationID != nil {
				*selected.operationID = m.id
			}
			if err := m.beginPreflight(); err != nil {
				return err
			}
			a, err := state.operationApplication(cmd)
			if err != nil {
				return &machineCommandError{cause: err, safe: app.NewMachineError("configuration_invalid", "preflight"), code: "configuration_invalid"}
			}
			a = a.WithProvisioningProgress(m.observe)
			runner := *a.Run
			runner.Stdout, runner.Stderr, runner.Echo = io.Discard, io.Discard, false
			a.Run = &runner
			a.Out, a.Err, a.Stdin = io.Discard, io.Discard, nil
			if err := m.ctx.Err(); err != nil {
				return err
			}
			if !selected.deferPreflight {
				if err := m.endPreflight("done"); err != nil {
					return err
				}
			}
			s := selection()
			if s.coarse {
				m.markEffect("runtime", "active")
			}
			err = handler(a)
			if s.coarse && err == nil && m.ctx.Err() == nil {
				m.markEffect("runtime", "done")
			}
			return err
		}
		err := run()
		if own {
			return m.finishSetup(err)
		}
		return err
	}
}

// Only the supported leaf routes can select setup framing. Known operands
// stay opaque even when they look like selectors or command names.
func provisioningJSONInvocation(argv []string) string {
	route := []string{}
	operation, output, count := "", "", 0
	for i := 0; i < len(argv); i++ {
		arg := argv[i]
		name, value, inline := strings.Cut(arg, "=")
		if name == "--" {
			break
		}
		switch name {
		case "--context", "-context", "--container-engine":
			if !inline {
				i++
			}
			continue
		case "--help", "-h":
			continue
		}
		if operation == "" {
			route = append(route, arg)
			switch strings.Join(route, " ") {
			case "cluster", "cluster orka", "cluster orka model":
				continue
			case "cluster up":
				operation = "cluster.up"
			case "cluster orka model connect":
				operation = "cluster.orka.model.connect"
			case "cluster orka model disconnect":
				operation = "cluster.orka.model.disconnect"
			case "cluster orka install":
				operation = "cluster.orka.install"
			default:
				return ""
			}
			continue
		}
		switch {
		case name == "--namespace" || name == "-n" || name == "--connection" || name == "--credential-generation" || name == "--route" || name == "--credential-mount" || name == "--step" || name == "--offline-bundle" || name == "--provider" || name == "--model" || name == "--model-url":
			if !inline {
				i++
			}
		case name == "--output" || arg == "-o":
			if !inline {
				i++
				if i >= len(argv) {
					return ""
				}
				value = argv[i]
			}
			output, count = value, count+1
		case strings.HasPrefix(arg, "-o") && len(arg) > 2:
			output, count = arg[2:], count+1
		}
	}
	if count == 1 && output == "jsonl" {
		return operation
	}
	return ""
}

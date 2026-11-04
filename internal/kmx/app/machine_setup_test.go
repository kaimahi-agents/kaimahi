package app

import (
	"context"
	"errors"
	"io"
	"testing"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/run"
)

func TestMachineSetupPhaseMapping(t *testing.T) {
	for _, row := range [][3]string{
		{"cluster.up", "Prepare kind cluster", "cluster"}, {"cluster.up", "Deploy Ollama", "ollama"},
		{"cluster.up", "Pull model", "model"}, {"cluster.up", "Install Orka", "orka"},
		{"cluster.orka.install", "Fetch the pinned installer", "installer"},
		{"cluster.orka.install", "Validate chart CRDs", "runtime"},
		{"cluster.orka.install", "Apply chart CRDs and install Orka", "runtime"},
		{"cluster.orka.install", "Provision Task result reader", "result_reader"},
		{"cluster.orka.install", "Wire a keyless Provider", "provider"},
		{"cluster.up", "private unexpected label", "other"},
	} {
		if got := MachineProvisioningStage(row[0], row[1]); got != row[2] {
			t.Fatalf("stage=%q want %q", got, row[2])
		}
	}
}

func TestMachinePhaseCancellation(t *testing.T) {
	for _, at := range []string{"before", "start", "body"} {
		t.Run(at, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			original := &App{Run: &run.Runner{Context: ctx}, Err: io.Discard}
			notifications, calls := 0, 0
			a := original.WithProvisioningProgress(func(_, _ string, _ error) {
				notifications++
				if at == "start" {
					cancel()
				}
			})
			if at == "before" {
				cancel()
			}
			err := a.runPhase(phase{name: "phase"}, func() error {
				calls++
				if at == "body" {
					cancel()
				}
				return nil
			})
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("cancel lost: %v", err)
			}
			if at != "body" && calls != 0 {
				t.Fatal("entered cancelled body")
			}
			if at == "before" && notifications != 0 {
				t.Fatal("started cancelled phase")
			}
			if original.operationProgress != nil {
				t.Fatal("mutated original App")
			}
		})
	}
}

func TestMachineTextPhaseUnchanged(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	a := &App{Run: &run.Runner{Context: ctx}, Err: io.Discard}
	calls := 0
	if err := a.runPhase(phase{name: "text"}, func() error { calls++; return nil }); err != nil || calls != 1 {
		t.Fatal("changed legacy context behavior", err)
	}
}

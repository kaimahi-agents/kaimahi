package main

import (
	"strings"
	"testing"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/app"
	"github.com/spf13/cobra"
)

func TestOrkaSetupFlagsRetainEnrollmentTriState(t *testing.T) {
	for _, setting := range []string{"omitted", "true", "false"} {
		t.Run(setting, func(t *testing.T) {
			cmd := &cobra.Command{}
			var opt app.OrkaSetupOptions
			addOrkaSetupFlags(cmd, &opt)
			if setting != "omitted" {
				if err := cmd.Flags().Set("enforcement", setting); err != nil {
					t.Fatal(err)
				}
			}
			got := selectedOrkaSetup(cmd, opt)
			if setting == "omitted" {
				if got.Enforcement != nil {
					t.Fatal("omitted enrollment became an explicit disable")
				}
			} else if got.Enforcement == nil || *got.Enforcement != (setting == "true") {
				t.Fatal("explicit enrollment selection changed")
			}
		})
	}
}

func TestOrkaSetupCommandsValidateBeforeConfiguration(t *testing.T) {
	for _, command := range []string{"up", "fixed", "guided"} {
		for _, flags := range [][]string{
			{"--orka-chart", "candidate.tgz"},
			{"--orka-chart-sha256", strings.Repeat("a", 64)},
			{"--enforcement=false"},
			{"--orka-chart", "candidate.tgz", "--orka-chart-sha256", "invalid"},
			{"--orka-chart", "candidate.tgz", "--orka-chart-sha256", strings.Repeat("A", 64)},
		} {
			t.Run(command+"/"+strings.Join(flags, " "), func(t *testing.T) {
				state := &commandState{}
				cmd := newQuickstartCommand(state)
				if command == "up" {
					cmd = newUpCommand(state)
				} else if command == "guided" {
					flags = append(append([]string{}, flags...), "--interactive")
				}
				if err := cmd.ParseFlags(flags); err != nil {
					t.Fatal(err)
				}
				if err := cmd.Args(cmd, nil); err == nil {
					t.Fatal("invalid setup options passed argument validation")
				}
			})
		}
	}
}

func TestOrkaSetupRejectsFlagsOnUnrelatedLocalStep(t *testing.T) {
	cmd := newUpCommand(&commandState{})
	if err := cmd.ParseFlags([]string{"--step", "model", "--orka-chart", "candidate.tgz",
		"--orka-chart-sha256", strings.Repeat("a", 64)}); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Args(cmd, nil); err == nil {
		t.Fatal("model-only step ignored exact installer selection")
	}
}

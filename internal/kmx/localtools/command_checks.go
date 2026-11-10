package localtools

import (
	"context"
	"os"
)

type CommandCheckResult struct {
	Tool  string `json:"tool"`
	Check string `json:"check"`
	State string `json:"state"`
}

// RunCommandChecks executes binding-owned activation probes with the same
// frozen executable, directory, environment, output bounds and process cleanup
// as ordinary calls. Raw probe output is never returned to the caller.
func RunCommandChecks(ctx context.Context, plan CommandPlan) error {
	_, err := CheckCommandPlan(ctx, plan)
	return err
}

// CheckCommandPlan returns only safe identities and pass states. Probe output
// remains broker-private even on mismatch.
func CheckCommandPlan(ctx context.Context, plan CommandPlan) ([]CommandCheckResult, error) {
	if _, err := OpenCommandPlan(plan); err != nil {
		return nil, ErrCommand
	}
	var observations []CommandCheckResult
	for _, input := range plan.Commands {
		cfg := input
		commandInfo, commandErr := os.Stat(cfg.Command)
		dirInfo, dirErr := os.Stat(cfg.Dir)
		if commandErr != nil || dirErr != nil {
			return nil, ErrCommand
		}
		cfg.commandInfo, cfg.dirInfo = commandInfo, dirInfo
		checkContent, err := validateCommandConfig(cfg)
		if err != nil {
			return nil, ErrCommand
		}
		for _, check := range cfg.Checks {
			if checkContent(check.Args...) != nil {
				return nil, ErrCommand
			}
			probe := cfg
			probe.Args, probe.ArgumentPlan, probe.Parameters, probe.Checks = append([]string(nil), check.Args...), nil, nil, nil
			probeCtx, cancel := context.WithTimeout(ctx, commandCheckTimeout(check))
			result, runErr := runCommand(probeCtx, probe, checkContent)
			cancel()
			if runErr != nil || checkCommandObservation(check, result) != nil {
				return nil, ErrCommand
			}
			observations = append(observations, CommandCheckResult{Tool: cfg.Name, Check: check.Name, State: "passed"})
		}
	}
	return observations, nil
}

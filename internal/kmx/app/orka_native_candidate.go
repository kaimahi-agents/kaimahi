package app

import (
	"context"
	"fmt"
)

func validateOrkaNativeChartSelection(chart, digest string) error {
	if chart == "" && digest == "" {
		return nil
	}
	if chart == "" || !verificationDigest(digest) {
		return fmt.Errorf("native Orka candidate requires --orka-chart and --orka-chart-sha256 with an exact SHA256")
	}
	return nil
}

func (a *App) selectOrkaNativeCandidate(ctx context.Context, chart, digest, namespace string) error {
	a.orkaNativeCandidate = nil
	if err := validateOrkaNativeChartSelection(chart, digest); err != nil {
		return err
	}
	if chart == "" && a.orkaSetup == nil {
		return nil
	}
	worker := a.withRunContext(ctx)
	helm := depHelm
	helm.fetchable = false
	if err := worker.preflight(helm); err != nil {
		return err
	}
	var spec orkaInstallSpec
	if chart == "" {
		spec = a.orkaSetup.spec
	} else {
		var err error
		_, spec, err = worker.loadOrkaInstaller(OrkaOptions{Chart: chart, ChartSHA256: digest})
		if err != nil {
			return err
		}
	}
	if spec.taskNamespace != namespace {
		return fmt.Errorf("native Orka candidate Task namespace does not match the selected destination")
	}
	known, err := worker.orkaInstallStateFor(spec)
	if err != nil {
		return err
	}
	if !known {
		return fmt.Errorf("native Orka candidate requires the exact already deployed kmx-owned chart")
	}
	a.orkaNativeCandidate = &spec
	return nil
}

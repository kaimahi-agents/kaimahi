package app

// OrkaSetupOptions select the same verified installer and explicit enrollment
// contract as orka install. Omission preserves ordinary setup behavior.
type OrkaSetupOptions struct {
	Chart, ChartSHA256 string
	Enforcement        *bool
}

type preparedOrkaSetup struct {
	chart []byte
	spec  orkaInstallSpec
}

func (a *App) prepareOrkaSetup(opt OrkaSetupOptions) error {
	a.orkaSetup, a.orkaNativeCandidate = nil, nil
	if opt.Chart == "" && opt.ChartSHA256 == "" && opt.Enforcement == nil {
		return nil
	}
	if err := validateOrkaNativeChartSelection(opt.Chart, opt.ChartSHA256); err != nil {
		return err
	}
	chart, spec, err := a.loadOrkaInstaller(OrkaOptions{
		Chart: opt.Chart, ChartSHA256: opt.ChartSHA256, Enforcement: opt.Enforcement,
	})
	if err != nil {
		return err
	}
	if spec.enrollment != nil {
		enabled := *spec.enrollment
		spec.enrollment = &enabled
		a.notef("Explicit privileged enforcement provider enrollment: %t. Task enforcement is a separate requirement.", enabled)
	}
	a.orkaSetup = &preparedOrkaSetup{chart: chart, spec: spec}
	a.orkaNativeCandidate = &a.orkaSetup.spec
	return nil
}

func (a *App) orkaSetupTaskNamespace() string {
	if a.orkaSetup != nil {
		return a.orkaSetup.spec.taskNamespace
	}
	return OrkaNamespace
}

func (a *App) orkaSetupNamespaces() string {
	if namespace := a.orkaSetupTaskNamespace(); namespace != OrkaNamespace {
		return OrkaPathNamespaces + ", " + namespace
	}
	return OrkaPathNamespaces
}

func (a *App) prepareQuickstartEnforcement(opt OrkaSetupOptions, filename string) error {
	requirement, err := readOrkaTaskEnforcement(a.operationContext(), filename)
	if err != nil {
		return err
	}
	if err := a.prepareOrkaSetup(opt); err != nil {
		return err
	}
	a.taskEnforcement = requirement
	return nil
}

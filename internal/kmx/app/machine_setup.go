package app

// MachineEvent is provisional observed progress, never readiness evidence.
type MachineEvent struct {
	MachineEnvelope
	Event  string `json:"event"`
	Stage  string `json:"stage"`
	Status string `json:"status,omitempty"`
}

// MachineEffect conservatively describes cluster mutation, not resource ownership.
type MachineEffect struct {
	Stage    string `json:"stage"`
	Attempt  string `json:"attempt"`
	Mutation string `json:"mutation"`
}

type MachineSetupResult struct {
	MachineResult
	Mode    string          `json:"mode"`
	Effects []MachineEffect `json:"effects"`
}

// WithProvisioningProgress borrows an observer on an operation-local copy.
// Only machine observers enable cancellation checks at phase boundaries.
func (a *App) WithProvisioningProgress(observer func(name, status string, err error)) *App {
	copy := *a
	copy.operationProgress = observer
	return &copy
}

// MachineProvisioningStage excludes raw phase text from the wire contract.
func MachineProvisioningStage(operation, label string) string {
	if operation == "cluster.up" {
		for _, stage := range UpSteps {
			if label == upPhaseName(stage) {
				return stage
			}
		}
	} else if operation == "cluster.orka.install" {
		switch label {
		case "Fetch the pinned installer":
			return "installer"
		case "Validate chart CRDs", "Apply chart CRDs and install Orka":
			return "runtime"
		case "Provision Task result reader":
			return "result_reader"
		case "Wire a keyless Provider":
			return "provider"
		}
	}
	return "other"
}

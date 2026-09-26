package app

// CreateOptions accepts references, never credential values. Raw numeric flags
// preserve the distinction between an omitted limit and an explicit zero.
type CreateOptions struct {
	Name, Namespace, Description, ProviderType, Model, BaseURL, Secret, SecretKey string
	Instructions, InstructionText, Tools, Skills, Task                            string
	AgentRequestsPerMinute, AgentTokensPerMinute                                  string
	ProviderRequestsPerMinute, ProviderTokensPerMinute                            string
	ResultServiceAccount, OrkaAPIService, ResultPort, SchemaTarget                string
	Out                                                                           string
	NoApply, DryRun                                                               bool
	// Resolved before entering raw terminal mode. Keep the original flags and
	// distinguish an empty file from an instruction source not yet read.
	instructionFileText *string
	descriptionDefault  string
}

// serverCondition is the shape of a Kubernetes status condition as the Orka
// and agent-show readers decode it.
type serverCondition struct {
	Type    string `json:"type"`
	Status  string `json:"status"`
	Message string `json:"message"`
	// LastTransitionTime distinguishes a live verdict from one about a
	// credential that has since been replaced.
	LastTransitionTime string `json:"lastTransitionTime"`
	ObservedGeneration int64  `json:"observedGeneration"`
}

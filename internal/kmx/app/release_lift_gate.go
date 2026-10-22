package app

import (
	"context"
	"reflect"

	suitecore "github.com/agent-primitives/suite/core"
)

// releaseLiftGate consumes already-coherent packaged evidence and a freshly
// prepared destination. Promotion is an explicit relationship, never a rewrite
// of the evidence into destination execution proof or signer authorization.
func (a *App) releaseLiftGate(ctx context.Context, source, destination suiteNativeSubject, promote bool) (string, string, error) {
	if reflect.DeepEqual(source, destination) {
		return "same-subject", "", nil
	}
	if !promote || !validSuitePassingSubject(destination) || !sameReleasePackagedSubject(source, destination) || !sameReleaseRuntimeProfile(source.Runtime, destination.Runtime) || source.Binding.Provider.Type != destination.Binding.Provider.Type || destination.Binding.Provider.Type != "openai" {
		return "", "", ErrReleaseLift
	}
	if err := a.verifyReleaseServingModel(ctx, destination); err != nil {
		return "", "", err
	}
	return "promotion", destination.Model, nil
}

func sameReleasePackagedSubject(source, destination suiteNativeSubject) bool {
	// Project out only the environment inputs explicitly allowed to change. The
	// copies are local policy comparisons; neither recorded receipt is modified.
	// All remaining packaged fields, including future fields, must still match.
	source.Target, destination.Target = suiteNativeTarget{}, suiteNativeTarget{}
	source.Binding, destination.Binding = suiteNativeBinding{}, suiteNativeBinding{}
	source.Runtime, destination.Runtime = suiteNativeRuntime{}, suiteNativeRuntime{}
	source.PhysicalMember, destination.PhysicalMember = "", ""
	source.EvaluationRun, destination.EvaluationRun = "", ""
	source.BindingDigest, destination.BindingDigest = "", ""
	source.PayloadDigest, destination.PayloadDigest = "", ""
	source.RenderedDigest, destination.RenderedDigest = "", ""
	return reflect.DeepEqual(source, destination)
}

func sameReleaseRuntimeProfile(source, destination suiteNativeRuntime) bool {
	// Installation identities/configuration are independently verified and bound
	// in each subject. Only these explicitly named physical identities may differ;
	// controller/worker images, chart/App/ManagedBy and mode remain exact. A new
	// runtime field is compared by default, not silently discarded.
	source.ControllerName, destination.ControllerName = "", ""
	source.ControllerUID, destination.ControllerUID = "", ""
	source.ControllerGeneration, destination.ControllerGeneration = 0, 0
	source.ControllerReplicas, destination.ControllerReplicas = 0, 0
	source.ControllerServiceAccount, destination.ControllerServiceAccount = "", ""
	source.WatchNamespace, destination.WatchNamespace = "", ""
	source.WorkerAccount, destination.WorkerAccount = "", ""
	source.WorkerAccountUID, destination.WorkerAccountUID = "", ""
	source.Helm.Instance, destination.Helm.Instance = "", ""
	return source == destination
}

func (a *App) verifyReleaseServingModel(ctx context.Context, subject suiteNativeSubject) error {
	// defaultModel is the selected Provider's declaration that this endpoint serves
	// the packaged model. The deployed member's model still comes from the suite;
	// this declaration does not prove backend behavior or destination evaluation.
	const projection = `go-template={"Name":{{printf "%q" .metadata.name}},"Namespace":{{printf "%q" .metadata.namespace}},"UID":{{printf "%q" .metadata.uid}},"Generation":{{.metadata.generation}},"ResourceVersion":{{printf "%q" .metadata.resourceVersion}},"Type":{{printf "%q" .spec.type}},"DefaultModel":{{printf "%q" .spec.defaultModel}}}`
	provider := subject.Binding.Provider
	raw, err := a.orkaCapture(ctx, nil, "-n", subject.Target.Namespace, "get", "providers.core.orka.ai", provider.Name, "--ignore-not-found=true", "-o", projection)
	if err != nil {
		return err
	}
	var observed struct {
		Name, Namespace, UID, ResourceVersion, Type, DefaultModel string
		Generation                                                int64
	}
	if suitecore.Unmarshal(raw, &observed) != nil || observed.Name != provider.Name || observed.Namespace != provider.Namespace || observed.UID != provider.UID || observed.Generation != provider.Generation || observed.ResourceVersion != provider.ResourceVersion || observed.Type != provider.Type || observed.DefaultModel == "" || observed.DefaultModel != subject.Model {
		return ErrReleaseLift
	}
	return nil
}

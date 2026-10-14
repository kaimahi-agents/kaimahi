package app

func kagentPrimaryReadiness(object *kagentObject, id kagentIdentity) (string, string) {
	if object.Status.ObservedGeneration != id.Generation {
		return "failed", "pending: stale or missing observed generation"
	}
	if !kagentObjectConditionTrue(object.Status.Conditions, "Accepted", id) {
		for _, condition := range object.Status.Conditions {
			if condition.Type == "Accepted" && condition.Status == "False" && (condition.ObservedGeneration == id.Generation || id.Kind == "ModelConfig" && condition.ObservedGeneration == 0) {
				return "failed", "current-generation primary rejection"
			}
		}
		return "failed", "pending: primary acceptance unknown"
	}
	if id.Kind == "Agent" && !kagentReadyConditionTrue(object.Status.Conditions, id.Generation) {
		return "failed", "pending or failed: Agent is not current-generation DeploymentReady"
	}
	return "", ""
}

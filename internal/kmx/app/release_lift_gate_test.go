package app

import (
	"reflect"
	"strings"
	"testing"
)

func TestReleaseLiftPromotionRuntimeFieldPolicy(t *testing.T) {
	source := suiteNativeRuntime{ControllerName: "controller", ControllerUID: "controller-uid", ControllerServiceAccount: "controller-account", ControllerImage: "controller-image", ControllerGeneration: 1, ControllerReplicas: 1, Mode: "harness-v2", WatchNamespace: "staging", AIWorkerImage: "worker-image", WorkerAccount: "worker-account", WorkerAccountUID: "worker-uid", Helm: suiteNativeHelmIdentity{Chart: "orka-0.2.0", Instance: "staging", App: "orka", ManagedBy: "Helm"}}
	for _, tc := range []struct {
		field   string
		allowed bool
	}{
		{"ControllerName", true}, {"ControllerUID", true}, {"ControllerServiceAccount", true}, {"ControllerImage", false},
		{"ControllerGeneration", true}, {"ControllerReplicas", true}, {"Mode", false}, {"WatchNamespace", true},
		{"AIWorkerImage", false}, {"WorkerAccount", true}, {"WorkerAccountUID", true}, {"Helm.Chart", false},
		{"Helm.Instance", true}, {"Helm.App", false}, {"Helm.ManagedBy", false},
	} {
		t.Run(tc.field, func(t *testing.T) {
			changed := source
			field := reflect.ValueOf(&changed).Elem()
			for _, part := range strings.Split(tc.field, ".") {
				field = field.FieldByName(part)
			}
			switch field.Kind() {
			case reflect.String:
				field.SetString(field.String() + "-other")
			case reflect.Int64, reflect.Int32:
				field.SetInt(field.Int() + 1)
			default:
				t.Fatalf("unhandled runtime field kind: %v", field.Kind())
			}
			if sameReleaseRuntimeProfile(source, changed) != tc.allowed {
				t.Fatalf("incorrect runtime relation for %s", tc.field)
			}
		})
	}
}

func TestReleaseLiftPromotionPackagedFieldPolicy(t *testing.T) {
	_, _, _, native := releaseLiftFixture(t)
	source := native.Subject
	for _, field := range []string{"Suite", "SuiteDigest", "GraphDigest", "Member", "MemberDigest", "InstructionsDigest", "CompositionDigest", "BuildProfileDigest", "Model", "Renderer"} {
		t.Run(field, func(t *testing.T) {
			changed := source
			v := reflect.ValueOf(&changed).Elem().FieldByName(field)
			v.SetString(v.String() + "-other")
			if sameReleasePackagedSubject(source, changed) {
				t.Fatalf("packaged identity change allowed: %s", field)
			}
		})
	}
	changed := source
	changed.RequiredImages = []string{"member-image"}
	if sameReleasePackagedSubject(source, changed) {
		t.Fatal("member-image requirement change allowed")
	}
}

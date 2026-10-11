package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/lift"
)

// Asking for a retired phase must refuse before Azure preflight or any writes.
func TestLiftPlanePhasesRefuseBeforeCloudAccess(t *testing.T) {
	for _, step := range []string{"boundary", "credential", "plane"} {
		t.Run(step, func(t *testing.T) {
			opt := lift.Options{Payload: lift.PayloadOrka, ResourceGroup: "rg", Cluster: "cluster", Registry: "reg12345", Step: step}
			err := opt.Validate()
			if err == nil || !strings.Contains(err.Error(), "--step") || !strings.Contains(err.Error(), step) {
				t.Fatalf("retired phase reached cloud access instead of phase validation: %v", err)
			}
		})
	}
}

// Materializing a workspace must not require plane assets just to install Orka.
func TestLiftWorkspaceCarriesOnlyProvisioningScripts(t *testing.T) {
	a := &App{}
	dir, cleanup, err := a.liftWorkspace()
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	for _, name := range []string{"aks-up.sh", "aks-down.sh", "kube-guard.sh"} {
		info, err := os.Stat(filepath.Join(dir, "scripts", name))
		if err != nil || info.Mode().Perm()&0o100 == 0 {
			t.Fatalf("provisioning script %s is missing or not executable: %v", name, err)
		}
	}
	for _, name := range []string{"k8s", "scripts/plane-deploy.sh", "scripts/netpol-probe.sh"} {
		if _, err := os.Stat(filepath.Join(dir, name)); !os.IsNotExist(err) {
			t.Errorf("lift still carries plane assets at %s: %v", name, err)
		}
	}
}

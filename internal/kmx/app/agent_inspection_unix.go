//go:build !windows

package app

import (
	"context"
	"fmt"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/guard"
)

func (a *App) provisionOrkaInspection() error {
	return a.provision([]dependency{depKubectl})
}

// Unix Task Result retains the ordinary kubectl path, including the operator's
// kuberc credential-plugin policy. Windows uses the physically pinned remote
// inspection path instead.
func (a *App) preflightOrkaTaskResult() error { return a.preflight(depKubectl) }

func (a *App) preflightSuiteWorkspaceKubectl() error {
	kubectl := depKubectl
	kubectl.fetchable = false
	return a.preflight(kubectl)
}

func (*App) requireRemoteSuiteWorkspaceContext(context.Context) error   { return nil }
func (*App) requireRemoteSuiteWorkspacePosture(*guard.Kubeconfig) error { return nil }

func inspectOrkaInspectionKubectl(context.Context, string) (string, error) { return "", nil }
func recheckOrkaInspectionKubectl(context.Context, string, string) error   { return nil }

func (a *App) readOrkaAgentMachineList(format, namespace string) ([]byte, error) {
	return a.orkaCapture(a.operationContext(), nil, "-n", namespace, "get", "agents.core.orka.ai", "-o", format)
}

func (a *App) readOrkaAgentTableList(namespace string) ([]byte, error) {
	return a.orkaCapture(a.operationContext(), nil, "-n", namespace, "get", "agents.core.orka.ai", "-o", "json")
}

func (a *App) missingOrkaInspectionKind() error {
	return fmt.Errorf("no Orka Agent kind on this cluster, so nothing here is an Orka agent.\n"+
		"  Install Orka with `%s`", a.operationCommand("cluster", "orka", "install"))
}

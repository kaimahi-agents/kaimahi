//go:build !windows

package app

import (
	"context"
	"fmt"
)

func (a *App) provisionOrkaInspection() error {
	return a.provision([]dependency{depKubectl})
}

func inspectOrkaInspectionKubectl(context.Context, string) (string, error) { return "", nil }
func recheckOrkaInspectionKubectl(context.Context, string, string) error   { return nil }

func (a *App) missingOrkaInspectionKind() error {
	return fmt.Errorf("no Orka Agent kind on this cluster, so nothing here is an Orka agent.\n"+
		"  Install Orka with `%s`", a.operationCommand("cluster", "orka", "install"))
}

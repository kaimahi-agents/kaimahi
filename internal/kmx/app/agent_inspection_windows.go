//go:build windows

package app

import (
	"context"
	"fmt"
	"net"
	"path/filepath"
	"strings"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/fileaccess"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/guard"
)

// Remote inspection trusts only the operator-installed kubectl and its normal
// kubeconfig exec-auth chain. It never downloads a tool or starts a login.
func (*App) provisionOrkaInspection() error { return nil }

// Windows Task Result uses the physically pinned remote inspection boundary.
func (a *App) preflightOrkaTaskResult() error { return a.preflightOrkaRemote() }

func (a *App) preflightSuiteWorkspaceKubectl() error { return a.preflightOrkaRemote() }

func (a *App) requireRemoteSuiteWorkspaceContext(ctx context.Context) error {
	raw, err := a.orkaCapture(ctx, nil, "config", "view", "-o", "json")
	if err != nil {
		return fmt.Errorf("native Windows workspace Test requires an existing remote Orka context")
	}
	cfg, err := guard.ParseKubeconfig(raw)
	if err != nil {
		return fmt.Errorf("native Windows workspace Test requires an existing remote Orka context")
	}
	return a.requireRemoteSuiteWorkspacePosture(cfg)
}

func (a *App) requireRemoteSuiteWorkspacePosture(cfg *guard.Kubeconfig) error {
	posture, err := guard.Classify(cfg, a.Cfg.KubeContext)
	host := strings.TrimSuffix(strings.ToLower(posture.Host), ".")
	ip := net.ParseIP(host)
	if err != nil || posture.Local || host == "" || host == "localhost" || ip != nil && (ip.IsLoopback() || ip.IsUnspecified()) {
		return fmt.Errorf("native Windows workspace Test requires an existing remote Orka context")
	}
	return nil
}

func inspectOrkaInspectionKubectl(ctx context.Context, path string) (identity string, err error) {
	if !filepath.IsAbs(path) || !strings.EqualFold(filepath.Ext(path), ".exe") {
		return "", fmt.Errorf("native kubectl must resolve to an absolute .exe")
	}
	parent, err := fileaccess.OpenDirectory(ctx, filepath.Dir(path))
	if err != nil {
		return "", err
	}
	defer parent.Close()
	file, err := fileaccess.OpenRegularAt(ctx, parent, filepath.Base(path))
	if err != nil {
		return "", err
	}
	defer file.Close()
	return fileaccess.PhysicalIdentity(ctx, file)
}

func recheckOrkaInspectionKubectl(ctx context.Context, path, expected string) error {
	identity, err := inspectOrkaInspectionKubectl(ctx, path)
	if err != nil {
		return err
	}
	if identity == "" || identity != expected {
		return fmt.Errorf("native kubectl identity changed")
	}
	return nil
}

func (*App) readOrkaAgentMachineList(string, string) ([]byte, error) {
	return nil, fmt.Errorf("native Windows Orka list supports table output only")
}

func (a *App) readOrkaAgentTableList(namespace string) ([]byte, error) {
	return a.readOrkaAgentList(namespace)
}

func (*App) missingOrkaInspectionKind() error {
	return fmt.Errorf("no Orka Agent kind on this cluster; install Orka from a supported administration environment before using native Windows inspection")
}

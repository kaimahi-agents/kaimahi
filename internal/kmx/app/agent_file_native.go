package app

import (
	"context"
	"github.com/google/uuid"
	"net"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentfile"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentpreview"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/copilotd"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/localpreview"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/localtools"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/modelaccess/copilot"
	"github.com/microsoft/agent-host-protocol/clients/go/ahptypes"
)

const nativeRunnerLabel = "copilotd native execution (name-based selection; no journal; not a receipt)"

func previewCompletedPolicy(c agentpreview.Completion, show, native bool) FilePreviewCompletion {
	row := previewCompleted(c, show)
	if native {
		row.Native = previewNative(c.Native, show)
	}
	return row
}

// NewCopilotdFileRunner uses the host's execution, not embedded tools or journals.
func NewCopilotdFileRunner(host string, snapshot func(context.Context, func(...string) error) (agentpreview.SourceSnapshot, error)) agentpreview.Runner {
	p := copilot.New(nil)
	return copilotd.NewRunner(copilotd.Options{Host: host, ExampleTimeout: localpreview.DefaultExampleTimeout, Snapshot: snapshot, Judge: newNativeJudge(p.PreviewClient), Access: func(ctx context.Context, observe func(ahptypes.ActionEnvelope) error) (copilot.HostAccess, error) {
		access, err := p.CopilotdClient(ctx, host, observe)
		if access != nil {
			access = nativeDisplayAccess{access}
		}
		return access, err
	}})
}

type nativeDisplayAccess struct{ copilot.HostAccess }

func (a nativeDisplayAccess) CheckContent(fields ...string) error {
	return nativeDisplayCheck(a.HostAccess.CheckContent)(fields...)
}

// nativeDisplayCheck also checks display-normalized views, for the host's and the
// separate judge's credentials alike.
func nativeDisplayCheck(check func(...string) error) func(...string) error {
	return func(fields ...string) error {
		for _, field := range fields {
			if err := check(field); err != nil {
				return err
			}
			views, _, ok := suiteDiagnosticViews(field, len(field))
			if !ok {
				// Binary working files are scanned as bytes, not rendered as text.
				continue
			}
			if err := check(views...); err != nil {
				return err
			}
		}
		return nil
	}
}

func validNative(n *agentpreview.NativeExecution) bool {
	if n == nil || n.Selection != "" && n.Selection != "name" {
		return false
	}
	u, err := url.Parse(n.Host)
	if err != nil {
		return false
	}
	ip := net.ParseIP(u.Hostname())
	port, e := strconv.Atoi(u.Port())
	if u.Scheme != "ws" && u.Scheme != "wss" || ip == nil || !ip.IsLoopback() || e != nil || port < 1 || port > 65535 || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
		return false
	}
	if !strings.HasPrefix(n.Session, "ahp-session:/") || len(n.Session) <= len("ahp-session:/") || !suitePrefixedDigest(n.ContentRevision) || n.ProtocolVersion == "" || n.GitRevision != "" && (len(n.GitRevision) != 40 && len(n.GitRevision) != 64 || strings.Trim(n.GitRevision, "0123456789abcdef") != "") {
		return false
	}
	chatSafe, ok := suiteDiagnosticText(n.Chat, 4096, nil)
	if !ok || chatSafe != n.Chat {
		// The host uses a 32-hex chat identifier, not a bearer credential.
		if !strings.HasPrefix(n.Chat, "ahp-chat:/") {
			return false
		}
		if _, err := uuid.Parse(strings.TrimPrefix(n.Chat, "ahp-chat:/")); err != nil {
			return false
		}
	}
	for _, field := range []string{n.Host, n.HostVersion, n.ProtocolVersion, n.Session, n.Turn, n.ContentRevision, n.GitRevision, n.EffectiveApprovalMode, n.PendingTool, n.PendingInput} {
		safe, ok := suiteDiagnosticText(field, 4096, nil)
		if !ok || safe != field {
			return false
		}
	}
	if n.PendingTool != "" && !localtools.ValidToolName(n.PendingTool) || len(n.ActualModels) > 32 {
		return false
	}
	for _, model := range n.ActualModels {
		if !agentfile.ValidModel(model) || suiteDiagnosticIdentity(model, nil) != model {
			return false
		}
	}
	return true
}
func previewNative(n *agentpreview.NativeExecution, show bool) *agentpreview.NativeExecution {
	if n == nil {
		return nil
	}
	copy := *n
	copy.ActualModels = slices.Clone(n.ActualModels)
	copy.PendingInput, _ = suiteDiagnosticText(copy.PendingInput, 4096, nil)
	if !show {
		copy.PendingInput = ""
	}
	if !validNative(&copy) {
		return nil
	}
	return &copy
}
func validPreviewExecutionTools(c agentpreview.Completion, ex agentfile.Example, names []string, native bool) bool {
	if !native {
		return validPreviewTools(c, ex, names) && c.ToolCalls >= 0 && c.ToolCalls <= localtools.MaxCalls && (len(names) > 0 || c.ToolCalls == 0)
	}
	if previewNative(c.Native, true) == nil || c.ID != c.Native.Session || ex.Mocks != nil || c.ToolCalls < 0 {
		return false
	}
	if c.State == agentpreview.Complete {
		if c.Native.Selection != "name" || c.Native.Chat == "" || c.Native.Turn == "" || len(c.Native.ActualModels) == 0 {
			return false
		}
		for _, model := range c.Native.ActualModels {
			if model != c.Model {
				return false
			}
		}
	}
	seen := map[string]bool{}
	total := 0
	for _, t := range c.Tools {
		if !localtools.ValidToolName(t.Name) || seen[t.Name] || t.Mode != "live" || t.Source != "live" || t.RecordingSaved || t.CallCount < 0 || t.RequestCount != t.CallCount || t.CallCount > c.ToolCalls-total {
			return false
		}
		seen[t.Name] = true
		total += t.CallCount
	}
	for _, name := range names {
		if !seen[name] {
			return false
		}
	}
	return total == c.ToolCalls
}

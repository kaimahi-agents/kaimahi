package main

import (
	"fmt"
	"io"
	"net"
	"net/url"
	"strconv"
	"strings"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentpreview"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/secretshapes"
	"github.com/spf13/cobra"
)

func validateNativeFileCommand(cmd *cobra.Command, f fileCommandOptions) error {
	native := f.Runner == "copilotd"
	if f.Runner != "" && f.Runner != "embedded" && !native {
		return fmt.Errorf("--runner must be embedded or copilotd")
	}
	if !native {
		if cmd.Flags().Changed("host") {
			return fmt.Errorf("--host requires --runner copilotd")
		}
		return nil
	}
	if f.Host == "" {
		return fmt.Errorf("--runner copilotd requires --host ws://IP:PORT")
	}
	for _, name := range []string{"mcp-server", "no-mcp-servers", "tool-bindings"} {
		if cmd.Flags().Changed(name) {
			return fmt.Errorf("--runner copilotd refuses --%s", name)
		}
	}
	if f.Provider != "" && f.Provider != "copilot" || f.Endpoint != "" {
		return fmt.Errorf("--runner copilotd uses the host's Copilot model route; --provider-url and other providers do not apply")
	}
	u, e := url.Parse(f.Host)
	if e != nil {
		return fmt.Errorf("--host requires a literal loopback WebSocket endpoint")
	}
	ip := net.ParseIP(u.Hostname())
	port, e := strconv.Atoi(u.Port())
	if u.Scheme != "ws" && u.Scheme != "wss" || ip == nil || !ip.IsLoopback() || e != nil || port < 1 || port > 65535 || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || secretshapes.Match(f.Host) != nil {
		return fmt.Errorf("--host requires a literal loopback WebSocket endpoint without credentials, query or fragment")
	}
	return nil
}
func nativeSessionCreated(out io.Writer) func(string, string) error {
	return func(caseID, session string) error {
		for _, s := range []string{caseID, session} {
			if len(s) > 4096 || strings.ContainsAny(s, "\r\n") || secretshapes.Match(s) != nil {
				return fmt.Errorf("unsafe native session diagnostic withheld")
			}
		}
		line := agentpreview.SessionCreatedLine(caseID, session)
		n, err := io.WriteString(out, line)
		if err == nil && n != len(line) {
			err = io.ErrShortWrite
		}
		return err
	}
}

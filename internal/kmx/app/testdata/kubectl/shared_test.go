// Package kubectl contains executable-boundary fixtures independent of App.
package kubectl

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	protocol "github.com/kaimahi-agents/kaimahi/internal/kmx/app/kubectlfake"
)

type orkaCall = protocol.OrkaCall

const (
	OrkaNamespace      = protocol.OrkaNamespace
	orkaResultAccount  = protocol.OrkaResultAccount
	orkaPortableMarker = protocol.OrkaPortableMarker
	orkaRenderedMarker = protocol.OrkaRenderedMarker
)

func orkaTestToken() string { return protocol.OrkaToken() }
func getenvLiftTest(key, fallback string) string {
	return protocol.GetenvFallback(os.Getenv, key, fallback)
}

func executableRequest(handler func(protocol.Request) error) {
	args := os.Args[slices.Index(os.Args, "--")+1:]
	err := handler(protocol.Request{Context: context.Background(), Args: args, Getenv: os.Getenv, Stdin: os.Stdin, Stdout: os.Stdout, Stderr: os.Stderr})
	if err != nil {
		var status *protocol.ExitError
		if errors.As(err, &status) {
			os.Exit(status.Code)
		}
		os.Exit(2)
	}
	os.Exit(0)
}

func TestMain(m *testing.M) {
	if os.Getenv("KMX_KUBECTL_FIXTURE_MODE") == "inspect" {
		err := inspect(protocol.Request{Context: context.Background(), Args: os.Args[1:], Getenv: os.Getenv, Stdin: os.Stdin, Stdout: os.Stdout, Stderr: os.Stderr})
		if err != nil {
			var status *protocol.ExitError
			if errors.As(err, &status) {
				os.Exit(status.Code)
			}
			os.Exit(2)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func TestFixtureProtocolHelper(t *testing.T)  { executableRequest(protocol.Pins) }
func TestFixtureBuildModeHelper(t *testing.T) { fmt.Printf("race=%t\n", fixtureRace); os.Exit(0) }
func TestInspectKubectlHelper(t *testing.T)   { executableRequest(inspect) }

func inspect(r protocol.Request) error {
	if slices.Equal(r.Args, []string{"version", "--client"}) {
		fmt.Fprint(r.Stdout, r.Getenv("KMX_INSPECT_VERSION_STDOUT"))
		fmt.Fprint(r.Stderr, r.Getenv("KMX_INSPECT_VERSION_STDERR"))
		if r.Getenv("KMX_INSPECT_VERSION_FAIL") != "" {
			return &protocol.ExitError{Code: 1}
		}
		return nil
	}
	if len(r.Args) < 3 || r.Args[0] != "--context" || r.Args[1] != "kind-test" || r.Args[2] != "--request-timeout=10s" {
		fmt.Fprint(r.Stderr, "private-invalid-inspection-request")
		return &protocol.ExitError{Code: 1}
	}
	if path := r.Getenv("KMX_INSPECT_CALLS"); path != "" {
		f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
		if err != nil {
			return &protocol.ExitError{Code: 2}
		}
		_, _ = fmt.Fprintln(f, strings.Join(r.Args, " "))
		_ = f.Close()
	}
	if delay := r.Getenv("KMX_INSPECT_DELAY"); delay != "" {
		d, err := time.ParseDuration(delay)
		if err != nil {
			return &protocol.ExitError{Code: 2}
		}
		select {
		case <-r.Context.Done():
			return r.Context.Err()
		case <-time.After(d):
		}
	}

	args := r.Args[3:]
	kind, name := "", ""
	if i := slices.Index(args, "get"); i >= 0 && len(args) > i+1 {
		kind = args[i+1]
		if len(args) > i+2 && !strings.HasPrefix(args[i+2], "-") {
			name = args[i+2]
		}
	}
	key := "KMX_INSPECT_GENERIC"
	switch kind {
	case "agents.core.orka.ai":
		if name == "" {
			key = "KMX_INSPECT_AGENTS"
		} else {
			key = "KMX_INSPECT_AGENT"
		}
	case "providers.core.orka.ai":
		key = "KMX_INSPECT_PROVIDER"
	case "secret":
		key = "KMX_INSPECT_SECRET"
	case "--raw":
		key = "KMX_INSPECT_TASKS"
		if strings.Contains(name, "continue=") && r.Getenv("KMX_INSPECT_TASKS_CONTINUED") != "" {
			key = "KMX_INSPECT_TASKS_CONTINUED"
		}
	}
	value := r.Getenv(key)
	if strings.HasPrefix(value, "ERROR:") {
		fmt.Fprint(r.Stderr, strings.TrimPrefix(value, "ERROR:"))
		return &protocol.ExitError{Code: 1}
	}
	if strings.HasPrefix(value, "ERRORREPEAT:") {
		parts := strings.SplitN(strings.TrimPrefix(value, "ERRORREPEAT:"), ":", 2)
		n, err := strconv.Atoi(parts[0])
		if err != nil || len(parts) != 2 {
			return &protocol.ExitError{Code: 2}
		}
		_, _ = io.WriteString(r.Stderr, strings.Repeat(parts[1], n))
		return &protocol.ExitError{Code: 1}
	}
	if strings.HasPrefix(value, "REPEAT:") {
		parts := strings.SplitN(strings.TrimPrefix(value, "REPEAT:"), ":", 2)
		n, err := strconv.Atoi(parts[0])
		if err != nil || len(parts) != 2 {
			return &protocol.ExitError{Code: 2}
		}
		_, _ = io.WriteString(r.Stdout, strings.Repeat(parts[1], n))
		return nil
	}
	_, _ = io.WriteString(r.Stdout, value)
	return nil
}

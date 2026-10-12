// Package kubectl contains executable-boundary fixtures independent of App.
package kubectl

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"testing"

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
func TestFixtureProtocolHelper(t *testing.T)  { executableRequest(protocol.Pins) }
func TestFixtureBuildModeHelper(t *testing.T) { fmt.Printf("race=%t\n", fixtureRace); os.Exit(0) }

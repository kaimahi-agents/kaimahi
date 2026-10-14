package kubectl

import (
	"os"
	"slices"
	"testing"

	protocol "github.com/kaimahi-agents/kaimahi/internal/kmx/app/kubectlfake"
)

func TestKagentKubectlHelper(t *testing.T) {
	if os.Getenv("KMX_KAGENT_TEST_DIR") != "" {
		if slices.Index(os.Args, "--") < 0 {
			os.Exit(2)
		}
		executableRequest(protocol.Kagent)
	}
}
func TestKagentKubectlPinsHelper(t *testing.T) {
	if os.Getenv("KMX_KAGENT_TEST_DIR") != "" {
		executableRequest(protocol.KagentPins)
	}
}

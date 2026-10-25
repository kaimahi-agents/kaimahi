package kubectl

import (
	protocol "github.com/kaimahi-agents/kaimahi/internal/kmx/app/kubectlfake"
	"os"
	"testing"
)

func TestSessionsAgentKubectlHelper(t *testing.T) {
	if os.Getenv("KMX_SESSIONS_TEST_DIR") != "" {
		executableRequest(protocol.AgentSessions)
	}
}

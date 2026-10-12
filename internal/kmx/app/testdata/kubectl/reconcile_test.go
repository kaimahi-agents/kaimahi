package kubectl

import (
	"os"
	"testing"

	protocol "github.com/kaimahi-agents/kaimahi/internal/kmx/app/kubectlfake"
)

func TestReconcileKubectlHelper(t *testing.T) {
	if os.Getenv("KMX_RECONCILE_DIR") != "" {
		executableRequest(protocol.Reconcile)
	}
}

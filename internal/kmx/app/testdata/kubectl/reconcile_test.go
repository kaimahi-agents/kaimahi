package kubectl

import (
	"os"
	"testing"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/app/testdata/kubectl/protocol"
)

func TestReconcileKubectlHelper(t *testing.T) {
	if os.Getenv("KMX_RECONCILE_DIR") != "" {
		executableRequest(protocol.Reconcile)
	}
}

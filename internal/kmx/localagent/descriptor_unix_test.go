//go:build !windows

package localagent

import (
	"context"
	"os"
	"os/exec"
	"testing"

	"golang.org/x/sys/unix"
)

func TestWorkerProtectsBrokerBeforeStateOrRouteChecks(t *testing.T) {
	if os.Getenv("KMX_TEST_WORKER_FD") == "1" {
		broker := os.NewFile(5, "inherited-broker")
		defer broker.Close()
		before, err := unix.FcntlInt(broker.Fd(), unix.F_GETFD, 0)
		if err != nil || before&unix.FD_CLOEXEC != 0 {
			t.Fatalf("fixture is not inherited: %v %v", before, err)
		}
		// Invalid identity refuses before state/route work, but descriptor protection
		// must already have happened at worker entry.
		if runLocalAgentWorker(context.Background(), "invalid", "invalid", localAgentCredentialSnapshot{}, os.NewFile(3, "readiness"), broker) == nil {
			t.Fatal("invalid worker accepted")
		}
		after, err := unix.FcntlInt(broker.Fd(), unix.F_GETFD, 0)
		if err != nil || after&unix.FD_CLOEXEC == 0 {
			t.Fatalf("broker descriptor unprotected: %v %v", after, err)
		}
		if err := exec.Command("/bin/sh", "-c", "test ! -e /dev/fd/5").Run(); err != nil {
			t.Fatal("route helper inherited broker descriptor")
		}
		return
	}
	program, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	null, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer null.Close()
	cmd := exec.Command(program, "-test.run=^TestWorkerProtectsBrokerBeforeStateOrRouteChecks$")
	cmd.Env = []string{"KMX_TEST_WORKER_FD=1", "KMX_HOME=" + t.TempDir()}
	cmd.ExtraFiles = []*os.File{null, null, null}
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("worker helper: %v\n%s", err, output)
	}
}

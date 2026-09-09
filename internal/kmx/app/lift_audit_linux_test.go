package app

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/lift"
	"golang.org/x/sys/unix"
)

func TestLiftDownInteractiveConfirmationReachesCarriedScript(t *testing.T) {
	for _, answer := range []string{"demo(rg)", "no"} {
		t.Run(answer, func(t *testing.T) {
			a, out, dir := liftAuditApp(t)
			master, err := os.OpenFile("/dev/ptmx", os.O_RDWR|unix.O_NOCTTY, 0)
			if err != nil {
				t.Skipf("PTY unavailable: %v", err)
			}
			defer master.Close()
			if err := unix.IoctlSetPointerInt(int(master.Fd()), unix.TIOCSPTLCK, 0); err != nil {
				t.Fatal(err)
			}
			number, err := unix.IoctlGetInt(int(master.Fd()), unix.TIOCGPTN)
			if err != nil {
				t.Fatal(err)
			}
			slave, err := os.OpenFile(fmt.Sprintf("/dev/pts/%d", number), os.O_RDWR|unix.O_NOCTTY, 0)
			if err != nil {
				t.Fatal(err)
			}
			defer slave.Close()
			a.Stdin = slave
			if _, err := master.WriteString(answer + "\n"); err != nil {
				t.Fatal(err)
			}
			opt := lift.Options{ResourceGroup: "demo(rg)", Cluster: "demo-cluster"}
			liftAuditRecord(t, a, opt, lift.Pre{}, true)
			err = a.LiftDown(opt)
			calls, _ := os.ReadFile(filepath.Join(dir, "calls"))
			if answer == "no" {
				if err == nil || strings.Contains(string(calls), "delete") {
					t.Fatalf("declined confirmation acted: %v\n%s", err, calls)
				}
				return
			}
			if err != nil {
				t.Fatalf("interactive confirmation did not preconfirm script: %v\n%s", err, out)
			}
			if strings.Count(out.String(), "Type the resource group") != 1 || !strings.Contains(out.String(), "aks-down: confirmed via KAIMAHI_CONFIRM") {
				t.Fatalf("script requested a second confirmation: %s", out)
			}
			if !strings.Contains(string(calls), "resource delete") || !strings.Contains(string(calls), "group delete") {
				t.Fatalf("confirmed teardown did not finish: %s", calls)
			}
		})
	}
}

package localtools

import (
	"testing"

	"golang.org/x/sys/unix"
)

func TestCommandGroupRequiresCompleteOwnedZombieObservation(t *testing.T) {
	leader := unix.KinfoProc{Proc: unix.ExternProc{P_pid: 101, P_stat: 5}, Eproc: unix.Eproc{Pgid: 101, Ppid: 100}}
	child := unix.KinfoProc{Proc: unix.ExternProc{P_pid: 102, P_stat: 5}, Eproc: unix.Eproc{Pgid: 101, Ppid: 101}}
	if !commandGroupExited(101, 100, []unix.KinfoProc{leader, child}) {
		t.Fatal("complete exited group refused")
	}
	live := child
	live.Proc.P_stat = 2
	foreign := child
	foreign.Eproc.Pgid = 99
	otherParent := leader
	otherParent.Eproc.Ppid = 99
	for _, members := range [][]unix.KinfoProc{nil, {child}, {leader, live}, {leader, foreign}, {otherParent, child}} {
		if commandGroupExited(101, 100, members) {
			t.Fatalf("incomplete/unsafe group accepted: %+v", members)
		}
	}
}

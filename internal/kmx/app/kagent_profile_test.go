package app

import (
	"sync"
	"testing"
)

// Version selection must not leak between simultaneous or interleaved Apps.
func TestKagentLiftRequiresExactVersionAndChartLabels(t *testing.T) {
	profile := kagentLiftProfile()
	for _, labels := range []map[string]string{nil, {"app.kubernetes.io/version": "0.10.3"}, {"helm.sh/chart": "kagent-0.10.3"}} {
		if err := profile.validateLabels(labels); err == nil {
			t.Fatalf("missing exact v0103 chart/version evidence accepted: %v", labels)
		}
	}
}

func TestKagentProfilesAreInvocationLocal(t *testing.T) {
	old, newer := kagentLiftProfile(), kagentLiftProfile()
	old.Version, old.Commit = "v0.10.2", "68df64f671800c37c4204d81ebe0dd66ec35d223"
	apps := []*App{{kagentProfile: &old}, {kagentProfile: &newer}, {}, {kagentProfile: &newer}, {kagentProfile: &old}}
	var wg sync.WaitGroup
	for _, a := range apps {
		wg.Go(func() {
			for i := 0; i < 200; i++ {
				p := a.selectedKagentProfile()
				if !p.validVersion(p.Version, p.Commit) {
					t.Errorf("own version rejected: %s", p.Version)
				}
				other := old
				if p.Version == old.Version {
					other = newer
				}
				if p.validVersion(other.Version, other.Commit) || p.validateLabels(map[string]string{"app.kubernetes.io/version": other.tag()}) == nil {
					t.Errorf("%s accepted foreign %s", p.Version, other.Version)
				}
			}
		})
	}
	wg.Wait()
}

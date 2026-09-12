package app

import (
	"math"
	"strings"
	"testing"
)

func lifecycleInt(n int64) *int64 { return &n }

func TestLifecycleValidationPrecedesGuard(t *testing.T) {
	const id = "00000000-0000-4000-8000-000000000001"
	a := &App{} // Any cluster access or guard call would panic.
	for name, call := range map[string]func() error{
		"negative budget":      func() error { return a.Budget("demo", lifecycleInt(-1), nil) },
		"zero ttl":             func() error { return a.Approve(id, lifecycleInt(0), nil, nil) },
		"excessive ttl":        func() error { return a.Approve(id, lifecycleInt(math.MaxInt64), nil, nil) },
		"zero uses":            func() error { return a.Approve(id, nil, lifecycleInt(0), nil) },
		"excessive uses":       func() error { return a.Approve(id, nil, lifecycleInt(1_000_001), nil) },
		"zero amount":          func() error { return a.Approve(id, nil, lifecycleInt(1), lifecycleInt(0)) },
		"excessive amount":     func() error { return a.Approve(id, nil, lifecycleInt(1), lifecycleInt(1_000_000_000_001)) },
		"renew name":           func() error { return a.RenewCredential("bad name", nil) },
		"renew ttl":            func() error { return a.RenewCredential("demo", lifecycleInt(59)) },
		"retired tool request": func() error { return a.Request("demo", "tool", "call") },
	} {
		t.Run(name, func(t *testing.T) {
			if err := call(); err == nil {
				t.Fatal("invalid lifecycle operation accepted")
			}
		})
	}
}

func TestLifecycleGuardDescribesLimitsAndFaithfulCommand(t *testing.T) {
	const id = "00000000-0000-4000-8000-000000000001"
	for _, tc := range []struct {
		name string
		call func(*App) error
		want []string
	}{
		{"budget", func(a *App) error { return a.Budget("demo", lifecycleInt(0), lifecycleInt(300)) }, []string{"cents=0 tokens=300", "budget demo --cents 0 --tokens 300"}},
		{"clear caps", func(a *App) error { return a.Budget("demo", nil, nil) }, []string{"cents=null tokens=null", "null clears the cap", "budget demo"}},
		{"approval", func(a *App) error { return a.Approve(id, lifecycleInt(600), lifecycleInt(1), lifecycleInt(100)) }, []string{"ttl_seconds=600 uses=1 amount=100", "approve " + id + " --ttl 600 --uses 1 --amount 100"}},
		{"budget request", func(a *App) error { return a.Request("demo", "budget", "tokens") }, []string{"file a budget approval request", "request budget tokens --credential demo"}},
		{"renew", func(a *App) error { return a.RenewCredential("demo", lifecycleInt(3600)) }, []string{"renewing for 3600 seconds", "credential renew demo --ttl 3600"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newPgFixture(t)
			// A non-local context refuses without confirmation. Nothing
			// beyond the guard may run, but its action and command are visible.
			f.app.Cfg.KubeContext = "remote 'context'"
			err := tc.call(f.app)
			if err == nil {
				t.Fatal("unconfirmed remote mutation accepted")
			}
			message := f.errOut.String() + err.Error()
			for _, want := range append(tc.want, "--context "+shellArg(f.app.Cfg.KubeContext)) {
				if !strings.Contains(message, want) {
					t.Errorf("guard missing %q:\n%s", want, message)
				}
			}
			if strings.Contains(f.args(), "port-forward") {
				t.Fatalf("mutation reached admin session: %s", f.args())
			}
		})
	}
}

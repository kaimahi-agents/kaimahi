package app

import (
	"math"
	"strings"
	"testing"
)

func lifecycleInt(n int64) *int64 { return &n }

func TestLifecycleValidationPrecedesGuard(t *testing.T) {
	a := &App{} // Any cluster access or guard call would panic.
	for name, call := range map[string]func() error{
		"negative cents":      func() error { return a.Budget("demo", lifecycleInt(-1), nil) },
		"negative tokens":     func() error { return a.Budget("demo", nil, lifecycleInt(-1)) },
		"budget name":         func() error { return a.Budget("bad name", nil, nil) },
		"renew name":          func() error { return a.RenewCredential("bad name", nil) },
		"renew zero ttl":      func() error { return a.RenewCredential("demo", lifecycleInt(0)) },
		"renew short ttl":     func() error { return a.RenewCredential("demo", lifecycleInt(59)) },
		"renew excessive ttl": func() error { return a.RenewCredential("demo", lifecycleInt(math.MaxInt64)) },
	} {
		t.Run(name, func(t *testing.T) {
			if err := call(); err == nil {
				t.Fatal("invalid lifecycle operation accepted")
			}
		})
	}
}

func TestLifecycleGuardDescribesLimitsAndFaithfulCommand(t *testing.T) {
	for _, tc := range []struct {
		name string
		call func(*App) error
		want []string
	}{
		{"budget", func(a *App) error { return a.Budget("demo", lifecycleInt(0), lifecycleInt(300)) }, []string{"cents=0 tokens=300", "budget demo --cents 0 --tokens 300"}},
		{"clear caps", func(a *App) error { return a.Budget("demo", nil, nil) }, []string{"cents=null tokens=null", "null clears the cap", "budget demo"}},
		{"one cap", func(a *App) error { return a.Budget("demo", nil, lifecycleInt(300)) }, []string{"cents=null tokens=300", "budget demo --tokens 300"}},
		{"default renewal", func(a *App) error { return a.RenewCredential("demo", nil) }, []string{"plane's default lifetime", "credential renew demo"}},
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

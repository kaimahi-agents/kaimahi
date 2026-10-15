package app

import (
	"context"
	"errors"
	"io"
	"reflect"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/guard"
)

// Never exposed or installed on the caller App. This invocation-local grant
// binds frozen source, repository, exact observed subjects and kubeconfig bytes.
type workspaceLiftConsent struct {
	plan     *PreparedWorkspaceLift
	kube     []byte
	subjects [2]suiteNativeSubject
}

func (a *App) workspaceLiftConsent(ctx context.Context, p *PreparedWorkspaceLift, eval, dest suiteNativeSubject, selectedDisclosure string, out io.Writer) (*workspaceLiftConsent, error) {
	raw, err := a.orkaCapture(ctx, nil, "config", "view", "-o", "json")
	if err != nil || !reflect.DeepEqual(raw, p.kube) {
		return nil, errors.New("kubeconfig changed after disclosed plan")
	}
	cfg, err := guard.ParseKubeconfig(raw)
	if err != nil {
		return nil, err
	}
	disclosure := p.plan.SuiteDigest + " → " + p.plan.Repository
	if p.options.JSON {
		disclosure += "; " + selectedDisclosure
	}
	if err := guard.ConfirmPipeline(ctx, cfg, []string{eval.Target.Context, dest.Target.Context}, a.Cfg.Confirm, disclosure, out, a.Stdin); err != nil {
		return nil, err
	}
	grant := &workspaceLiftConsent{p, append([]byte(nil), raw...), [2]suiteNativeSubject{eval, dest}}
	for _, subject := range grant.subjects {
		worker := a.releaseLiftWorker(ctx, ReleaseLiftOptions{ToContext: subject.Target.Context})
		if err := grant.check(ctx, worker, CreateOptions{Name: subject.physicalMember(), Namespace: subject.Target.Namespace}); err != nil {
			return nil, err
		}
	}
	return grant, nil
}
func (g *workspaceLiftConsent) check(ctx context.Context, a *App, opt CreateOptions) error {
	refuse := errors.New("workspace Lift scoped consent changed or does not authorize this target")
	if g == nil || g.plan == nil || g.plan.options.Repository != g.plan.plan.Repository || g.plan.review.checkCurrent(ctx) != nil {
		return refuse
	}
	raw, err := a.orkaCapture(ctx, nil, "config", "view", "-o", "json")
	if err != nil || !reflect.DeepEqual(raw, g.kube) {
		return refuse
	}
	for _, subject := range g.subjects {
		if subject.Target.Context != a.Cfg.KubeContext || subject.Target.Namespace != opt.Namespace || subject.physicalMember() != opt.Name {
			continue
		}
		var observed *suiteNativePrepared
		if subject.EvaluationRun != "" {
			observed, err = a.prepareSuiteNativeCandidate(ctx, g.plan.review.suite, opt.Namespace, subject.Binding.Provider.Name, subject.EvaluationRun)
		} else {
			observed, err = a.prepareSuiteNative(ctx, g.plan.review.suite, opt.Namespace, subject.Binding.Provider.Name)
		}
		if err != nil || !reflect.DeepEqual(observed.Subject, subject) {
			return refuse
		}
		if g.plan.review.checkCurrent(ctx) != nil {
			return refuse
		}
		return nil
	}
	return refuse
}

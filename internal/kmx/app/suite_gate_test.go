package app

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentsuite/workspace"
	rt "github.com/kaimahi-agents/kaimahi/internal/kmx/runtime"
)

func suiteClonePolicy(t *testing.T, p suitePromotionPolicy) suitePromotionPolicy {
	t.Helper()
	raw, _ := json.Marshal(p)
	var out suitePromotionPolicy
	json.Unmarshal(raw, &out)
	return out
}
func TestSuiteGateExactAndReviewedTransition(t *testing.T) {
	s, source, r, p := suiteEvidenceFixture(t)
	d, err := gateSuitePromotion(s, source, source, r, p)
	if err != nil {
		t.Fatal(err)
	}
	if d.DestinationEvaluated || d.ReceiptDigest != p.ReceiptDigest || len(d.AuthorizedTransitions) != 0 || d.PolicyDigest != "sha256:"+suiteTestHash(mustSuiteCanonical(t, p)) {
		t.Fatal("wrong gate permission or attestation claim")
	}
	dest := source
	dest.Target.Context = "destination"
	dest.Target.ClusterUID = "cluster-destination"
	dest.Target.NamespaceUID = "namespace-destination"
	dest.Binding.Provider.UID = "provider-destination"
	dest.Binding.Provider.BaseURL = "https://destination.example.invalid/v1"
	dest.Binding.Secret.UID = "secret-destination"
	dest.Runtime.ControllerUID = "controller-destination"
	dest.Runtime.WorkerAccountUID = "worker-destination"
	dest = suiteTestSubject(t, s, dest)
	p.Destination = dest
	p.AllowedTransitionPaths = []string{"target.context", "target.clusterUID", "target.namespaceUID", "binding.provider.uid", "binding.provider.baseURL", "binding.secret.uid", "runtime.controllerUID", "runtime.workerAccountUID"}
	raw, _ := json.MarshalIndent(p, "", "  ")
	parsed, err := decodeSuitePromotionPolicy(raw)
	if err != nil {
		t.Fatal(err)
	}
	d, err = gateSuitePromotion(s, source, dest, r, parsed)
	if err != nil {
		t.Fatal(err)
	}
	if d.DestinationEvaluated || len(d.AuthorizedTransitions) != 8 || d.ReceiptDigest != p.ReceiptDigest {
		t.Fatal("transition claimed destination proof or omitted reviewed changes")
	}
	// Owned proof UIDs remain proof-side evidence, not destination expectations.
	r.Placement.Resources[0].UID = "new-proof-provider"
	r.Placement.Resources[1].UID = "new-proof-agent"
	p.ReceiptDigest = "sha256:" + suiteTestHash(mustSuiteCanonical(t, r))
	if _, err = gateSuitePromotion(s, source, dest, r, p); err != nil {
		t.Fatal(err)
	}
}
func TestSuiteGateRefusesUnapprovedEvidenceAndTransitions(t *testing.T) {
	s, source, base, policy := suiteEvidenceFixture(t)
	tests := map[string]func(*suiteEvaluationReceipt, *suitePromotionPolicy, *suiteNativeSubject, *suiteNativeSubject){
		"absent-policy": func(_ *suiteEvaluationReceipt, p *suitePromotionPolicy, _, _ *suiteNativeSubject) {
			*p = suitePromotionPolicy{}
		}, "version": func(_ *suiteEvaluationReceipt, p *suitePromotionPolicy, _, _ *suiteNativeSubject) {
			p.SchemaVersion = 2
		}, "policy-domain": func(_ *suiteEvaluationReceipt, p *suitePromotionPolicy, _, _ *suiteNativeSubject) {
			p.Domain = "bundle"
		}, "wrong-run": func(_ *suiteEvaluationReceipt, p *suitePromotionPolicy, _, _ *suiteNativeSubject) {
			p.RunID = strings.Repeat("b", 32)
		}, "latest": func(_ *suiteEvaluationReceipt, p *suitePromotionPolicy, _, _ *suiteNativeSubject) { p.RunID = "latest" }, "receipt-digest": func(_ *suiteEvaluationReceipt, p *suitePromotionPolicy, _, _ *suiteNativeSubject) {
			p.ReceiptDigest = "sha256:" + strings.Repeat("f", 64)
		}, "raw-digest": func(_ *suiteEvaluationReceipt, p *suitePromotionPolicy, _, _ *suiteNativeSubject) {
			p.ReceiptDigest = strings.TrimPrefix(p.ReceiptDigest, "sha256:")
		}, "wildcard": func(_ *suiteEvaluationReceipt, p *suitePromotionPolicy, _, _ *suiteNativeSubject) {
			p.AllowedTransitionPaths = []string{"*"}
		}, "path-prefix": func(_ *suiteEvaluationReceipt, p *suitePromotionPolicy, _, _ *suiteNativeSubject) {
			p.AllowedTransitionPaths = []string{"binding.*"}
		}, "model-override": func(_ *suiteEvaluationReceipt, p *suitePromotionPolicy, _, d *suiteNativeSubject) {
			d.Model = "different"
			p.Destination = *d
			p.AllowedTransitionPaths = []string{"model"}
		}, "derived-override": func(_ *suiteEvaluationReceipt, p *suitePromotionPolicy, _, _ *suiteNativeSubject) {
			p.AllowedTransitionPaths = []string{"renderedDigest"}
		}, "unused-transition": func(_ *suiteEvaluationReceipt, p *suitePromotionPolicy, _, _ *suiteNativeSubject) {
			p.AllowedTransitionPaths = []string{"target.context"}
		}, "null-transitions": func(_ *suiteEvaluationReceipt, p *suitePromotionPolicy, _, _ *suiteNativeSubject) {
			p.AllowedTransitionPaths = nil
		}, "source-drift": func(_ *suiteEvaluationReceipt, _ *suitePromotionPolicy, e, _ *suiteNativeSubject) {
			e.Binding.Secret.ResourceVersion = "new"
			*e = suiteTestSubject(t, s, *e)
		}, "policy-source": func(_ *suiteEvaluationReceipt, p *suitePromotionPolicy, _, _ *suiteNativeSubject) {
			p.Evaluated.Target.ClusterUID = "other"
		}, "policy-destination": func(_ *suiteEvaluationReceipt, p *suitePromotionPolicy, _, _ *suiteNativeSubject) {
			p.Destination.Binding.Provider.UID = "other"
		}, "undeclared": func(_ *suiteEvaluationReceipt, p *suitePromotionPolicy, _, d *suiteNativeSubject) {
			d.Target.ClusterUID = "other"
			*d = suiteTestSubject(t, s, *d)
			p.Destination = *d
		}, "spurious-digest-change": func(_ *suiteEvaluationReceipt, p *suitePromotionPolicy, _, d *suiteNativeSubject) {
			d.PayloadDigest = strings.Repeat("f", 64)
			p.Destination = *d
		}, "incoherent-transition": func(_ *suiteEvaluationReceipt, p *suitePromotionPolicy, _, d *suiteNativeSubject) {
			d.Target.Namespace = "other"
			*d = suiteTestSubject(t, s, *d)
			p.Destination = *d
			p.AllowedTransitionPaths = []string{"target.namespace"}
		}, "preview": func(r *suiteEvaluationReceipt, _ *suitePromotionPolicy, _, _ *suiteNativeSubject) {
			r.ExecutionMode = "preview"
		}, "diagnostic": func(r *suiteEvaluationReceipt, p *suitePromotionPolicy, _, _ *suiteNativeSubject) {
			r.FullCaseSet = false
			r.PromotionEligible = false
			p.ReceiptDigest = "sha256:" + suiteTestHash(mustSuiteCanonical(t, *r))
		}, "subset": func(r *suiteEvaluationReceipt, p *suitePromotionPolicy, _, _ *suiteNativeSubject) {
			r.FullCaseSet = false
			r.PromotionEligible = false
			r.Cases = r.Cases[:1]
			p.ReceiptDigest = "sha256:" + suiteTestHash(mustSuiteCanonical(t, *r))
		}, "unknown": func(r *suiteEvaluationReceipt, p *suitePromotionPolicy, _, _ *suiteNativeSubject) {
			r.Detail = "subject unavailable"
			r.Result = rt.EvaluationUnknown
			r.PromotionEligible = false
			p.ReceiptDigest = "sha256:" + suiteTestHash(mustSuiteCanonical(t, *r))
		}, "fail": func(r *suiteEvaluationReceipt, p *suitePromotionPolicy, _, _ *suiteNativeSubject) {
			r.Cases[0].Verdict = rt.EvaluationFail
			r.Cases[0].Assertions[0].Verdict = rt.EvaluationFail
			r.Cases[0].Assertions[0].Reason = "assertion not satisfied"
			r.Result = rt.EvaluationFail
			r.PromotionEligible = false
			p.ReceiptDigest = "sha256:" + suiteTestHash(mustSuiteCanonical(t, *r))
		},
	}
	for name, change := range tests {
		t.Run(name, func(t *testing.T) {
			r := suiteCloneReceipt(t, base)
			p := suiteClonePolicy(t, policy)
			e, d := source, source
			change(&r, &p, &e, &d)
			if _, err := gateSuitePromotion(s, e, d, r, p); err == nil {
				t.Fatal("unreviewed promotion accepted")
			}
		})
	}
}
func TestSuitePolicyStrictJSON(t *testing.T) {
	_, _, _, p := suiteEvidenceFixture(t)
	raw := string(mustSuiteCanonical(t, p))
	for name, bad := range map[string]string{"unknown": strings.Replace(raw, `"domain":"suite-promotion"`, `"domain":"suite-promotion","RAW_UNKNOWN_PAYLOAD":"CANARY"`, 1), "override": strings.Replace(raw, `"domain":"suite-promotion"`, `"domain":"suite-promotion","override":true`, 1), "duplicate": strings.Replace(raw, `"schemaVersion":1`, `"schemaVersion":1,"schemaVersion":1`, 1), "yaml": "schemaVersion: 1\ndomain: suite-promotion\n", "null-source": strings.Replace(raw, `"evaluated":{`, `"evaluated":null,"ignored":{`, 1), "null-list": strings.Replace(raw, `"allowedTransitionPaths":[]`, `"allowedTransitionPaths":null`, 1), "missing-list": strings.Replace(raw, `"allowedTransitionPaths":[],`, "", 1)} {
		t.Run(name, func(t *testing.T) {
			_, err := decodeSuitePromotionPolicy([]byte(bad))
			if err == nil {
				t.Fatal("policy JSON accepted")
			}
			if strings.Contains(err.Error(), "RAW_UNKNOWN_PAYLOAD") || strings.Contains(err.Error(), "CANARY") {
				t.Fatal("raw policy leaked")
			}
		})
	}
}

// Each possible consumed leaf is tested independently; this catches accidental
// broad permissions or comparison omissions, not just one representative field.
func TestSuiteGateAllConsumedSubjectLeavesRequireReview(t *testing.T) {
	s, src, r, p := suiteEvidenceFixture(t)
	var leaves []string
	var collect func(reflect.Value, string)
	collect = func(v reflect.Value, path string) {
		if v.Kind() == reflect.Struct {
			typ := v.Type()
			for i := 0; i < v.NumField(); i++ {
				name := strings.Split(typ.Field(i).Tag.Get("json"), ",")[0]
				collect(v.Field(i), path+"."+name)
			}
			return
		}
		if v.Kind() == reflect.String || v.Kind() == reflect.Int64 || v.Kind() == reflect.Bool {
			leaves = append(leaves, path)
		}
	}
	collect(reflect.ValueOf(src.Target), "target")
	collect(reflect.ValueOf(src.Binding), "binding")
	collect(reflect.ValueOf(src.Runtime), "runtime")
	for _, path := range leaves {
		t.Run(path, func(t *testing.T) {
			dest := src
			v := reflect.ValueOf(&dest).Elem()
			for _, part := range strings.Split(path, ".") {
				for i := 0; i < v.NumField(); i++ {
					if strings.Split(v.Type().Field(i).Tag.Get("json"), ",")[0] == part {
						v = v.Field(i)
						break
					}
				}
			}
			switch v.Kind() {
			case reflect.String:
				v.SetString(v.String() + "-changed")
			case reflect.Int64:
				v.SetInt(v.Int() + 1)
			case reflect.Bool:
				v.SetBool(!v.Bool())
			}
			dest = suiteTestSubjectIfRenderable(t, s, dest)
			policy := suiteClonePolicy(t, p)
			policy.Destination = dest
			if _, err := gateSuitePromotion(s, src, dest, r, policy); err == nil {
				t.Fatal("consumed identity drift accepted without review")
			}
		})
	}
}
func suiteTestSubjectIfRenderable(t *testing.T, s *workspace.NativeSuite, d suiteNativeSubject) suiteNativeSubject {
	// Invalid fixed fields remain invalid without attempting an unsupported render.
	if d.Binding.Provider.Type != "openai" {
		return d
	}
	return suiteTestSubject(t, s, d)
}

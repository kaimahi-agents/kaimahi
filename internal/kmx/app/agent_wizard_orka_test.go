package app

import (
	"bytes"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

func nativeWizardOptions() CreateOptions {
	return CreateOptions{Name: "demo", Description: "Demo", Namespace: "team", ProviderType: "openai", Model: "custom-model", Secret: "model-key"}
}

func TestCreateWizardNativeOfflinePreservesOptionsWithoutDiscovery(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	for _, mode := range []string{"stdout", "no-apply", "dry-run"} {
		t.Run(mode, func(t *testing.T) {
			opt := nativeWizardOptions()
			opt.Task, opt.Tools, opt.Skills = "Say hello", "read,search", "summarize"
			opt.BaseURL, opt.SecretKey = "http://model.example/v1", "custom-key"
			opt.InstructionText = "Retain my instructions"
			opt.AgentRequestsPerMinute, opt.ProviderTokensPerMinute = "12", "1234"
			switch mode {
			case "stdout":
				opt.Out = "-"
			case "no-apply":
				opt.NoApply = true
			case "dry-run":
				opt.DryRun = true
			}
			completed, err := runCreateWizard(nil, &bytes.Buffer{}, opt)
			if err != nil {
				t.Fatal(err)
			}
			if completed.ResultServiceAccount != "" || completed.Model != "custom-model" || completed.Task != opt.Task || completed.Tools != opt.Tools || completed.Skills != opt.Skills || completed.BaseURL != opt.BaseURL || completed.SecretKey != opt.SecretKey || completed.InstructionText != opt.InstructionText || completed.AgentRequestsPerMinute != "12" || completed.ProviderTokensPerMinute != "1234" || completed.AgentTokensPerMinute != "" || completed.ProviderRequestsPerMinute != "" {
				t.Fatalf("native supplied options changed: %+v", completed)
			}
			if mode != "dry-run" {
				completed.Out = "-"
				var output bytes.Buffer
				a := &App{Out: &output, Err: &bytes.Buffer{}}
				if err := a.CreateAgent(completed); err != nil {
					t.Fatal(err)
				}
				if !strings.Contains(output.String(), "core.orka.ai/v1alpha1") || strings.Contains(output.String(), "kagent.dev") {
					t.Fatal("not native Orka")
				}
			}
		})
	}
}

func TestCreateWizardNativeTaskAccountAndAuthority(t *testing.T) {
	opt := nativeWizardOptions()
	opt.Task = "Say hello"
	m, err := newCreateWizardModel(opt)
	if err != nil {
		t.Fatal(err)
	}
	if m.step == createConfirm || !strings.Contains(m.View().Content, "Existing result-reader ServiceAccount") {
		t.Fatalf("missing account prompt: %s", m.View().Content)
	}
	m.input.SetValue("reader")
	m = updateCreateWizard(t, m, wizardKey(tea.KeyEnter))
	if m.step != createConfirm || m.opt.ResultServiceAccount != "reader" {
		t.Fatalf("account not collected: %+v", m.opt)
	}
	for _, want := range []string{"authorizes Task execution", "full authority", "v0.1.3"} {
		if !strings.Contains(m.View().Content, want) {
			t.Fatalf("confirmation hides %q", want)
		}
	}
	opt.ResultServiceAccount = "supplied-reader"
	m, err = newCreateWizardModel(opt)
	if err != nil || m.step != createConfirm || m.opt.ResultServiceAccount != "supplied-reader" {
		t.Fatalf("supplied account prompted again: %+v %v", m.opt, err)
	}
}

func TestCreateWizardNativeRejectsInvalidOptionsBeforeCompletion(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*CreateOptions)
	}{
		{"legacy tools", func(o *CreateOptions) { o.Tools = "server:read" }},
		{"provider", func(o *CreateOptions) { o.ProviderType = "azure-openai" }},
		{"namespace", func(o *CreateOptions) { o.Namespace = "Not Valid" }},
		{"secret", func(o *CreateOptions) { o.Secret = "Not Valid" }},
		{"url", func(o *CreateOptions) { o.BaseURL = "https://user:password@model.example" }},
		{"explicit zero", func(o *CreateOptions) { o.AgentRequestsPerMinute = "0" }},
		{"explicit provider zero", func(o *CreateOptions) { o.ProviderTokensPerMinute = "0" }},
		{"result without task", func(o *CreateOptions) { o.ResultServiceAccount = "reader" }},
		{"stdout dry run", func(o *CreateOptions) { o.Out = "-"; o.DryRun = true }},
		{"offline dry run", func(o *CreateOptions) { o.NoApply = true; o.DryRun = true }},
		{"online schema override", func(o *CreateOptions) { o.SchemaTarget = "main" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			opt := nativeWizardOptions()
			tc.change(&opt)
			if _, err := newCreateWizardModel(opt); err == nil {
				t.Fatal("invalid options reached confirmation or completion")
			}
			// Exercise validation after the final missing field as well.
			opt.Name = ""
			m, err := newCreateWizardModel(opt)
			if err != nil {
				return
			}
			m.input.SetValue("demo")
			m = updateCreateWizard(t, m, wizardKey(tea.KeyEnter))
			if m.err == nil || m.step == createConfirm {
				t.Fatal("invalid collected options reached confirmation or completion")
			}
		})
	}
}

func TestCreateWizardNativeRefusesCredentialsBeforeDerivedEcho(t *testing.T) {
	credential := "sk-" + "proj-" + strings.Repeat("B", 32)
	for _, supplied := range []bool{true, false} {
		opt := nativeWizardOptions()
		opt.Name, opt.Description = "", ""
		if supplied {
			opt.Description = credential
		}
		m, err := newCreateWizardModel(opt)
		if supplied {
			if err == nil || strings.Contains(err.Error(), credential) {
				t.Fatal("supplied credential was not safely refused")
			}
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		m.input.SetValue(credential)
		m = updateCreateWizard(t, m, wizardKey(tea.KeyEnter))
		if m.err == nil || m.step == createName || strings.Contains(strings.ToLower(m.View().Content), strings.ToLower(credential)) {
			t.Fatal("typed credential reached derived-name echo")
		}
	}
}

func TestCreateWizardNativeExplainsExplicitEndpointFlag(t *testing.T) {
	m, err := newCreateWizardModel(CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(m.View().Content, "--base-url") {
		t.Fatal("missing custom endpoint guidance")
	}
	var out bytes.Buffer
	_, _ = collectCreateOptions(&sliceScanner{}, &out, CreateOptions{})
	if !strings.Contains(out.String(), "--base-url") {
		t.Fatal("fallback hides custom endpoint flag")
	}
}

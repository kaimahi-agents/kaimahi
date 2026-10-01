package app

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	agentruntime "github.com/kaimahi-agents/kaimahi/internal/kmx/runtime"
)

func TestPortableSourceIdenticalForEveryCreationTargetBinding(t *testing.T) {
	base := goldenNoTaskCreate("")
	original, err := portableOrkaSource(base)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name  string
		alter func(*CreateOptions)
	}{
		{"namespace", func(o *CreateOptions) { o.Namespace = "another" }},
		{"provider type", func(o *CreateOptions) { o.ProviderType = "anthropic" }},
		{"base URL", func(o *CreateOptions) { o.BaseURL = "https://new.example.invalid" }},
		{"Secret name", func(o *CreateOptions) { o.Secret = "other-key" }},
		{"Secret key", func(o *CreateOptions) { o.SecretKey = "token" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			changed := base
			tc.alter(&changed)
			source, err := portableOrkaSource(changed)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(original, source) {
				t.Fatal("creation target leaked into portable source bytes")
			}
		})
	}
}

func TestPortableDigestIndependentOfBindingsAndRenderedDigestDependsOnThem(t *testing.T) {
	opt := goldenNoTaskCreate("")
	source, err := portableOrkaSource(opt)
	if err != nil {
		t.Fatal(err)
	}
	bindings := orkaBindingsFromCreate(opt)
	adapter := orkaRuntimeAdapter{create: &CreateOptions{NoApply: true, SchemaTarget: "v0.1.3"}, bindings: &bindings}
	prepared, err := agentruntime.PreparePortableRender(source, adapter)
	if err != nil {
		t.Fatal(err)
	}
	first, err := adapter.Render(context.Background(), prepared, agentruntime.RenderOptions{})
	if err != nil {
		t.Fatal(err)
	}
	adapter.bindings.Provider.SecretRef.Name = "another-key"
	adapter.bindings.Namespace = "elsewhere"
	second, err := adapter.Render(context.Background(), prepared, agentruntime.RenderOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if first.PortableDigest() != second.PortableDigest() || first.PortableDigest() != agentruntime.PortableBundleDigest(source) {
		t.Fatal("target bindings changed the portable revision digest")
	}
	if first.RenderedDigest() == second.RenderedDigest() {
		t.Fatal("changed namespace and Secret binding did not change rendered output")
	}
	for _, doc := range second.Documents() {
		if bytes.Contains(doc, []byte("orka-system")) || bytes.Contains(doc, []byte("model-key")) {
			t.Fatal("render leaked the original target bindings")
		}
	}
}

func TestRenderedDigestChangesForEachTargetBinding(t *testing.T) {
	opt := goldenNoTaskCreate("")
	source, err := portableOrkaSource(opt)
	if err != nil {
		t.Fatal(err)
	}
	base := orkaBindingsFromCreate(opt)
	for _, tc := range []struct {
		name   string
		change func(*agentruntime.OrkaBindings)
	}{
		{"namespace", func(b *agentruntime.OrkaBindings) { b.Namespace = "another" }},
		{"type", func(b *agentruntime.OrkaBindings) { b.Provider.Type = "anthropic" }},
		{"baseURL", func(b *agentruntime.OrkaBindings) { b.Provider.BaseURL = "https://new.example.invalid" }},
		{"Secret", func(b *agentruntime.OrkaBindings) { b.Provider.SecretRef.Name = "other-key" }},
		{"key", func(b *agentruntime.OrkaBindings) { b.Provider.SecretRef.Key = "token" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			adapter := orkaRuntimeAdapter{create: &CreateOptions{NoApply: true, SchemaTarget: "v0.1.3"}, bindings: &base}
			prepared, err := agentruntime.PreparePortableRender(source, adapter)
			if err != nil {
				t.Fatal(err)
			}
			first, err := adapter.Render(context.Background(), prepared, agentruntime.RenderOptions{})
			if err != nil {
				t.Fatal(err)
			}
			changed := base
			tc.change(&changed)
			adapter.bindings = &changed
			second, err := adapter.Render(context.Background(), prepared, agentruntime.RenderOptions{})
			if err != nil {
				t.Fatal(err)
			}
			if first.PortableDigest() != second.PortableDigest() || first.RenderedDigest() == second.RenderedDigest() {
				t.Fatalf("%s did not change rendered identity while preserving source identity", tc.name)
			}
		})
	}
}

func TestExplicitInvalidBindingsNeverFallBackToCreateFlags(t *testing.T) {
	opt := goldenNoTaskCreate("")
	source, err := portableOrkaSource(opt)
	if err != nil {
		t.Fatal(err)
	}
	adapter := lifecycleAdapter(t, opt)
	adapter.bindings = &agentruntime.OrkaBindings{Provider: orkaBindingsFromCreate(opt).Provider}
	prepared, err := agentruntime.PreparePortableRender(source, adapter)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := adapter.Render(context.Background(), prepared, agentruntime.RenderOptions{}); err == nil {
		t.Fatal("empty explicitly supplied namespace fell back to create flags")
	}
}

func TestCoreOnlyRendersLikeEmptyOrkaExtension(t *testing.T) {
	opt := goldenNoTaskCreate("")
	adapter := lifecycleAdapter(t, opt)
	core := []byte("apiVersion: kmx.kaimahi.dev/v1alpha1\nkind: PortableAgent\nmetadata:\n  name: hello\nspec:\n  instructions: Do the thing.\n  model:\n    name: gpt-4o-mini\n")
	withExtension := append(append([]byte(nil), core...), []byte("extensions:\n  orka:\n    apiVersion: core.orka.ai/v1alpha1\n")...)
	var rendered []agentruntime.RenderedBundle
	for _, source := range [][]byte{core, withExtension} {
		prepared, err := agentruntime.PreparePortableRender(source, adapter)
		if err != nil {
			t.Fatal(err)
		}
		bundle, err := adapter.Render(t.Context(), prepared, agentruntime.RenderOptions{})
		if err != nil {
			t.Fatal(err)
		}
		rendered = append(rendered, bundle)
	}
	if rendered[0].PortableDigest() == rendered[1].PortableDigest() {
		t.Fatal("distinct authored bytes kept the same portable digest")
	}
	if rendered[0].RenderedDigest() != rendered[1].RenderedDigest() {
		t.Fatal("core-only changed the rendered digest")
	}
	for i, doc := range rendered[0].Documents() {
		if !bytes.Equal(doc, rendered[1].Documents()[i]) {
			t.Fatalf("rendered document %d differs", i)
		}
	}
}

func TestRenderOrkaBundleFileUsesExplicitBindingsWithoutClusterIO(t *testing.T) {
	opt := goldenNoTaskCreate("")
	source, err := portableOrkaSource(opt)
	if err != nil {
		t.Fatal(err)
	}
	path := t.TempDir()
	if err := os.WriteFile(filepath.Join(path, "agent.yaml"), source, 0600); err != nil {
		t.Fatal(err)
	}
	bindings := orkaBindingsFromCreate(opt)
	creation, err := RenderOrkaBundleFile(path, bindings)
	if err != nil {
		t.Fatal(err)
	}
	bindings.Namespace = "other-namespace"
	bindings.Provider.SecretRef.Name = "another-key"
	rebound, err := RenderOrkaBundleFile(path, bindings)
	if err != nil {
		t.Fatal(err)
	}
	if rebound.PortableDigest() != creation.PortableDigest() || rebound.RenderedDigest() == creation.RenderedDigest() {
		t.Fatal("the renderer did not use the supplied bindings against the exact file bytes")
	}
	if _, err := os.Stat(filepath.Join(path, "bindings.yaml")); !os.IsNotExist(err) {
		t.Fatalf("render requires stored creation bindings or wrote a file: %v", err)
	}
}

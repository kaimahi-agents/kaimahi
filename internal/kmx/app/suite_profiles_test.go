package app

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	agentsuite "github.com/agent-primitives/suite"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentsuite/workspace"
)

// Synthetic existing-format records establish offline consistency only. They
// are not execution proof or qualification of public implementation pins.
func profiledPassFixture(t *testing.T) (*suitePassStore, *workspace.NativeSuite, *workspace.NativeSuite, *suitePassingReceipt, *suitePassingReceipt) {
	t.Helper()
	return profiledPassFixtureRuntime(t, "orka")
}

func profiledPassFixtureRuntime(t *testing.T, runtimeID string) (*suitePassStore, *workspace.NativeSuite, *workspace.NativeSuite, *suitePassingReceipt, *suitePassingReceipt) {
	t.Helper()
	root, old, legacy, placement, observations := passingFixture(t)
	receipt, err := newSuitePassingReceipt(t.Context(), old, placement, observations)
	if err != nil {
		t.Fatal(err)
	}
	member := filepath.Join(root, "agents/greeter/member.yaml")
	data, err := os.ReadFile(member)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, member, strings.Split(string(data), "\nruntime:")[0]+"\n")
	data, err = os.ReadFile(filepath.Join(root, "build-profiles/default.json"))
	if err != nil {
		t.Fatal(err)
	}
	var bp agentsuite.BuildProfile
	if err := agentsuite.Unmarshal(data, &bp); err != nil {
		t.Fatal(err)
	}
	rows := ""
	for _, id := range []string{"a", "b"} {
		rp := agentsuite.RuntimeProfile{SchemaVersion: agentsuite.SpecVersion, MediaType: agentsuite.MediaTypeRuntimeProfile, ID: id, Runtime: agentsuite.EvidenceImplementation{ID: runtimeID, Version: "test-only", Digest: bp.RuntimeBase[0].Image.Digest}, Harness: agentsuite.EvidenceImplementation{ID: "test-harness", Version: "test-only", Digest: bp.Harness[0].Image.Digest}, Renderer: agentsuite.EvidenceImplementation{ID: "test-renderer", Version: "test-only", Digest: bp.Harness[0].Image.Digest}, ConfigurationABI: "kmx-native-orka-v2", ImageRequired: false}
		raw, err := agentsuite.Marshal(rp)
		if err != nil {
			t.Fatal(err)
		}
		writeFile(t, filepath.Join(root, "runtime-profiles/"+id+".json"), string(raw))
		rows += fmt.Sprintf("  - runtimeProfile: runtime-profiles/%s.json\n    buildProfile: build-profiles/default.json\n    platform: linux/amd64\n", id)
	}
	writeFile(t, filepath.Join(root, "workspace.yaml"), "schemaVersion: 3\nname: "+legacy.Name+"\nmember: agents/greeter/member.yaml\nprofiles:\n"+rows)
	p, err := workspace.Prepare(t.Context(), root, filepath.Join(t.TempDir(), "cache"))
	if err != nil {
		t.Fatal(err)
	}
	a, err := workspace.LoadSelected(t.Context(), p, workspace.Selection{RuntimeProfile: "a"})
	if err != nil {
		t.Fatal(err)
	}
	b, err := a.ForCompositionDigest(t.Context(), func() string {
		s, e := workspace.LoadSelected(t.Context(), p, workspace.Selection{RuntimeProfile: "b"})
		if e != nil {
			t.Fatal(e)
		}
		return s.CompositionDigest
	}())
	if err != nil {
		t.Fatal(err)
	}
	rebind := func(s *workspace.NativeSuite) *suitePassingReceipt {
		r := *receipt
		r.Subject.SuiteDigest = s.Digest
		r.Subject.GraphDigest = s.GraphDigest
		r.Subject.CompositionDigest = s.CompositionDigest
		r.Subject.BuildProfileDigest = s.BuildProfileDigest
		r.SourceFingerprint = p.Fingerprint
		r.Subject.RenderedDigest = ""
		r.Subject.RenderedDigest, err = suiteNativeDigest("kmx.native-orka.revision/v1", r.Subject)
		if err != nil {
			t.Fatal(err)
		}
		return &r
	}
	t.Setenv("KMX_HOME", privateSuiteDir(t))
	store, err := openSuitePassStore(t.Context(), root, filepath.Join(os.Getenv("KMX_HOME"), "suites"), a.Digest)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	return store, a, b, rebind(a), rebind(b)
}
func writeSyntheticProfileRecord(t *testing.T, s *suitePassStore, seq uint64, r *suitePassingReceipt) {
	t.Helper()
	raw, err := agentsuite.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	record, err := agentsuite.Marshal(suitePassRecord{1, s.identity, s.suiteDigest, seq, raw})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(s.partition, fmt.Sprintf("%020d.json", seq)), record, 0600); err != nil {
		t.Fatal(err)
	}
	m := publicMetadataFixture()
	m.Workspace, m.SuiteDigest, m.Sequence, m.ReceiptDigest = s.identity, s.suiteDigest, seq, suitePublicHash(raw)
	metadata, err := agentsuite.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(s.partition, m.filename()), metadata, 0600); err != nil {
		t.Fatal(err)
	}
}
func TestProfiledReceiptProjectionCannotAuthorizeConsumers(t *testing.T) {
	_, suite, _, receipt, _ := profiledPassFixture(t)
	before := mustProfileReceiptBytes(t, receipt)
	view, err := suiteReceiptDeclarationView(suite)
	if err != nil {
		t.Fatal(err)
	}
	if view.Profile == nil || view.Runtime.ID != "orka" {
		t.Fatal("missing retained marker or declaration mapping")
	}
	if _, err := canonicalSuitePassingReceipt(t.Context(), receipt, suite); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, mustProfileReceiptBytes(t, receipt)) {
		t.Fatal("coherence changed observed receipt facts")
	}
	view.Profile = nil
	if err := view.RequireUnprofiled(); !errors.Is(err, workspace.ErrProfileExecutionUnsupported) {
		t.Fatal("projection lost private marker", err)
	}
	app := &App{}
	if _, err := app.prepareSuiteNative(t.Context(), view, "ns", "provider"); !errors.Is(err, workspace.ErrProfileExecutionUnsupported) {
		t.Fatal("projection reached native execution", err)
	}
	if _, err := snapshotSuiteSessionsHost("unread", view); !errors.Is(err, workspace.ErrProfileExecutionUnsupported) {
		t.Fatal("projection reached host read", err)
	}
	if _, _, err := suitePublicEvidence(t.Context(), nil, view, receipt, nil); !errors.Is(err, workspace.ErrProfileExecutionUnsupported) {
		t.Fatal("projection reached portable export", err)
	}
}

// Consistent detached declarations confer no execution, evidence or Lift authority.
func TestSessionsAssociationCannotQualifyOrAuthorize(t *testing.T) {
	_, suite, _, receipt, _ := profiledPassFixtureRuntime(t, "agentsessions")
	const image = "ghcr.io/orka-agents/agentkit/serve-pydantic-ai@sha256:8c4c17dc3d778c02097ad7e1ea88e9084b3f1aad6c36458829ff9cec9b4d043c"
	raw, err := workspace.NewSessionsAssociation(t.Context(), suite, image)
	if err != nil || workspace.CheckSessionsAssociation(t.Context(), raw, suite) != nil {
		t.Fatal("consistent declaration refused", err)
	}
	if !bytes.Contains(raw, []byte(`"imageVerification":"declared-unverified"`)) {
		t.Fatal("image declaration lost unverified status")
	}
	app, _, calls := nativeFixture(t)
	before := calls.calls
	for _, operation := range []func() error{
		suite.RequireUnprofiled,
		func() error { _, err := app.prepareSuiteNative(t.Context(), suite, "ns", "provider"); return err },
		func() error { _, err := snapshotSuiteSessionsHost("unread", suite); return err },
		func() error { _, _, err := suitePublicEvidence(t.Context(), nil, suite, receipt, nil); return err },
	} {
		if err := operation(); !errors.Is(err, workspace.ErrProfileExecutionUnsupported) {
			t.Fatal("association bypassed profile refusal", err)
		}
	}
	if _, err := parseSuitePassingReceipt(raw); err == nil {
		t.Fatal("association became private Lift/evaluated-publication evidence")
	}
	if calls.calls != before {
		t.Fatal("declaration reached runtime effects")
	}
}

func TestReceiptCompositionGetterBranches(t *testing.T) {
	cases := []suitePassingReceipt{
		{ExecutionMode: "native-orka", Subject: suiteNativeSubject{CompositionDigest: "orka"}},
		{ExecutionMode: "native-kagent", Kagent: &suiteKagentPassingProof{Observation: suiteKagentObservation{Subject: suiteKagentSubject{CompositionDigest: "kagent"}}}},
		{ExecutionMode: "native-agentsessions", Sessions: &suiteSessionsPassingProof{Subject: suiteSessionsSubject{CompositionDigest: "sessions"}}},
	}
	for i, want := range []string{"orka", "kagent", "sessions"} {
		if got := cases[i].compositionDigest(); got != want {
			t.Fatal(got, want)
		}
	}
}

func TestProfiledProofLookupOfflineConsistency(t *testing.T) {
	store, a, b, ra, rb := profiledPassFixture(t)
	observed := ra.Subject.Runtime
	writeSyntheticProfileRecord(t, store, 1, ra)
	writeSyntheticProfileRecord(t, store, 2, rb)
	r, path, err := store.latest(t.Context(), a)
	if err != nil || r.compositionDigest() != a.CompositionDigest || !strings.HasSuffix(path, "00000000000000000001.json") {
		t.Fatal("other-profile latest replaced A", path, err)
	}
	if r.Subject.Runtime != observed {
		t.Fatal("observed runtime changed")
	}
	scan, err := store.scan(t.Context(), a)
	if err != nil || scan.sequence != 2 || scan.selectedSequence != 1 {
		t.Fatal("allocation/selection conflated", scan, err)
	}
	m, err := store.readPublicMetadata(t.Context(), a)
	if err != nil || m.Sequence != 1 {
		t.Fatal("wrong companion sequence", m, err)
	}
	if err := store.reserve(t.Context()); err != nil {
		t.Fatal(err)
	}
	if store.completion.name != "00000000000000000003.json" {
		t.Fatal("wrong global allocation")
	}
	if _, err := store.commit(t.Context(), mustProfileReceiptBytes(t, ra), a); !errors.Is(err, workspace.ErrProfileExecutionUnsupported) {
		t.Fatal("profiled pass registration accepted", err)
	}
	writeSyntheticProfileRecord(t, store, 3, ra)
	if r, _, err := store.latest(t.Context(), a); err != nil || r.compositionDigest() != a.CompositionDigest {
		t.Fatal(err)
	}
	if m, err := store.readPublicMetadata(t.Context(), a); err != nil || m.Sequence != 3 {
		t.Fatal("latest matching not selected", m, err)
	}
	if m, err := store.readPublicMetadata(t.Context(), b); err != nil || m.Sequence != 2 {
		t.Fatal("B sidecar lost", m, err)
	}
	// A synthetic B pass alone never authorizes A or fallback.
	if err := os.Remove(filepath.Join(store.partition, "00000000000000000001.json")); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(store.partition, "00000000000000000003.json")); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.latest(t.Context(), a); !errors.Is(err, errSuitePassMissing) {
		t.Fatal("cross-profile fallback", err)
	}
}
func mustProfileReceiptBytes(t *testing.T, r *suitePassingReceipt) []byte {
	t.Helper()
	raw, err := agentsuite.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
func TestProfiledProofCorruptionRefusesWithoutFallback(t *testing.T) {
	for _, kind := range []string{"composition", "member", "runtime", "laundering", "unsupported"} {
		t.Run(kind, func(t *testing.T) {
			store, a, _, ra, rb := profiledPassFixture(t)
			writeSyntheticProfileRecord(t, store, 1, ra)
			switch kind {
			case "composition":
				rb.Subject.CompositionDigest = "sha256:" + strings.Repeat("f", 64)
			case "member":
				rb.Subject.Member = "other"
			case "runtime":
				rb.Subject.Runtime.ControllerImage = "invalid"
			case "laundering":
				rb.Subject.CompositionDigest = a.CompositionDigest
			case "unsupported":
				a.Profile.ConfigurationABI = "unknown"
				if _, err := canonicalSuitePassingReceipt(t.Context(), ra, a); err == nil {
					t.Fatal("unsupported mapping accepted")
				}
				return
			}
			writeSyntheticProfileRecord(t, store, 2, rb)
			if _, _, err := store.latest(t.Context(), a); err == nil {
				t.Fatal("corrupt record fell back")
			}
		})
	}
}
func TestProfiledPushOfflineSelectionAndExecutionRefusals(t *testing.T) {
	store, a, _, _, _ := profiledPassFixture(t)
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	for _, opt := range []SuiteWorkspacePushOptions{
		{RuntimeProfile: "a"}, {RuntimeProfile: "missing", Unevaluated: true}, {Unevaluated: true}, {RuntimeProfile: "a", Platform: "linux/arm64", Unevaluated: true},
	} {
		opt.Workspace = store.workspace
		opt.Reference = "suite:v1"
		opt.Layout = filepath.Join(t.TempDir(), "layout")
		var out bytes.Buffer
		app := &App{Out: &out}
		if _, err := app.PushSuiteWorkspaceManaged(t.Context(), opt); err == nil || out.Len() != 0 {
			t.Fatal("invalid publication accepted", err)
		}
		if _, err := os.Stat(opt.Layout); !os.IsNotExist(err) {
			t.Fatal("refusal wrote destination")
		}
	}
	var out bytes.Buffer
	app := &App{Out: &out}
	layout := filepath.Join(t.TempDir(), "layout")
	got, err := app.PushSuiteWorkspaceManaged(t.Context(), SuiteWorkspacePushOptions{Workspace: store.workspace, Reference: "suite:v1", Layout: layout, RuntimeProfile: "b", Unevaluated: true})
	if err != nil || got.Descriptor.Digest.String() != a.Digest {
		t.Fatal("selection changed transferred S", err)
	}
	export, err := preflightSuitePassingExport(t.Context(), filepath.Join(privateSuiteDir(t), "receipt.json"), store.workspace, store.cache)
	if err != nil {
		t.Fatal(err)
	}
	defer export.Close()
	if err := export.publish(t.Context(), nil, a); !errors.Is(err, workspace.ErrProfileExecutionUnsupported) {
		t.Fatal("profiled private export not blocked", err)
	}
}

package app

import (
	"bytes"
	"encoding/json"
	"os"
	"regexp"
	"strings"
	"testing"
)

func agentOn(name, modelConfig string) agentStatus {
	var a agentStatus
	a.Metadata.Name = name
	a.Spec.Declarative.ModelConfig = modelConfig
	return a
}

func modelAt(name, baseURL, secret string) modelStatus {
	var m modelStatus
	m.Metadata.Name = name
	m.Spec.OpenAI.BaseURL = baseURL
	m.Spec.APIKeySecret = secret
	return m
}

func serverAt(name, url, secret string) toolServerStatus {
	var s toolServerStatus
	s.Metadata.Name = name
	s.Spec.URL = url
	if secret != "" {
		s.Spec.HeadersFrom = append(s.Spec.HeadersFrom, struct {
			ValueFrom struct {
				Type string `json:"type"`
				Name string `json:"name"`
			} `json:"valueFrom"`
		}{})
		s.Spec.HeadersFrom[0].ValueFrom.Type = "Secret"
		s.Spec.HeadersFrom[0].ValueFrom.Name = secret
	}
	return s
}

const (
	governedModelURL = "https://kaimahi-proxy.kaimahi.svc.cluster.local:8080/upstream/ollama/v1"
	governedToolURL  = "https://kaimahi-mcp-gateway.kaimahi:8081/upstream/kagent-tools/mcp"
	// What a seam written before the seams carried a certificate still says.
	// Governed, unchanged, and no longer reachable.
	staleModelURL = "http://kaimahi-proxy.kaimahi.svc.cluster.local:8080/upstream/ollama/v1"
	staleToolURL  = "http://kaimahi-mcp-gateway.kaimahi:8081/upstream/kagent-tools/mcp"
	directToolURL = "http://kagent-tool-server.kagent:8084/mcp"
)

// Every DNS form of the same Service is the plane; nothing else is, however
// much it looks like it.
func TestPlaneHostAcceptsEveryServiceFormAndNothingElse(t *testing.T) {
	governed := []string{
		"http://kaimahi-proxy.kaimahi:8080/upstream/ollama/v1",
		"http://kaimahi-proxy.kaimahi.svc:8080/v1",
		governedModelURL,
		"https://KAIMAHI-PROXY.KAIMAHI.svc.cluster.local/v1",
	}
	for _, url := range governed {
		if classifySeam(url, planeProxyService) != seamGoverned {
			t.Errorf("%q is the plane's proxy and was not counted as governed", url)
		}
	}
	direct := []string{
		"",
		"https://api.openai.com/v1",
		"http://kaimahi-proxy.other-namespace:8080/v1",
		"http://kaimahi-proxy.kaimahi.example.com/v1",
		"http://kaimahi-proxy-evil.kaimahi:8080/v1",
		"http://10.0.0.5:8080/v1",
	}
	for _, url := range direct {
		if classifySeam(url, planeProxyService) == seamGoverned {
			t.Errorf("%q is not the plane and was counted as governed", url)
		}
	}
}

func TestModelSeamsCountGovernedAndDirect(t *testing.T) {
	agents := []agentStatus{agentOn("hello-world", "governed-ollama"), agentOn("hello-tools", "ollama")}
	models := []modelStatus{
		modelAt("governed-ollama", governedModelURL, "kaimahi-governed-token"),
		modelAt("ollama", "", ""),
	}
	got := modelSeams(agents, models)
	if got.State != stateCounted || got.Total != 2 || got.Governed != 1 || got.Direct != 1 || got.Unresolved != 0 {
		t.Fatalf("model seams miscounted: %+v", got)
	}
}

// A dangling ModelConfig reference is `unknown`, never `direct`: the object
// that would answer the question is not on the cluster.
func TestModelSeamsRefuseToCallADanglingReferenceDirect(t *testing.T) {
	got := modelSeams([]agentStatus{agentOn("orphan", "gone")}, nil)
	if got.Direct != 0 || got.Unresolved != 1 {
		t.Fatalf("a dangling ModelConfig was resolved anyway: %+v", got)
	}
	if len(got.UnresolvedRefs) != 1 || !strings.Contains(got.UnresolvedRefs[0], "orphan→gone") {
		t.Errorf("the unresolved reference is not named: %v", got.UnresolvedRefs)
	}
	if got.Reason != "" {
		t.Errorf("`reason` belongs to an unknown population, not a counted one: %q", got.Reason)
	}
	if line := seamLine(got, "agents", "agent"); !strings.Contains(line, "1 unknown") {
		t.Errorf("the printed line hides the unknown: %q", line)
	}
}

// No agents at all is a known nothing — `none` — and not a zero
// pretending to be a count.
func TestEmptyPopulationsAreNoneNotZeroGoverned(t *testing.T) {
	if got := modelSeams(nil, nil); got.State != stateNone {
		t.Errorf("an empty agent population is not `none`: %+v", got)
	}
	if got := toolSeams(nil, ""); got.State != stateNone {
		t.Errorf("an empty tool-server population is not `none`: %+v", got)
	}
	if line := seamLine(toolSeams(nil, ""), "tool servers", "tool server"); strings.Contains(line, "0 of 0") {
		t.Errorf("an empty population printed as a count: %q", line)
	}
}

// The branch that matters most: a read that failed says so. "0 governed"
// would be a claim about a population nobody managed to look at.
func TestUnreadablePopulationIsUnknownNotZero(t *testing.T) {
	got := toolSeams(nil, `the server doesn't have a resource type "remotemcpservers"`)
	if got.State != stateUnknown || got.Governed != 0 || got.Total != 0 {
		t.Fatalf("an unreadable population was counted: %+v", got)
	}
	line := seamLine(got, "tool servers", "tool server")
	if !strings.HasPrefix(line, "unknown — ") || !strings.Contains(line, "remotemcpservers") {
		t.Errorf("the unknown line does not carry kubectl's reason: %q", line)
	}
}

func TestToolSeamsCountGovernedAndDirect(t *testing.T) {
	servers := []toolServerStatus{
		serverAt("kaimahi-tools", governedToolURL, "kaimahi-tools-token"),
		serverAt("kagent-tool-server", directToolURL, ""),
		serverAt("kagent-querydoc", "http://querydoc.kagent:8080/mcp", ""),
	}
	got := toolSeams(servers, "")
	if got.Total != 3 || got.Governed != 1 || got.Direct != 2 {
		t.Fatalf("tool seams miscounted: %+v", got)
	}
	if line := seamLine(got, "tool servers", "tool server"); line != "1 of 3 tool servers governed, 2 direct" {
		t.Errorf("unexpected line: %q", line)
	}
}

// The cluster state this release creates, and the one `kmx plane` sends an
// operator to `kmx status` to look at: a seam written before the seams
// carried a certificate. It is still GOVERNED — the plane enforces on the
// credential, not on the scheme — and its calls now fail closed, so a report
// that said only "governed" would be two reassuring lines about a plane no
// agent can reach.
func TestAGovernedSeamStillOnPlaintextIsCountedAndSaidOutLoud(t *testing.T) {
	models := []modelStatus{modelAt("governed-ollama", staleModelURL, "kaimahi-governed-token")}
	agents := []agentStatus{agentOn("hello-world", "governed-ollama")}
	got := modelSeams(agents, models)
	if got.Governed != 1 {
		t.Fatalf("a plaintext seam stopped being governed: %+v", got)
	}
	if got.Plaintext != 1 {
		t.Fatalf("a governed seam on plaintext was not counted: %+v", got)
	}
	line := seamLine(got, "agents", "agent")
	if !strings.Contains(line, "1 of 1 agent governed") {
		t.Errorf("the governed count changed: %q", line)
	}
	if !strings.Contains(line, "PLAINTEXT") || !strings.Contains(line, "fail closed") {
		t.Errorf("the line does not say the calls fail closed: %q", line)
	}

	servers := []toolServerStatus{serverAt("kaimahi-tools", staleToolURL, "kaimahi-tools-token")}
	tools := toolSeams(servers, "")
	if tools.Governed != 1 || tools.Plaintext != 1 {
		t.Fatalf("the tool seam was not counted the same way: %+v", tools)
	}
}

// And the seam this release writes says nothing extra, or the warning would
// be noise on every healthy cluster.
func TestASeamOverTLSCarriesNoPlaintextWarning(t *testing.T) {
	got := modelSeams(
		[]agentStatus{agentOn("hello-world", "governed-ollama")},
		[]modelStatus{modelAt("governed-ollama", governedModelURL, "kaimahi-governed-token")},
	)
	if got.Plaintext != 0 {
		t.Fatalf("a TLS seam was counted as plaintext: %+v", got)
	}
	if line := seamLine(got, "agents", "agent"); strings.Contains(line, "PLAINTEXT") {
		t.Errorf("a healthy cluster was warned about plaintext: %q", line)
	}
}

// Only the GOVERNED seams name a credential, and a named Secret that is not
// there is reported rather than left to fail at the next call.
func TestCredentialsCountOnlyWhatGovernedSeamsName(t *testing.T) {
	models := []modelStatus{
		modelAt("governed-ollama", governedModelURL, "kaimahi-governed-token"),
		modelAt("openai", "https://api.openai.com/v1", "openai-key"),
	}
	servers := []toolServerStatus{serverAt("kaimahi-tools", governedToolURL, "kaimahi-tools-token")}

	got := credentialSeams(models, servers, []string{"kaimahi-governed-token", "openai-key"}, "", "")
	if got.Required != 2 || got.Present != 1 || len(got.Missing) != 1 || got.Missing[0] != "kaimahi-tools-token" {
		t.Fatalf("credential population wrong: %+v", got)
	}
	if line := credentialLine(got); !strings.Contains(line, "missing: kaimahi-tools-token") {
		t.Errorf("the missing credential is not named: %q", line)
	}
	// The ungoverned upstream key is deliberately NOT required: it is not a
	// credential the plane issued and status makes no claim about it.
	if got.Required == 3 {
		t.Error("an ungoverned seam's own key was counted as a governed credential")
	}
}

func TestCredentialsAreNoneWhenNoGovernedSeamNamesOne(t *testing.T) {
	got := credentialSeams([]modelStatus{modelAt("ollama", "", "")}, nil, nil, "", "")
	if got.State != stateNone {
		t.Fatalf("no governed seam should be `none`, got %+v", got)
	}
	if credentialLine(got) != "none — no governed seam names one" {
		t.Errorf("unexpected line: %q", credentialLine(got))
	}
}

// An unreadable tool-seam population must not throw away the model half's
// answer. A token that is genuinely, knowably missing is worth more than a
// tidy `unknown` — so the count is published and MARKED PARTIAL.
func TestCredentialsStayPartialRatherThanLoseTheCountableHalf(t *testing.T) {
	d := &statusData{serverErr: "connection refused"}
	d.models.Items = []modelStatus{modelAt("governed-ollama", governedModelURL, "kaimahi-governed-token")}
	got := d.governanceOf().Credentials
	if got.State != stateCounted || !got.Partial {
		t.Fatalf("expected a partial count, got %+v", got)
	}
	if len(got.Missing) != 1 || got.Missing[0] != "kaimahi-governed-token" {
		t.Fatalf("the knowably-missing credential was dropped: %+v", got)
	}
	if line := credentialLine(got); !strings.Contains(line, "partial") {
		t.Errorf("the line does not say the count is partial: %q", line)
	}
}

// A Secret listing that FAILED is the case where nothing can be said. An
// empty list would otherwise become a confident accusation naming Secrets
// that may well exist.
func TestCredentialsAreUnknownWhenTheSecretsCouldNotBeListed(t *testing.T) {
	models := []modelStatus{modelAt("governed-ollama", governedModelURL, "kaimahi-governed-token")}
	got := credentialSeams(models, nil, nil, "Error from server (Forbidden): secrets is forbidden", "")
	if got.State != stateUnknown {
		t.Fatalf("expected unknown, got %+v", got)
	}
	if len(got.Missing) != 0 {
		t.Errorf("an unreadable listing accused specific Secrets: %+v", got.Missing)
	}
}

// The no-plane branch: the counts still work, and the output SAYS the plane
// is absent rather than reporting a bare zero an operator would read as
// "you have not got round to it yet".
func TestGovernanceWithNoPlaneSaysSoAndStillCounts(t *testing.T) {
	d := &statusData{}
	d.agents.Items = []agentStatus{agentOn("hello-world", "ollama")}
	d.models.Items = []modelStatus{modelAt("ollama", "", "")}
	d.servers.Items = []toolServerStatus{serverAt("kagent-tool-server", directToolURL, "")}

	g := d.governanceOf()
	if g.Plane.State != stateNone {
		t.Fatalf("no plane pods should read as none: %+v", g.Plane)
	}
	var out bytes.Buffer
	writeGovernance(&out, g)
	text := out.String()
	for _, want := range []string{"not installed", "0 of 1 agent governed, 1 direct", "0 of 1 tool server governed, 1 direct", "none — no governed seam names one"} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %q in:\n%s", want, text)
		}
	}
}

// The cannot-tell branch: an unreadable plane namespace is not an absent
// plane, and status must not say "not installed" about a namespace it could
// not read.
func TestGovernanceCannotTellIsNotNotInstalled(t *testing.T) {
	d := &statusData{planeErr: "Error from server (Forbidden): pods is forbidden"}
	g := d.governanceOf()
	if g.Plane.State != stateUnknown {
		t.Fatalf("an unreadable plane namespace was reported as absent: %+v", g.Plane)
	}
	var out bytes.Buffer
	writeGovernance(&out, g)
	if text := out.String(); !strings.Contains(text, "plane:        unknown — Error from server (Forbidden)") || strings.Contains(text, "not installed") {
		t.Errorf("cannot-tell was conflated with not-installed:\n%s", text)
	}
}

func TestGovernanceWithAPlaneCountsGovernedSeams(t *testing.T) {
	d := &statusData{}
	d.agents.Items = []agentStatus{agentOn("hello-world", "governed-ollama"), agentOn("hello-tools", "ollama")}
	d.models.Items = []modelStatus{
		modelAt("governed-ollama", governedModelURL, "kaimahi-governed-token"),
		modelAt("ollama", "", ""),
	}
	d.servers.Items = []toolServerStatus{
		serverAt("kaimahi-tools", governedToolURL, "kaimahi-tools-token"),
		serverAt("kagent-tool-server", directToolURL, ""),
	}
	d.secrets = []string{"kaimahi-governed-token", "kaimahi-tools-token"}
	d.planeThere, d.planeDesired, d.planeReady = true, 1, 1

	var out bytes.Buffer
	writeGovernance(&out, d.governanceOf())
	text := out.String()
	for _, want := range []string{"installed (1/1 replicas ready)", "1 of 2 agents governed, 1 direct", "1 of 2 tool servers governed, 1 direct", "2 of 2 present"} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %q in:\n%s", want, text)
		}
	}
}

func TestFirstLineKeepsKubectlsOwnComplaint(t *testing.T) {
	if got := firstLine("exit status 1: Error from server (NotFound)\ntrailing detail"); got != "exit status 1: Error from server (NotFound)" {
		t.Errorf("unexpected reason: %q", got)
	}
}

// #37 added finite timeouts to the status reads and its file is gone; the
// reasoning is not. Every read status makes carries one, or an unreachable
// API server turns the command people run when something is wrong into a
// command that hangs.
func TestEveryStatusReadIsBounded(t *testing.T) {
	source, err := os.ReadFile("status.go")
	if err != nil {
		t.Fatal(err)
	}
	// The vacuity guard is derived from the file, not a remembered count. A
	// floor spelled `< 4` is a second copy of how many reads status.go has,
	// and the first legitimate edit that adds or removes one fails on the
	// number rather than on anything the test is about. What actually has to
	// hold is that the scan is still looking at the right helper and still
	// finding reads in it.
	calls := regexp.MustCompile(`kubectlCapture\([^)]*\)`).FindAllString(string(source), -1)
	if len(calls) == 0 {
		t.Fatal("status.go makes no kubectlCapture calls at all — this scan is looking for a helper that " +
			"is no longer there, so it would pass while proving nothing")
	}
	var reads []string
	for _, call := range calls {
		if strings.Contains(call, `"get"`) {
			reads = append(reads, call)
		}
	}
	if len(reads) == 0 {
		t.Fatalf("none of status.go's %d kubectlCapture calls is a read — the scan is passing vacuously", len(calls))
	}
	for _, read := range reads {
		if !strings.Contains(read, "statusRequestTimeout") {
			t.Errorf("an unbounded status read: %s", read)
		}
	}
}

// A fully qualified Service name may carry the root's trailing dot. Reading
// it as a different host would overstate the ungoverned count.
func TestPlaneHostAcceptsTheRootDot(t *testing.T) {
	if classifySeam("http://kaimahi-proxy.kaimahi.svc.cluster.local.:8080/v1", planeProxyService) != seamGoverned {
		t.Error("a fully qualified name with the root dot was not recognised as the plane")
	}
}

// The finding, in the wire format: a population nobody could read must not
// hand a parser a zero it did not count.
func TestUnknownPopulationsPublishNoCounts(t *testing.T) {
	d := &statusData{serverErr: "Forbidden", planeErr: "Forbidden", secretErr: "Forbidden"}
	raw, err := json.Marshal(d.governanceOf())
	if err != nil {
		t.Fatal(err)
	}
	var generic map[string]map[string]any
	if err := json.Unmarshal(raw, &generic); err != nil {
		t.Fatal(err)
	}
	for _, population := range []string{"toolSeams", "credentials", "plane"} {
		got := generic[population]
		if got["state"] != stateUnknown {
			t.Fatalf("%s is not unknown: %v", population, got)
		}
		for _, absent := range []string{"governed", "direct", "total", "required", "present", "ready", "pods"} {
			if _, ok := got[absent]; ok {
				t.Errorf("%s published %q alongside state=unknown: %v", population, absent, got)
			}
		}
	}
	// A counted population still publishes its zeros — there they are a fact.
	counted := governance{ModelSeams: seamPopulation{State: stateNone}}
	raw, _ = json.Marshal(counted)
	if !strings.Contains(string(raw), `"governed":0`) {
		t.Errorf("a known-zero population dropped its counts: %s", raw)
	}
}

// A plane scaled to zero is INSTALLED and DOWN, not absent. Telling that
// operator to run `kmx plane` would be a false absence and the wrong
// instruction — and it would sit next to "1 of 2 agents governed", which
// only makes sense if a plane exists.
func TestAPlaneWithNoPodsIsInstalledAndDownNotAbsent(t *testing.T) {
	// Scaled to zero, with Postgres still running in the same namespace —
	// the case that would read "1/1 pods ready" if the namespace's pods
	// were counted instead of the proxy Deployment's replicas.
	var postgres podStatus
	postgres.Metadata.Name = "kaimahi-postgres-0"
	postgres.Status.Conditions = []statusCondition{{Type: "Ready", Status: "True"}}
	d := &statusData{planeThere: true, planeDesired: 0, planeReady: 0}
	d.planePods.Items = []podStatus{postgres}
	g := d.governanceOf()
	if g.Plane.State != stateInstalled || g.Plane.Ready != 0 {
		t.Fatalf("a scaled-to-zero plane was not reported as installed and down: %+v", g.Plane)
	}
	var out bytes.Buffer
	writeGovernance(&out, g)
	text := out.String()
	if !strings.Contains(text, "SCALED TO ZERO") || strings.Contains(text, "not installed") {
		t.Errorf("a plane that is down reads as never deployed:\n%s", text)
	}

	// Desired replicas that are all unready is the other half of down.
	down := (&statusData{planeThere: true, planeDesired: 2}).governanceOf()
	out.Reset()
	writeGovernance(&out, down)
	if !strings.Contains(out.String(), "installed but DOWN (0/2 replicas ready)") {
		t.Errorf("an unready plane reads as healthy:\n%s", out.String())
	}
}

// A seam whose destination cannot be read is unresolved, not direct.
// Counting it direct would be a confident claim built from a failed read.
func TestAnUnreadableSeamURLIsUnresolvedNotDirect(t *testing.T) {
	// A schemeless authority: Go parses this as a scheme with an opaque
	// body and no host at all.
	servers := []toolServerStatus{serverAt("bare", "kaimahi-mcp-gateway.kaimahi:8081/mcp", "")}
	got := toolSeams(servers, "")
	if got.Direct != 0 || got.Unresolved != 1 {
		t.Fatalf("an unreadable URL was classified anyway: %+v", got)
	}
	if len(got.UnresolvedRefs) != 1 || !strings.Contains(got.UnresolvedRefs[0], "bare") {
		t.Errorf("the unresolved server is not named: %v", got.UnresolvedRefs)
	}
	// A RemoteMCPServer with no URL at all is the same kind of unknown.
	if got := toolSeams([]toolServerStatus{serverAt("empty", "", "")}, ""); got.Unresolved != 1 {
		t.Errorf("a URL-less tool server was classified: %+v", got)
	}
}

// The cluster domain is not always cluster.local, and a cluster built with
// another one resolves the same Service. Refusing it would report every
// governed seam on that cluster as direct.
func TestAnotherClusterDomainIsNotClaimedEitherWay(t *testing.T) {
	if classifySeam("http://kaimahi-proxy.kaimahi.svc.cluster.internal:8080/v1", planeProxyService) != seamUnresolved {
		t.Error("a non-default cluster domain was claimed rather than left unresolved")
	}
	// `svc` in the third label is NOT enough on its own — see
	// TestAnExternalHostWearingTheServiceNameIsNeverGoverned.
	if classifySeam("http://kaimahi-proxy.kaimahi.evil.com/v1", planeProxyService) == seamGoverned {
		t.Error("an external host wearing the Service's first two labels was counted as governed")
	}
}

// The one direction of error that matters: a DIRECT seam reported as
// governed. `kaimahi-proxy.kaimahi.svc.evil.com` is a registrable domain
// anyone can own, and it wears the Service's first three labels. kmx cannot
// tell it from the same Service under a non-default cluster domain, so it
// claims neither.
func TestAnExternalHostWearingTheServiceNameIsNeverGoverned(t *testing.T) {
	for _, host := range []string{
		"http://kaimahi-proxy.kaimahi.svc.evil.com/v1",
		"http://kaimahi-proxy.kaimahi.svc.cluster.internal:8080/v1",
	} {
		if got := classifySeam(host, planeProxyService); got != seamUnresolved {
			t.Errorf("%q classified as %q — a host kmx cannot place must be unresolved", host, got)
		}
	}
	// The forms the cluster itself produces are still governed.
	for _, host := range []string{
		"http://kaimahi-proxy.kaimahi:8080/v1",
		"http://kaimahi-proxy.kaimahi.svc:8080/v1",
		"http://kaimahi-proxy.kaimahi.svc.cluster.local./v1",
	} {
		if got := classifySeam(host, planeProxyService); got != seamGoverned {
			t.Errorf("%q classified as %q — the cluster's own DNS forms must be governed", host, got)
		}
	}
	// And a plain lookalike is still plainly direct.
	if got := classifySeam("http://kaimahi-proxy.kaimahi.evil.com/v1", planeProxyService); got != seamDirect {
		t.Errorf("an unrelated host classified as %q", got)
	}
}

// The Runtime line and the Governance section must decide the plane's
// presence from the same fact, or a scaled-to-zero plane gets told to
// install what it already has.
func TestTheTwoPlaneLinesAgree(t *testing.T) {
	d := &statusData{planeThere: true, planeDesired: 0}
	if d.governanceOf().Plane.State != stateInstalled {
		t.Fatal("the Governance section does not see the Deployment")
	}
	source, err := os.ReadFile("status.go")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(source), "case data.planeThere || len(planePods.Items) > 0:") {
		t.Error("the Runtime governance line no longer decides presence from the Deployment")
	}
}

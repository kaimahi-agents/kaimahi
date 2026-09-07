// Package seam holds what kmx knows about each upstream credential it can
// capture: which token that upstream takes, what shape its subject has, which
// Secret the gateway reads it from, and — the part that matters — what can be
// PROVEN about a token before it is stored.
//
// This package is the one place in kmx that deliberately handles credential
// material. Everywhere else refuses it: the agent wizard screens its input
// against credential shapes, and the blueprint parser refuses
// credential-shaped keys before a document is decoded. Those screens stay
// exactly as they are. The exception exists because the alternative was
// worse: capturing a credential was the one step of the journey that still
// required a checkout, and telling an operator to clone a repository in order
// to hand a token to a cluster is not a smaller trust ask than this.
//
// The rules the exception carries live in the caller (the value is read from
// a terminal and nowhere else, never echoed, never written to a file, never
// in argv). What lives HERE is the other half of the bargain: a prompt that
// stores an unvetted token would be worse than the shell scripts it replaces,
// because it would be faster at getting a broken credential into a cluster.
//
// Every validator is fail-closed in the same way: it accepts only a
// well-formed positive, and anything else stores nothing. And every validator
// is honest about its limits — where a check cannot be made soundly it is
// REPORTED rather than guessed, and the report says whose job the fact is.
// Two of those limits were checks once, and both refused correct tokens; the
// reasoning is written out at each site so neither comes back.
package seam

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"
)

// Report is what a validator established, printed to the operator after the
// credential is stored. It carries no credential material.
type Report struct {
	// Proven are the facts the validator established by asking the upstream.
	Proven []string
	// Unproven are facts this path cannot establish, said plainly with
	// whose job each one is. A silence here would read as a guarantee.
	Unproven []string
	// Deadline is the one line about when the credential dies.
	Deadline string
}

// Seam is one upstream credential kmx can capture.
type Seam struct {
	// Name is what an operator types, and it is the gateway's own upstream
	// name — the entry in the plane's upstream table this credential is
	// injected on.
	Name string
	// Summary is the one-line description in `kmx credential capture --help`.
	Summary string
	// SubjectName names what the positional argument is ("repository",
	// "organization"), so a refusal can say which thing was malformed.
	SubjectName string
	// SubjectHint is the shape of that argument, shown in usage.
	SubjectHint string
	// Prompt is the line printed above the (unechoed) input.
	Prompt string
	// Guidance is what to do when there is no token yet: which token this
	// upstream takes, and how to get one.
	Guidance string
	// Secret and Key are where the gateway reads this credential from.
	Secret string
	Key    string
	// HostedEgress records that this upstream is on the internet, so
	// capturing its credential is also the moment the gateway needs the
	// hosted-egress allowance. Every seam here is hosted today; the field
	// exists so an in-cluster seam is a table entry and not a special case.
	HostedEgress bool

	subject  *regexp.Regexp
	validate func(Env, string, []byte) (Report, error)
}

// Env is where the validators reach. The endpoints are fields rather than
// constants so the refusals can be exercised against a local test server:
// this project's CI holds no credential of any kind and never will, so every
// proof about a refusal has to be reproducible without one.
type Env struct {
	Client    *http.Client
	GitHubAPI string
	ADOBase   string
	Now       func() time.Time
}

// DefaultEnv reaches the real upstreams.
//
// The client refuses redirects. Go's own client strips Authorization across
// hosts but not custom headers, and a keyed call that follows a redirect is a
// keyed call to a host nobody vetted; refusing is the same rule the capture
// scripts followed by passing no -L to curl.
func DefaultEnv() Env {
	return Env{
		Client: &http.Client{
			Timeout: 60 * time.Second,
			CheckRedirect: func(req *http.Request, _ []*http.Request) error {
				return fmt.Errorf("refusing to follow a redirect to %s on a call carrying the credential", req.URL.Host)
			},
		},
		GitHubAPI: "https://api.github.com",
		ADOBase:   "https://mcp.dev.azure.com",
		Now:       time.Now,
	}
}

func (e Env) client() *http.Client {
	if e.Client != nil {
		return e.Client
	}
	return DefaultEnv().Client
}

func (e Env) now() time.Time {
	if e.Now != nil {
		return e.Now()
	}
	return time.Now()
}

// A GitHub repository, anchored: the value is interpolated into a URL and
// must be exactly owner/name.
var repoRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9-]{0,38}/[A-Za-z0-9._-]{1,100}$`)

// An Azure DevOps organization name, anchored for the same reason. The name
// itself is an Azure identifier: it is passed in, used to build one URL, and
// never written down (scripts/check-no-azure-ids.sh).
var orgRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9-]{0,62}$`)

const githubGuidance = `A FINE-GRAINED personal access token, scoped to ONE repository:
  github.com/settings/personal-access-tokens`

var seams = []*Seam{
	{
		Name:        "github",
		Summary:     "GitHub's hosted MCP server, read-only",
		SubjectName: "repository",
		SubjectHint: "owner/name",
		Prompt:      "Paste the fine-grained GitHub token (github_pat_...), then press Enter",
		Guidance: githubGuidance + `
  Issues: Read, Pull requests: Read (Metadata comes along). Nothing else:
  this seam only reads, and least privilege is the point of custody.`,
		Secret:       "kaimahi-github-pat",
		Key:          "token",
		HostedEgress: true,
		subject:      repoRE,
		validate:     validateGitHub,
	},
	{
		Name:        "github-release",
		Summary:     "GitHub, for an agent that cuts a release branch and dispatches builds",
		SubjectName: "repository",
		SubjectHint: "owner/name",
		Prompt:      "Paste the fine-grained GitHub token (github_pat_...), then press Enter",
		Guidance: githubGuidance + `
  Contents: Read and write, Pull requests: Read, Actions: Read and write.

  This token can change a real repository, so it gets its own Secret and its
  own allowlist rather than sharing the read-only seam's. Note what the token
  cannot do for you: GitHub has no permission that grants creating a ref
  without also granting deleting one, so the token is not what stops a
  destructive operation — the gateway is (docs/release-agent.md).`,
		Secret:       "kaimahi-release-pat",
		Key:          "token",
		HostedEgress: true,
		subject:      repoRE,
		validate:     validateGitHub,
	},
	{
		Name:        "ado",
		Summary:     "Azure DevOps, through Microsoft's hosted MCP server",
		SubjectName: "organization",
		SubjectHint: "<organization>",
		Prompt:      "Paste the Azure DevOps access token (eyJ...), then press Enter",
		Guidance: `An Entra access token, NOT a personal access token: the hosted server
  advertises Microsoft Entra ID as its only authorization server and accepts
  nothing else. Mint one, then copy it:

      az account get-access-token --scope https://mcp.dev.azure.com/.default \
         --query accessToken -o tsv

  If that command fails, your tenant has not consented the Azure CLI to the
  Azure DevOps MCP application and you need an app registration of your own.
  The token lives about an hour, which is why this is a capture you repeat.`,
		Secret:       "kaimahi-ado-token",
		Key:          "token",
		HostedEgress: true,
		subject:      orgRE,
		validate:     validateADO,
	},
}

// Lookup resolves a seam name. An unknown name lists what exists rather than
// guessing: a typo here would otherwise send a credential to a Secret the
// gateway does not read, which fails much later and looks like a bad token.
func Lookup(name string) (*Seam, error) {
	for _, s := range seams {
		if s.Name == name {
			return s, nil
		}
	}
	return nil, fmt.Errorf("no upstream named %q. kmx can capture a credential for: %s",
		name, strings.Join(Names(), ", "))
}

// Names lists the seams, in table order.
func Names() []string {
	out := make([]string, 0, len(seams))
	for _, s := range seams {
		out = append(out, s.Name)
	}
	return out
}

// All returns the seam table, for help text and for tests that assert every
// entry is complete.
func All() []*Seam { return seams }

// CheckSubject validates the positional argument before anything else
// happens — in particular before a credential is read, so a typo costs a
// retype rather than a token.
func (s *Seam) CheckSubject(subject string) error {
	if strings.TrimSpace(subject) == "" {
		return fmt.Errorf("kmx credential capture %s needs a %s: kmx credential capture %s %s",
			s.Name, s.SubjectName, s.Name, s.SubjectHint)
	}
	if !s.subject.MatchString(subject) {
		return fmt.Errorf("%q is not a %s (want %s)", subject, s.SubjectName, s.SubjectHint)
	}
	return nil
}

// Validate proves what can be proven about the token, against the upstream
// itself. A non-nil error means nothing may be stored.
func (s *Seam) Validate(env Env, subject string, token []byte) (Report, error) {
	return s.validate(env, subject, token)
}

// ---- GitHub ---------------------------------------------------------------

var fineGrainedRE = regexp.MustCompile(`^github_pat_[A-Za-z0-9_]{20,}$`)

func validateGitHub(env Env, repo string, token []byte) (Report, error) {
	var report Report
	if !fineGrainedRE.Match(token) {
		return report, fmt.Errorf("that is not a fine-grained GitHub token.\n" +
			"  Nothing was stored. A classic token or an OAuth token cannot be scoped to a\n" +
			"  single repository, so this path does not accept one. Create a fine-grained\n" +
			"  token at github.com/settings/personal-access-tokens")
	}

	req, err := http.NewRequest(http.MethodGet, env.GitHubAPI+"/repos/"+repo, nil)
	if err != nil {
		return report, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	// The one place the token becomes a string: an HTTP header value is one,
	// and there is no way to hand net/http bytes instead. It is built here,
	// used once, and nothing keeps a reference to it afterwards.
	req.Header.Set("Authorization", "Bearer "+string(token))

	resp, err := env.client().Do(req)
	if err != nil {
		return report, fmt.Errorf("could not reach GitHub to check this token: %w\n"+
			"  Nothing was stored.", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return report, fmt.Errorf("could not read GitHub's answer: %w\n  Nothing was stored.", err)
	}

	if resp.StatusCode != http.StatusOK {
		return report, fmt.Errorf("GitHub would not let this token read %s.\n"+
			"  Nothing was stored. A fine-grained token has to list that repository under\n"+
			"  \"Repository access\" — and GitHub answers a repository the token may not\n"+
			"  reach exactly as it answers one that does not exist, so check both.", repo)
	}

	// A classic token announces its scopes; a fine-grained one does not. The
	// prefix check above already refused the classic FORM, and this refuses a
	// token that carries scopes whatever it looks like.
	if strings.TrimSpace(resp.Header.Get("X-OAuth-Scopes")) != "" {
		return report, fmt.Errorf("this token carries OAuth scopes, which makes it a classic token.\n" +
			"  Nothing was stored. Use a fine-grained token: a classic one cannot be\n" +
			"  limited to a single repository.")
	}

	var answer struct {
		FullName string `json:"full_name"`
	}
	if err := json.Unmarshal(body, &answer); err != nil {
		return report, fmt.Errorf("GitHub's answer was not the repository JSON this check expects.\n" +
			"  Nothing was stored.")
	}
	if !strings.EqualFold(answer.FullName, repo) {
		return report, fmt.Errorf("GitHub answered about %q, not %q.\n"+
			"  Nothing was stored.", answer.FullName, repo)
	}
	report.Proven = append(report.Proven,
		fmt.Sprintf("it is a fine-grained token, and it reads %s", repo))

	// The deadline, said now rather than when it bites — the rule this
	// project's own credentials follow, applied to a borrowed one.
	//
	// REPORTED, not enforced. An absent header means "this response did not
	// carry one", not "this token never expires", and a check here that read
	// the silence as a fact refused valid tokens. Once was enough.
	expiry := strings.TrimSpace(resp.Header.Get("Github-Authentication-Token-Expiration"))
	if expiry == "" {
		report.Deadline = "NOT REPORTED — check it yourself. An unattended credential that never\n" +
			"  dies is one nobody ever revokes."
	} else {
		report.Deadline = "expires " + expiry
	}

	// The limit worth writing down, because a check used to sit here and was
	// wrong: it asked GET /user/repos and refused a token listing more than
	// one repository. That endpoint answers by the USER's affiliations, not
	// the token's scope, so for a member of a large organization it returns
	// thousands whatever the token can reach — it refused correctly-scoped
	// tokens and proved nothing about wrong ones. There is no sound
	// replacement: GitHub exposes no endpoint reporting a fine-grained
	// token's repository grant, and a negative control cannot tell "the token
	// cannot reach it" from "it can, because the repository is public".
	report.Unproven = append(report.Unproven,
		"which permissions you granted, and whether the token reaches only this one\n"+
			"  repository. GitHub exposes neither, so both are yours to get right when you\n"+
			"  create the token. The plane narrows the blast radius anyway: the allowlist\n"+
			"  binds the tools to this owner and repository and the gateway denies the rest.")
	return report, nil
}

// ---- Azure DevOps ---------------------------------------------------------

var jwtRE = regexp.MustCompile(`^eyJ[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+$`)

func validateADO(env Env, org string, token []byte) (Report, error) {
	var report Report
	if !jwtRE.Match(token) {
		return report, fmt.Errorf("that is not an access token.\n" +
			"  Nothing was stored. The hosted Azure DevOps MCP server advertises Microsoft\n" +
			"  Entra ID as its only authorization server: it takes an Entra access token,\n" +
			"  not a personal access token. Mint one with:\n" +
			"    az account get-access-token --scope https://mcp.dev.azure.com/.default \\\n" +
			"       --query accessToken -o tsv")
	}

	// The claims, read locally: no network, and only the audience and the
	// expiry leave this step. The audience is a public identifier.
	claims, err := readClaims(token)
	if err != nil {
		return report, err
	}

	// Audience: REPORTED, not enforced, and the reason matters because a
	// check here refused a correct token. Entra writes `aud` two ways
	// depending on the target application's token version — the resource URI
	// for v2.0, the application's client-id for v1.0 — and asking for
	// --scope https://mcp.dev.azure.com/.default can legitimately give
	// either, so accepting only the first refused a token minted by the exact
	// command this seam recommends. The identifier form cannot be
	// allowlisted here either: an application id is an Azure identifier and
	// this repository carries none. Nothing is lost, because the check below
	// asks the server, which is the only authority on whether it accepts the
	// token.
	if aud := claims.audience(); aud != "" {
		report.Proven = append(report.Proven, "its audience is "+aud)
	}
	report.Unproven = append(report.Unproven,
		"that the audience is the right one, from the token alone. Microsoft writes it\n"+
			"  two ways for the same request, so the server below is asked instead — which\n"+
			"  is the only authority on it anyway.")

	exp, ok := claims.expiry()
	if !ok {
		return report, fmt.Errorf("this token carries no expiry claim.\n" +
			"  Nothing was stored. A credential with no deadline is one nobody revokes,\n" +
			"  and an access token that reports none is not the token this seam expects.")
	}
	left := exp.Sub(env.now())
	if left <= 0 {
		return report, fmt.Errorf("this token expired %d minutes ago.\n"+
			"  Nothing was stored. Mint a fresh one:\n"+
			"    az account get-access-token --scope https://mcp.dev.azure.com/.default \\\n"+
			"       --query accessToken -o tsv", int(-left/time.Minute))
	}
	minutes := int(left / time.Minute)
	report.Deadline = fmt.Sprintf("it dies in about %d minutes. Re-run this before a session rather than\n"+
		"  during one; a call made after it dies fails closed with the server's own\n"+
		"  refusal, audited, and nothing is left half done.", minutes)

	// The server accepts it. A well-formed POSITIVE: an MCP `initialize` that
	// comes back with a JSON-RPC result. Anything else is refused, the body
	// parsed rather than trusted, because a filtering proxy's HTML answer must
	// never read as success.
	if err := adoInitialize(env, org, token); err != nil {
		return report, err
	}
	report.Proven = append(report.Proven, "the hosted Azure DevOps server accepts it for "+org)
	return report, nil
}

const adoInitializeBody = `{"jsonrpc":"2.0","id":1,"method":"initialize","params":` +
	`{"protocolVersion":"2025-06-18","capabilities":{},` +
	`"clientInfo":{"name":"kmx-credential-capture","version":"1"}}}`

func adoInitialize(env Env, org string, token []byte) error {
	req, err := http.NewRequest(http.MethodPost, env.ADOBase+"/"+org, strings.NewReader(adoInitializeBody))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("Authorization", "Bearer "+string(token))

	resp, err := env.client().Do(req)
	if err != nil {
		return fmt.Errorf("could not reach the hosted Azure DevOps server to check this token: %w\n"+
			"  Nothing was stored.", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))

	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusUnauthorized:
		return fmt.Errorf("the hosted Azure DevOps server does not accept this token.\n" +
			"  Nothing was stored. The organization has to be backed by a Microsoft Entra\n" +
			"  tenant — a standalone Microsoft-account organization is not supported by the\n" +
			"  remote server — and your tenant has to have consented the client that minted\n" +
			"  this token.")
	case http.StatusNotFound:
		return fmt.Errorf("the hosted Azure DevOps server has no organization named %q.\n"+
			"  Nothing was stored.", org)
	default:
		return fmt.Errorf("the hosted Azure DevOps server refused the check, and not in a way\n"+
			"  this path recognises. Nothing was stored. It said:\n  %s", excerpt(body))
	}

	// The endpoint may answer as a server-sent-event stream; the last data:
	// frame carries the JSON-RPC message when it does.
	payload := strings.TrimSpace(string(body))
	for _, line := range strings.Split(string(body), "\n") {
		if after, found := strings.CutPrefix(strings.TrimSpace(line), "data:"); found {
			payload = strings.TrimSpace(after)
		}
	}
	var answer struct {
		Result json.RawMessage `json:"result"`
		Error  json.RawMessage `json:"error"`
	}
	if err := json.Unmarshal([]byte(payload), &answer); err != nil {
		return fmt.Errorf("the hosted Azure DevOps server answered the check with something that is\n" +
			"  not a protocol message. Nothing was stored.")
	}
	if len(answer.Error) > 0 || len(answer.Result) == 0 {
		return fmt.Errorf("the hosted Azure DevOps server refused the opening handshake.\n"+
			"  Nothing was stored. It said:\n  %s", excerpt([]byte(payload)))
	}
	return nil
}

// adoClaims is the sliver of an access token's payload this check reads.
// Nothing else is decoded, which is what keeps a credential's contents from
// reaching a log by accident.
type adoClaims struct {
	Aud json.RawMessage `json:"aud"`
	Exp json.RawMessage `json:"exp"`
}

func readClaims(token []byte) (adoClaims, error) {
	var claims adoClaims
	parts := strings.Split(string(token), ".")
	if len(parts) != 3 {
		return claims, fmt.Errorf("this token is not shaped like an access token. Nothing was stored.")
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return claims, fmt.Errorf("this token's payload is not readable. Nothing was stored.")
	}
	if err := json.Unmarshal(raw, &claims); err != nil {
		return claims, fmt.Errorf("this token's payload is not readable JSON. Nothing was stored.")
	}
	return claims, nil
}

func (c adoClaims) audience() string {
	var s string
	if err := json.Unmarshal(c.Aud, &s); err == nil {
		return s
	}
	return ""
}

func (c adoClaims) expiry() (time.Time, bool) {
	var seconds int64
	if err := json.Unmarshal(c.Exp, &seconds); err != nil || seconds <= 0 {
		return time.Time{}, false
	}
	return time.Unix(seconds, 0), true
}

// excerpt keeps an upstream's own words — an expired credential and an
// unreachable host are different problems and the upstream is the only thing
// that can tell them apart — without pasting a page of HTML into a terminal.
func excerpt(body []byte) string {
	text := strings.TrimSpace(string(body))
	text = strings.Join(strings.Fields(text), " ")
	if len(text) > 300 {
		text = text[:300] + "…"
	}
	if text == "" {
		text = "(nothing)"
	}
	return text
}

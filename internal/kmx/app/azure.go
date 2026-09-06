package app

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/lift"
)

// The Azure CLI is a prerequisite and is deliberately NOT fetched.
//
// kmx downloads kind, kubectl and Helm when a machine lacks them, pinned and
// checksum-verified, because each is a single binary and the alternative is a
// first agent that is four downloads away. The Azure CLI is neither of those
// things. It is a Python distribution rather than one file, it has a real
// package story on every platform it supports, and it must be signed in
// interactively before it is useful — so fetching it would save an operator
// no step at all. The decisive reason is the last one: `az login` puts cloud
// credentials on the machine, and a tool that quietly installs the thing that
// then holds your subscription access is a different kind of surprise from
// one that drops a kubectl in a cache.
var depAz = dependency{"az", "to reach Azure: the cluster, the registry and the monitoring workspaces",
	"https://learn.microsoft.com/cli/azure/install-azure-cli", []string{"version"}, false}

// PyYAML is the one prerequisite the managed path needs and the local path
// does not: rendering the plane's manifest for a registry-backed cluster
// parses proxy.yaml rather than pattern-matching it. Checked here, at the
// start, because the alternative is discovering it after a resource group, a
// registry and a cluster already exist.
func (a *App) preflightManifestRenderer() error {
	if _, err := a.Run.Capture("python3", "-c", "import yaml"); err != nil {
		return fmt.Errorf(`kmx lift: python3 with PyYAML is required on the managed path.

  The plane's manifest is rendered for your registry by parsing it rather
  than by pattern-matching it, and that parser is PyYAML. The local path
  never renders anything, which is why this is the one prerequisite kind
  does not share.

  install: pip install pyyaml`)
	}
	return nil
}

// account is who the CLI is signed in as and which subscription it will act
// on. Both go in the banner, and neither is the subscription id: the id is an
// identifier this project keeps out of terminals and transcripts, and the
// name plus the signed-in user is what a human actually checks against.
type account struct {
	Name string `json:"name"`
	ID   string `json:"id"`
	User struct {
		Name string `json:"name"`
	} `json:"user"`
}

func (a *App) azAccount() (account, error) {
	var acct account
	out, err := a.Run.Capture("az", "account", "show", "-o", "json")
	if err != nil {
		return acct, fmt.Errorf("kmx lift: the Azure CLI is not signed in — run: az login")
	}
	if err := json.Unmarshal([]byte(out), &acct); err != nil {
		return acct, fmt.Errorf("kmx lift: could not read the signed-in account: %w", err)
	}
	if strings.TrimSpace(acct.ID) == "" {
		return acct, fmt.Errorf("kmx lift: the Azure CLI reported no subscription — refusing to act without knowing where")
	}
	return acct, nil
}

// groupExists answers in three values rather than two, for the reason every
// other check against a subscription in this repository does: `az` prints
// nothing on stdout and errors on stderr when the call itself fails, so code
// that reads "no answer" as "does not exist" will create-and-tag a group that
// may already belong to somebody else — or report a teardown that never
// happened. Only a literal true or false is an answer.
func (a *App) groupExists(group string) (lift.Existence, error) {
	out, err := a.Run.Capture("az", "group", "exists", "--name", group)
	if err != nil {
		return lift.Unusable, err
	}
	switch strings.TrimSpace(out) {
	case "true":
		return lift.Present, nil
	case "false":
		return lift.Absent, nil
	default:
		return lift.Unusable, fmt.Errorf("az group exists answered %q, which is neither true nor false", strings.TrimSpace(out))
	}
}

// clusterPolicyEngine reads what the control plane says a cluster's
// NetworkPolicy engine is. The second return value is whether the question
// could be asked at all, which is not the same as the answer being empty —
// an empty answer means "no engine", and that is a refusal, while an
// unaskable question is a different refusal with a different fix.
func (a *App) clusterPolicyEngine(group, cluster string) (string, bool) {
	out, err := a.Run.Capture("az", "aks", "show", "--name", cluster, "--resource-group", group,
		"--query", "networkProfile.networkPolicy", "-o", "tsv")
	if err != nil {
		return "", false
	}
	return strings.TrimSpace(out), true
}

// resourceIDByName resolves a resource to its id, and distinguishes "not
// there" from "could not ask". Used at creation to record the id, and never
// used at teardown to FIND something to delete — teardown starts from the
// recorded id and only confirms it.
func (a *App) resourceID(args ...string) (string, lift.Existence) {
	out, err := a.Run.Capture("az", append(args, "--query", "id", "-o", "tsv")...)
	if err != nil {
		if isAzureNotFound(err) {
			return "", lift.Absent
		}
		return "", lift.Unusable
	}
	id := strings.TrimSpace(out)
	if id == "" {
		return "", lift.Unusable
	}
	return id, lift.Present
}

// confirmRecordedResource asks Azure about a resource BY ITS RECORDED ID and
// returns what it found. This is the only question teardown asks, and it is
// asked by id rather than by name on purpose: on a subscription we do not
// own, a name can have been re-used since the lift ran, and deleting by name
// is how a demo removes a stranger's monitoring.
func (a *App) confirmRecordedResource(id string) (lift.Existence, string) {
	out, err := a.Run.Capture("az", "resource", "show", "--ids", id, "--query", "id", "-o", "tsv")
	if err != nil {
		if isAzureNotFound(err) {
			return lift.Absent, ""
		}
		return lift.Unusable, ""
	}
	resolved := strings.TrimSpace(out)
	if resolved == "" {
		return lift.Unusable, ""
	}
	return lift.Present, resolved
}

// isAzureNotFound is deliberately narrow. Every other failure — an expired
// token, a throttled subscription, a network blip — must NOT read as absence,
// because absence is the answer that authorises skipping a delete and
// reporting success.
func isAzureNotFound(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	for _, marker := range []string{"ResourceNotFound", "ResourceGroupNotFound", "was not found", "could not be found", "(NotFound)"} {
		if strings.Contains(msg, marker) {
			return true
		}
	}
	return false
}

package app

import (
	"encoding/json"
	"fmt"
	"strconv"
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

// refuseWithoutRegistryPullRights checks that a cluster we did not create can
// already pull from the registry it was pointed at, and refuses if it cannot.
//
// The question is asked of ARM directly rather than through `az role
// assignment list`, which also calls Microsoft Graph to decorate principals —
// a tenant's conditional-access policy can refuse the CLI a Graph token while
// ARM calls keep working, and that turned a healthy cluster into a failed gate
// once already. The AcrPull role definition is resolved by name at runtime
// because its id is a GUID, and this repository refuses committed GUIDs.
//
// Fail closed in both directions: a missing assignment refuses, and so does an
// answer that could not be obtained. Proceeding on an unknown here produces
// ImagePullBackOff several minutes later, which is the least informative
// possible way to learn this.
func (a *App) refuseWithoutRegistryPullRights(opt liftIdentity) error {
	acrID, err := a.Run.Capture("az", "acr", "show", "--name", opt.RegistryName(), "--query", "id", "-o", "tsv")
	if err != nil || strings.TrimSpace(acrID) == "" {
		return fmt.Errorf(`cannot find the registry %q, or cannot read it.

  On your own cluster the registry has to exist already and your cluster has
  to be able to pull from it — this path does not create either. Check the
  name, and that you can see it: az acr show --name %s`, opt.RegistryName(), opt.RegistryName())
	}
	kubelet, err := a.Run.Capture("az", "aks", "show", "--name", opt.ClusterName(),
		"--resource-group", opt.GroupName(), "--query", "identityProfile.kubeletidentity.objectId", "-o", "tsv")
	if err != nil || strings.TrimSpace(kubelet) == "" {
		return fmt.Errorf("cannot read your cluster's kubelet identity, so whether it may pull from %s cannot be established — refusing rather than finding out as an image pull failure", opt.RegistryName())
	}
	role, err := a.Run.Capture("az", "role", "definition", "list", "--name", "AcrPull", "--query", "[0].name", "-o", "tsv")
	if err != nil || strings.TrimSpace(role) == "" {
		return fmt.Errorf("cannot resolve the AcrPull role definition — refusing to guess whether your cluster can pull")
	}
	count, err := a.Run.Capture("az", "rest", "--method", "get",
		"--url", strings.TrimSpace(acrID)+"/providers/Microsoft.Authorization/roleAssignments",
		"--url-parameters", "api-version=2022-04-01", "$filter=principalId eq '"+strings.TrimSpace(kubelet)+"'",
		"--query", "length(value[?ends_with(properties.roleDefinitionId, '"+strings.TrimSpace(role)+"')])", "-o", "tsv")
	if err != nil {
		return fmt.Errorf(`cannot read role assignments on %s, so whether your cluster may pull from it is unknown.

  Reading them needs Microsoft.Authorization/roleAssignments/read on the
  registry. Not claiming your cluster can pull, and not granting anything.

  underlying failure: %w`, opt.RegistryName(), err)
	}
	// Only a well-formed POSITIVE count is a "yes". Comparing against "0"
	// alone let an empty or non-numeric answer — which is what a changed
	// query, a truncated response or an unexpected output format produces —
	// fall through to "holds AcrPull", which is the fail-OPEN this function's
	// own contract says it does not do.
	held, convErr := strconv.Atoi(strings.TrimSpace(count))
	if convErr != nil || held < 0 {
		return fmt.Errorf("the role-assignment count for %s came back as %q, which is not a number — refusing to decide whether your cluster can pull from it on an answer that cannot be read", opt.RegistryName(), strings.TrimSpace(count))
	}
	if held == 0 {
		return fmt.Errorf(`your cluster cannot pull from %s, and this will not grant it.

  Granting AcrPull creates a role assignment on YOUR subscription against
  YOUR cluster's identity. That is a change to your cluster, not to anything
  this created, so it is yours to make:

    az aks update --name %s --resource-group %s --attach-acr %s

  Then resume — nothing before this is undone:

    kmx lift --byo --step plane --resource-group %s --cluster %s --registry %s`,
			opt.RegistryName(), opt.ClusterName(), opt.GroupName(), opt.RegistryName(),
			opt.GroupName(), opt.ClusterName(), opt.RegistryName())
	}
	a.notef("your cluster's kubelet identity holds AcrPull on %s.", opt.RegistryName())
	return nil
}

// liftIdentity is the three names that say which lift this is. Taking an
// interface rather than the options struct keeps this file free of a
// dependency on the command layer's shape.
type liftIdentity interface {
	GroupName() string
	ClusterName() string
	RegistryName() string
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

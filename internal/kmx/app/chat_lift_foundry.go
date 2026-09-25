package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/scaffold"
)

type foundryAccount struct {
	Name, Kind, ResourceGroup, Location string
	Properties                          struct {
		Endpoint, CustomSubDomainName string
		DisableLocalAuth              bool
	}
}
type foundryDeployment struct {
	Name       string
	Properties struct {
		ProvisioningState string
		Model             struct{ Name, Version, Format string }
	}
}
type foundryModel struct {
	Name, Version, Format, LifecycleStatus string
	IsDefaultVersion                       bool
	Capabilities                           map[string]string
	SKUs                                   []foundrySKU
}

type foundrySKU struct {
	Name, UsageName string
	Capacity        struct {
		Default, Minimum, Maximum, Step int
		AllowedValues                   []int
	}
}

func foundryScope(cluster chatLiftTarget, account foundryAccount) []string {
	return []string{"--subscription", cluster.Subscription, "--resource-group", account.ResourceGroup, "--name", account.Name}
}

func foundryCreateArgs(cluster chatLiftTarget, account foundryAccount) []string {
	args := append([]string{"cognitiveservices", "account", "create"}, foundryScope(cluster, account)...)
	return append(args, "--kind", "AIServices", "--sku", "S0", "--location", account.Location, "--custom-domain", account.Name, "--yes", "-o", "json", "--only-show-errors")
}

func foundryDeploymentArgs(cluster chatLiftTarget, account foundryAccount, name string, model foundryModel, sku string, capacity int) []string {
	args := append([]string{"cognitiveservices", "account", "deployment", "create"}, foundryScope(cluster, account)...)
	return append(args, "--deployment-name", name, "--model-format", model.Format, "--model-name", model.Name, "--model-version", model.Version, "--sku-name", sku, "--sku-capacity", fmt.Sprint(capacity), "-o", "json", "--only-show-errors")
}

func foundryBaseURL(account foundryAccount) (string, error) {
	endpoint := account.Properties.Endpoint
	if account.Properties.CustomSubDomainName != "" {
		name := account.Properties.CustomSubDomainName
		if !agentNameRE.MatchString(name) {
			return "", fmt.Errorf("Foundry returned an invalid custom domain")
		}
		endpoint = "https://" + name + ".openai.azure.com"
	}
	u, err := url.Parse(endpoint)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return "", fmt.Errorf("Foundry account has no usable HTTPS inference endpoint")
	}
	return strings.TrimSuffix(endpoint, "/") + "/openai/v1", nil
}

var errFoundryAccounts = errors.New("return to Foundry accounts")

func (b *orkaChatBackend) configureLiftFoundry(ctx context.Context, target *App, cluster chatLiftTarget, bundle *scaffold.OrkaBundle, r *chatRenderer) error {
	for {
		err := b.configureLiftFoundryAttempt(ctx, target, cluster, bundle, r)
		if !errors.Is(err, errFoundryAccounts) {
			return err
		}
	}
}

func (b *orkaChatBackend) configureLiftFoundryAttempt(ctx context.Context, target *App, cluster chatLiftTarget, bundle *scaffold.OrkaBundle, r *chatRenderer) error {
	// Kubeconfig targets may not carry Azure identity; collect it explicitly.
	if cluster.Subscription == "" {
		raw, err := b.liftSubscriptions(ctx)
		if err != nil {
			return err
		}
		var subs []struct{ Name, ID string }
		if err = json.Unmarshal(raw, &subs); err != nil {
			return err
		}
		var items []chatPickerItem
		var keys []string
		for _, s := range subs {
			items = append(items, chatPickerItem{name: s.Name, detail: s.ID})
			keys = append(keys, s.ID)
		}
		i, ok, err := b.liftRecentPick(ctx, "Foundry subscription", items, keys, "subscription")
		if err != nil {
			return err
		}
		if !ok {
			return errLiftBack
		}
		cluster.Subscription = subs[i].ID
	}
	raw, err := b.liftAzureFetch(ctx, "cognitiveservices", "account", "list", "--subscription", cluster.Subscription, "-o", "json", "--only-show-errors")
	if err != nil {
		return err
	}
	var all []foundryAccount
	if err = json.Unmarshal(raw, &all); err != nil {
		return err
	}
	var accounts []foundryAccount
	var items []chatPickerItem
	// Prefer existing resources in the AKS group without hiding other groups.
	for _, sameGroup := range []bool{true, false} {
		for _, account := range all {
			if (account.ResourceGroup == cluster.ResourceGroup) != sameGroup || (account.Kind != "AIServices" && account.Kind != "OpenAI") {
				continue
			}
			accounts = append(accounts, account)
			items = append(items, chatPickerItem{name: account.Name, detail: account.ResourceGroup + " · " + account.Location})
		}
	}
	items = append(items, chatPickerItem{name: "Create Foundry account", detail: "Default resource group: " + cluster.ResourceGroup})
	i, ok, err := b.liftPick(ctx, "Foundry account", items)
	if err != nil {
		return err
	}
	if !ok {
		return errLiftBack
	}
	var account foundryAccount
	var planned *foundryQuotaChoice
	if i < len(accounts) {
		account = accounts[i]
	} else {
		raw, err = b.liftAzureFetch(ctx, "group", "list", "--subscription", cluster.Subscription, "--query", "[].{name:name,location:location}", "-o", "json", "--only-show-errors")
		if err != nil {
			return err
		}
		var groups []struct{ Name, Location string }
		if err = json.Unmarshal(raw, &groups); err != nil {
			return err
		}
		for j, g := range groups {
			if g.Name == cluster.ResourceGroup {
				groups[0], groups[j] = groups[j], groups[0]
				break
			}
		}
		items = nil
		for _, g := range groups {
			detail := g.Location
			if g.Name == cluster.ResourceGroup {
				detail += " · AKS resource group (default)"
			}
			items = append(items, chatPickerItem{name: g.Name, detail: detail})
		}
		i, ok, err = b.liftPick(ctx, "Foundry resource group", items)
		if err != nil {
			return err
		}
		if !ok {
			return errLiftBack
		}
		suffix, err := randomHex(4)
		if err != nil {
			return err
		}
		account = foundryAccount{Name: "kmx-foundry-" + suffix, Kind: "AIServices", ResourceGroup: groups[i].Name, Location: cluster.Location}
		if account.Location == "" {
			account.Location = groups[i].Location
		}
		// Confirm that a model/SKU has quota before creating an empty account.
		choice, err := b.chooseQuotaModel(ctx, cluster, account, true)
		if err != nil {
			return err
		}
		planned = &choice
		account.Location = choice.Location
		if err := b.confirmLiftAction(ctx, fmt.Sprintf("Create Foundry\nSubscription: %s\nResource group: %s\nLocation: %s\nAccount: %s", cluster.Subscription, account.ResourceGroup, account.Location, account.Name), "Create Azure Foundry account (S0)"); err != nil {
			return err
		}
		r.operation("LIFT", "", colorBlue, "Creating Foundry "+account.Name+" in "+account.ResourceGroup+"…")
		raw, err = b.liftAzureCreate(ctx, foundryCreateArgs(cluster, account)...)
		if err != nil {
			return fmt.Errorf("Foundry %s/%s creation failed: %w", account.ResourceGroup, account.Name, err)
		}
		if err = json.Unmarshal(raw, &account); err != nil {
			return err
		}
	}
	if err := b.waitFoundryReady(ctx, cluster, account, ""); err != nil {
		return err
	}
	if account.Properties.DisableLocalAuth {
		return fmt.Errorf("Foundry %s disables API keys; this adapter requires key authentication", account.Name)
	}
	baseURL, err := foundryBaseURL(account)
	if err != nil {
		return err
	}
	args := append([]string{"cognitiveservices", "account", "deployment", "list"}, foundryScope(cluster, account)...)
	raw, err = b.liftAzureFetch(ctx, append(args, "-o", "json", "--only-show-errors")...)
	if err != nil {
		return err
	}
	var deployments []foundryDeployment
	if err = json.Unmarshal(raw, &deployments); err != nil {
		return err
	}
	var ready []foundryDeployment
	items = nil
	for _, d := range deployments {
		if d.Properties.ProvisioningState == "Succeeded" && d.Properties.Model.Format == "OpenAI" {
			ready = append(ready, d)
			items = append(items, chatPickerItem{name: d.Name, detail: d.Properties.Model.Name + " · " + d.Properties.Model.Version})
		}
	}
	items = append(items, chatPickerItem{name: "Create model deployment", detail: account.Name})
	i, ok, err = b.liftPick(ctx, "Foundry deployment", items)
	if err != nil {
		return err
	}
	if !ok {
		return errLiftBack
	}
	deployment := ""
	if i < len(ready) {
		deployment = ready[i].Name
	} else {
		if planned == nil {
			choice, err := b.chooseQuotaModel(ctx, cluster, account, false)
			if err != nil {
				return err
			}
			planned = &choice
		}
		if planned.Location != "" && planned.Location != account.Location {
			suffix, err := randomHex(4)
			if err != nil {
				return err
			}
			account = foundryAccount{Name: "kmx-foundry-" + suffix, Kind: "AIServices", ResourceGroup: account.ResourceGroup, Location: planned.Location}
			if err := b.recheckFoundryQuota(ctx, cluster, account, *planned); err != nil {
				return err
			}
			if err := b.confirmLiftAction(ctx, fmt.Sprintf("Create new Foundry account\nRG: %s\nRegion: %s\nModel: %s / %s\nExisting account is retained.", account.ResourceGroup, account.Location, planned.Model.Name, planned.SKU.Name), "Create account in verified model region"); err != nil {
				return err
			}
			raw, err = b.liftAzureCreate(ctx, foundryCreateArgs(cluster, account)...)
			if err != nil {
				return err
			}
			if err = json.Unmarshal(raw, &account); err != nil {
				return err
			}
			if err = b.waitFoundryReady(ctx, cluster, account, ""); err != nil {
				return err
			}
			baseURL, err = foundryBaseURL(account)
			if err != nil {
				return err
			}
		}
		model, sku, capacity := planned.Model, planned.SKU, planned.Capacity
		suffix, err := randomHex(4)
		if err != nil {
			return err
		}
		deployment = "kmx-" + suffix
		if err := b.confirmLiftAction(ctx, fmt.Sprintf("Deploy %s version %s\nAccount: %s / RG: %s\nSKU: %s · capacity: %d", model.Name, model.Version, account.Name, account.ResourceGroup, sku.Name, capacity), "Create billed model deployment"); err != nil {
			return err
		}
		if err := b.recheckFoundryQuota(ctx, cluster, account, *planned); err != nil {
			return err
		}
		r.operation("LIFT", "", colorBlue, "Creating model deployment "+deployment+"…")
		if _, err = b.liftAzureCreate(ctx, foundryDeploymentArgs(cluster, account, deployment, model, sku.Name, capacity)...); err != nil {
			return fmt.Errorf("Foundry deployment %s creation failed: %w", deployment, err)
		}
	}
	if err := b.waitFoundryReady(ctx, cluster, account, deployment); err != nil {
		return err
	}
	if err := b.confirmLiftAction(ctx, fmt.Sprintf("Use Foundry %s / %s\nTarget: %s / namespace: %s\nEndpoint: %s\nRuns one small model request from a temporary cluster Job.", account.Name, deployment, target.Cfg.KubeContext, b.namespace, baseURL), "Create target Secret and test inference connection"); err != nil {
		return err
	}
	args = append([]string{"cognitiveservices", "account", "keys", "list"}, foundryScope(cluster, account)...)
	raw, err = b.liftAzureFetch(ctx, append(args, "-o", "json", "--only-show-errors")...)
	if err != nil {
		return err
	}
	var keys struct{ Key1 string }
	if json.Unmarshal(raw, &keys) != nil || keys.Key1 == "" {
		return fmt.Errorf("Foundry returned no usable key")
	}
	suffix, err := randomHex(4)
	if err != nil {
		return err
	}
	secretName := "kmx-foundry-" + suffix
	secret := map[string]any{"apiVersion": "v1", "kind": "Secret", "metadata": map[string]any{"name": secretName, "namespace": b.namespace}, "type": "Opaque", "stringData": map[string]string{"api-key": keys.Key1}}
	body, err := json.Marshal(secret)
	if err != nil {
		return err
	}
	if _, err = target.orkaCapture(ctx, body, "-n", b.namespace, "create", "-f", "-", "-o", "name"); err != nil {
		return fmt.Errorf("target %s: cannot create Foundry credential Secret: %w", target.Cfg.KubeContext, err)
	}
	if err := b.verifyLiftFoundry(ctx, target, secretName, baseURL, deployment); err != nil {
		return fmt.Errorf("Foundry connection verification failed (target Secret %s retained): %w", secretName, err)
	}
	return useLiftProvider(bundle, map[string]any{"type": "openai", "defaultModel": deployment, "baseURL": baseURL, "secretRef": map[string]any{"name": secretName, "key": "api-key"}})
}

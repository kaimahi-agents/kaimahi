package app

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
)

type foundryUsage struct {
	Name                struct{ Value string }
	Limit, CurrentValue *float64
	Status              string
	ScopeID, ScopeType  string
}

type foundryQuotaChoice struct {
	Model      foundryModel
	SKU        foundrySKU
	Capacity   int
	Remaining  float64
	Location   string
	QuotaScope string
}

func quotaChoices(models []foundryModel, usages []foundryUsage) []foundryQuotaChoice {
	byName := map[string]foundryUsage{}
	for _, usage := range usages {
		byName[strings.ToLower(usage.Name.Value)] = usage
	}
	var choices []foundryQuotaChoice
	seen := map[string]bool{}
	for _, model := range models {
		if model.Format != "OpenAI" || !strings.EqualFold(model.Capabilities["chatCompletion"], "true") || strings.EqualFold(model.LifecycleStatus, "Deprecated") {
			continue
		}
		for _, sku := range model.SKUs {
			// Batch and provisioned-throughput SKUs are not defaults for a small
			// synchronous chat endpoint, even when their quota is nonzero.
			if sku.Name != "GlobalStandard" && sku.Name != "Standard" && sku.Name != "DataZoneStandard" {
				continue
			}
			usage, ok := byName[strings.ToLower(sku.UsageName)]
			if !ok || sku.UsageName == "" || usage.Limit == nil || usage.CurrentValue == nil || strings.EqualFold(usage.Status, "Blocked") || strings.EqualFold(usage.Status, "Unknown") {
				continue
			}
			capacity := foundryMinimumCapacity(sku)
			if capacity == 0 {
				continue
			}
			remaining := *usage.Limit - *usage.CurrentValue
			if remaining < float64(capacity) {
				continue
			}
			key := model.Format + "/" + model.Name + "/" + model.Version + "/" + sku.Name
			if seen[key] {
				continue
			}
			seen[key] = true
			choices = append(choices, foundryQuotaChoice{Model: model, SKU: sku, Capacity: capacity, Remaining: remaining, QuotaScope: usage.ScopeType + "/" + usage.ScopeID})
		}
	}
	// Prefer pay-as-you-go deployment classes over provisioned capacity. Every
	// candidate, including the first/default entry, has a matching quota record.
	rank := func(s string) int {
		switch s {
		case "GlobalStandard":
			return 0
		case "Standard":
			return 1
		case "DataZoneStandard":
			return 2
		}
		return 3
	}
	modelRank := func(m foundryModel) int {
		switch m.Name {
		case "gpt-4.1-mini":
			return 0
		case "gpt-4o-mini":
			return 1
		case "gpt-4.1":
			return 2
		case "gpt-4o":
			return 3
		}
		return 4
	}
	sort.SliceStable(choices, func(i, j int) bool {
		a, b := choices[i], choices[j]
		if rank(a.SKU.Name) != rank(b.SKU.Name) {
			return rank(a.SKU.Name) < rank(b.SKU.Name)
		}
		if modelRank(a.Model) != modelRank(b.Model) {
			return modelRank(a.Model) < modelRank(b.Model)
		}
		if a.Model.IsDefaultVersion != b.Model.IsDefaultVersion {
			return a.Model.IsDefaultVersion
		}
		return false
	})
	return choices
}

func foundryMinimumCapacity(sku foundrySKU) int {
	minimum := max(1, sku.Capacity.Minimum)
	if len(sku.Capacity.AllowedValues) > 0 {
		best := 0
		for _, v := range sku.Capacity.AllowedValues {
			if v >= minimum && (best == 0 || v < best) {
				best = v
			}
		}
		if best == 0 {
			return 0
		}
		minimum = best
	} else if sku.Capacity.Step > 1 {
		base := sku.Capacity.Minimum
		if minimum > base {
			minimum = base + ((minimum-base+sku.Capacity.Step-1)/sku.Capacity.Step)*sku.Capacity.Step
		}
	}
	if sku.Capacity.Maximum > 0 && minimum > sku.Capacity.Maximum {
		return 0
	}
	return minimum
}

func (b *orkaChatBackend) foundryQuota(ctx context.Context, cluster chatLiftTarget, location string) ([]foundryUsage, error) {
	raw, err := b.liftAzureFetch(ctx, "cognitiveservices", "usage", "list", "--subscription", cluster.Subscription, "--location", location, "-o", "json", "--only-show-errors")
	if err != nil {
		return nil, err
	}
	var usage []foundryUsage
	if err = json.Unmarshal(raw, &usage); err != nil {
		return nil, err
	}
	return usage, nil
}

// Account catalogs are flat; the regional API wraps each AccountModel in model.
func parseFoundryCatalog(raw []byte, regional bool) ([]foundryModel, error) {
	if !regional {
		var models []foundryModel
		err := json.Unmarshal(raw, &models)
		return models, err
	}
	var entries []struct{ Model foundryModel }
	if err := json.Unmarshal(raw, &entries); err != nil {
		return nil, err
	}
	var models []foundryModel
	for _, entry := range entries {
		models = append(models, entry.Model)
	}
	return models, nil
}

func (b *orkaChatBackend) chooseQuotaModel(ctx context.Context, cluster chatLiftTarget, account foundryAccount, regional bool) (foundryQuotaChoice, error) {
	for {
		args := append([]string{"cognitiveservices", "account", "list-models"}, foundryScope(cluster, account)...)
		if regional {
			args = []string{"cognitiveservices", "model", "list", "--subscription", cluster.Subscription, "--location", account.Location}
		}
		raw, err := b.liftAzureFetch(ctx, append(args, "-o", "json", "--only-show-errors")...)
		if err != nil {
			return foundryQuotaChoice{}, err
		}
		models, err := parseFoundryCatalog(raw, regional)
		if err != nil {
			return foundryQuotaChoice{}, err
		}
		usage, err := b.foundryQuota(ctx, cluster, account.Location)
		if err != nil {
			return foundryQuotaChoice{}, fmt.Errorf("cannot confirm Foundry quota in %s: %w", account.Location, err)
		}
		choices := quotaChoices(models, usage)
		for i := range choices {
			choices[i].Location = account.Location
		}
		if len(choices) == 0 {
			reason := foundryAvailabilityReason(models, account.Location)
			if regional {
				choices, err = b.alternativeFoundryRegions(ctx, cluster, account.Location)
				if err != nil {
					return foundryQuotaChoice{}, err
				}
				if len(choices) > 0 {
					return b.pickQuotaChoice(ctx, "Choose Foundry region/model · RG stays "+account.ResourceGroup, choices)
				}
			}
			i, ok, err := b.liftAction(ctx, reason, []chatPickerItem{{name: "Find a region with available model quota", detail: "Create a new account in the same resource group; existing account is kept"}, {name: "Refresh models and quota"}, {name: "Back to Foundry accounts"}})
			if err != nil {
				return foundryQuotaChoice{}, err
			}
			if !ok || i == 2 {
				return foundryQuotaChoice{}, errFoundryAccounts
			}
			if i == 0 {
				alternatives, err := b.alternativeFoundryRegions(ctx, cluster, account.Location)
				if err != nil {
					return foundryQuotaChoice{}, err
				}
				if len(alternatives) > 0 {
					return b.pickQuotaChoice(ctx, "New account region/model · RG "+account.ResourceGroup, alternatives)
				}
			}
			continue
		}
		return b.pickQuotaChoice(ctx, "Create Foundry model · "+account.Location, choices)
	}
}

func foundryAvailabilityReason(models []foundryModel, location string) string {
	for _, m := range models {
		if m.Format == "OpenAI" && strings.EqualFold(m.Capabilities["chatCompletion"], "true") {
			return "No confirmed chat deployment quota in " + location
		}
	}
	return "No OpenAI chat models offered in " + location + "; subscription quota alone is not regional availability"
}

func (b *orkaChatBackend) pickQuotaChoice(ctx context.Context, title string, choices []foundryQuotaChoice) (foundryQuotaChoice, error) {
	items := make([]chatPickerItem, 0, len(choices))
	for i, c := range choices {
		prefix := "Create "
		if i == 0 {
			prefix = "Create (quota available) "
		}
		items = append(items, chatPickerItem{name: prefix + c.Model.Name, detail: fmt.Sprintf("%s · %s · %s · %d / %.0f quota (%s)", c.Location, c.Model.Version, c.SKU.Name, c.Capacity, c.Remaining, c.QuotaScope)})
	}
	i, ok, err := b.liftPick(ctx, title, items)
	if err != nil {
		return foundryQuotaChoice{}, err
	}
	if !ok {
		return foundryQuotaChoice{}, errLiftBack
	}
	return choices[i], nil
}

// Check a small regional shortlist concurrently only when the AKS region has no
// eligible model. Each result still comes from this subscription's catalog and
// quota; an unsuccessful region read is never treated as zero quota.
func (b *orkaChatBackend) alternativeFoundryRegions(ctx context.Context, cluster chatLiftTarget, original string) ([]foundryQuotaChoice, error) {
	regions := []string{"westus3", "eastus2", "eastus"}
	type result struct {
		choices []foundryQuotaChoice
		err     error
	}
	results := make([]result, len(regions))
	_, err := b.liftLoading(ctx, "Checking subscription model availability and quota in alternative regions: westus3, eastus2, eastus", func(ctx context.Context) ([]byte, error) {
		var wg sync.WaitGroup
		for i, region := range regions {
			if region == original {
				continue
			}
			wg.Add(1)
			go func() {
				defer wg.Done()
				raw, e := b.app.liftDiscovery(ctx, "az", "cognitiveservices", "model", "list", "--subscription", cluster.Subscription, "--location", region, "-o", "json", "--only-show-errors")
				if e != nil {
					results[i].err = e
					return
				}
				models, e := parseFoundryCatalog(raw, true)
				if e != nil {
					results[i].err = e
					return
				}
				raw, e = b.app.liftDiscovery(ctx, "az", "cognitiveservices", "usage", "list", "--subscription", cluster.Subscription, "--location", region, "-o", "json", "--only-show-errors")
				if e != nil {
					results[i].err = e
					return
				}
				var usage []foundryUsage
				if e = json.Unmarshal(raw, &usage); e != nil {
					results[i].err = e
					return
				}
				results[i].choices = quotaChoices(models, usage)
				for j := range results[i].choices {
					results[i].choices[j].Location = region
				}
			}()
		}
		wg.Wait()
		return nil, ctx.Err()
	})
	if err != nil {
		return nil, err
	}
	var choices []foundryQuotaChoice
	var failures []string
	for i, result := range results {
		choices = append(choices, result.choices...)
		if result.err != nil {
			failures = append(failures, regions[i])
		}
	}
	if len(choices) == 0 && len(failures) > 0 {
		return nil, fmt.Errorf("could not confirm alternative-region availability in %s; retry discovery", strings.Join(failures, ", "))
	}
	return choices, nil
}

func (b *orkaChatBackend) recheckFoundryQuota(ctx context.Context, cluster chatLiftTarget, account foundryAccount, choice foundryQuotaChoice) error {
	usage, err := b.foundryQuota(ctx, cluster, account.Location)
	if err != nil {
		return err
	}
	model := choice.Model
	model.SKUs = []foundrySKU{choice.SKU}
	if len(quotaChoices([]foundryModel{model}, usage)) == 0 {
		return fmt.Errorf("quota for %s / %s in %s is no longer available; no model deployment attempted", model.Name, choice.SKU.Name, account.Location)
	}
	return nil
}

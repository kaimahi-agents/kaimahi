package app

import (
	"fmt"
	"net/url"
	"strings"
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

func foundryScope(cluster chatLiftTarget, account foundryAccount) []string {
	return []string{"--subscription", cluster.Subscription, "--resource-group", account.ResourceGroup, "--name", account.Name}
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

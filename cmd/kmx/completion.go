package main

import (
	"context"
	"os/exec"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/app"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/config"
)

func staticCompletion(values []string) cobra.CompletionFunc {
	return func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
		return filterCompletions(values, toComplete), cobra.ShellCompDirectiveNoFileComp
	}
}

func completeContexts(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	return filterCompletions(kubectlCompletion("config", "get-contexts", "-o", "name"), toComplete), cobra.ShellCompDirectiveNoFileComp
}

func completeLiveAgents(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	if len(args) > 0 {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	namespace, _ := cmd.Flags().GetString("namespace")
	if namespace == "" {
		namespace = app.OrkaNamespace
	}
	values := kubectlCompletion("--context", completionContext(cmd), "-n", namespace, "get", "agents.core.orka.ai", "-o", "name")
	for i, value := range values {
		if _, name, ok := strings.Cut(value, "/"); ok {
			values[i] = name
		}
	}
	return filterCompletions(values, toComplete), cobra.ShellCompDirectiveNoFileComp
}

func completionContext(cmd *cobra.Command) string {
	if value, _ := cmd.Flags().GetString("context"); value != "" {
		return value
	}
	if value, _ := cmd.InheritedFlags().GetString("context"); value != "" {
		return value
	}
	if value, _ := cmd.Root().PersistentFlags().GetString("context"); value != "" {
		return value
	}
	engine := ""
	if value, _ := cmd.Flags().GetString("container-engine"); value != "" {
		engine = value
	} else if value, _ := cmd.InheritedFlags().GetString("container-engine"); value != "" {
		engine = value
	} else if value, _ := cmd.Root().PersistentFlags().GetString("container-engine"); value != "" {
		engine = value
	}
	if cfg, err := config.LoadWithOverrides("", engine); err == nil {
		return cfg.KubeContext
	}
	return ""
}

func kubectlCompletion(args ...string) []string {
	ctx, cancel := context.WithTimeout(context.Background(), 750*time.Millisecond)
	defer cancel()
	out, err := exec.CommandContext(ctx, "kubectl", args...).Output()
	if err != nil {
		return nil
	}
	var values []string
	for _, line := range strings.Split(string(out), "\n") {
		if value := strings.TrimSpace(line); value != "" {
			values = append(values, value)
		}
	}
	return filterCompletions(values, "")
}

func filterCompletions(values []string, prefix string) []string {
	seen := map[string]bool{}
	var filtered []string
	for _, value := range values {
		if strings.HasPrefix(value, prefix) && !seen[value] {
			seen[value] = true
			filtered = append(filtered, value)
		}
	}
	sort.Strings(filtered)
	return filtered
}

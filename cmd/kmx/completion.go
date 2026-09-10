package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
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
	contextName := completionContext(cmd)
	values := kubectlCompletion("--context", contextName, "-n", "kagent", "get", "agents.kagent.dev", "-o", "name")
	for i, value := range values {
		if _, name, ok := strings.Cut(value, "/"); ok {
			values[i] = name
		}
	}
	return filterCompletions(values, toComplete), cobra.ShellCompDirectiveNoFileComp
}

func completeLocalAgents(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	if len(args) > 0 {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	entries, err := os.ReadDir("agents")
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	var names []string
	for _, entry := range entries {
		if !entry.IsDir() && filepath.Ext(entry.Name()) == ".yaml" {
			names = append(names, strings.TrimSuffix(entry.Name(), ".yaml"))
		}
	}
	return filterCompletions(names, toComplete), cobra.ShellCompDirectiveNoFileComp
}

func completePresets(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	return filterCompletions(app.PresetNames(), toComplete), cobra.ShellCompDirectiveNoFileComp
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
	if cfg, err := config.Load(""); err == nil {
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

package app

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/config"
)

// Remember identities, not list positions: Azure names can repeat between
// subscriptions/resource groups. Preferences never change Azure/kubeconfig defaults.
func liftPreferencesPath() (string, error) {
	dir, err := config.StateDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "lift-selections.json"), nil
}

func loadLiftPreferences() (map[string]string, error) {
	path, err := liftPreferencesPath()
	if err != nil {
		return nil, err
	}
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return map[string]string{}, nil
	}
	if err != nil {
		return nil, err
	}
	var prefs map[string]string
	if err := json.Unmarshal(raw, &prefs); err != nil {
		return nil, fmt.Errorf("invalid lift selections cache: %w", err)
	}
	if prefs == nil {
		prefs = map[string]string{}
	}
	return prefs, nil
}

func saveLiftPreference(scope, identity string) error {
	prefs, err := loadLiftPreferences()
	if err != nil {
		return err
	}
	prefs[scope] = identity
	path, err := liftPreferencesPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(prefs, "", "  ")
	if err != nil {
		return err
	}
	return writePrivateAgentFile(path, raw)
}

func recentLiftItems(items []chatPickerItem, keys []string, last string) ([]chatPickerItem, []int) {
	order := make([]int, 0, len(items))
	selected := -1
	for i, key := range keys {
		if last != "" && key == last {
			selected = i
			break
		}
	}
	if selected >= 0 {
		order = append(order, selected)
	}
	for i := range items {
		if i != selected {
			order = append(order, i)
		}
	}
	result := make([]chatPickerItem, 0, len(items))
	for _, i := range order {
		item := items[i]
		if i == selected {
			item.detail = "last selected · " + item.detail
		}
		result = append(result, item)
	}
	return result, order
}

func (b *orkaChatBackend) liftRecentPick(ctx context.Context, title string, items []chatPickerItem, keys []string, scope string) (int, bool, error) {
	if len(items) != len(keys) {
		return 0, false, fmt.Errorf("lift choice identity mismatch")
	}
	prefs, err := loadLiftPreferences()
	if err != nil {
		return 0, false, err
	}
	ordered, indices := recentLiftItems(items, keys, prefs[scope])
	i, ok, err := b.liftPick(ctx, title, ordered)
	if err != nil || !ok {
		return 0, false, err
	}
	i = indices[i]
	if err := saveLiftPreference(scope, keys[i]); err != nil {
		return 0, false, err
	}
	return i, true, nil
}

package agentsuite

import (
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strings"
)

const (
	DiffAdded               = "added"
	DiffRemoved             = "removed"
	DiffModified            = "modified"
	DiffReportSchemaVersion = "kmx.agentsuite.diff/v1"

	maxDiffRecordChanges   = 10_000
	maxDiffFieldsPerRecord = 10_000
	maxDiffFieldChanges    = 20_000
	maxDiffReportBytes     = 8 << 20
)

type DiffLimitError struct {
	Resource string
	Limit    int
}

func (e *DiffLimitError) Error() string {
	return fmt.Sprintf("AgentSuite diff exceeds the %s limit of %d", e.Resource, e.Limit)
}

type DiffSummary struct {
	Added          int `json:"added"`
	Removed        int `json:"removed"`
	Modified       int `json:"modified"`
	FieldsChanged  int `json:"fieldsChanged"`
	AgentsAffected int `json:"agentsAffected"`
}

type FieldChange struct {
	Path   string          `json:"path"`
	Before json.RawMessage `json:"before,omitempty"`
	After  json.RawMessage `json:"after,omitempty"`
}

type RecordChange struct {
	Type         string        `json:"type"`
	Kind         string        `json:"kind"`
	ID           string        `json:"id"`
	BeforeDigest string        `json:"beforeDigest,omitempty"`
	AfterDigest  string        `json:"afterDigest,omitempty"`
	Fields       []FieldChange `json:"fields"`
}

type AffectedAgent struct {
	ID      string   `json:"id"`
	Reasons []string `json:"reasons"`
}

type DiffIdentity struct {
	Suite          string `json:"suite"`
	SourceType     string `json:"sourceType"`
	ContentDigest  string `json:"contentDigest"`
	ArtifactDigest string `json:"artifactDigest,omitempty"`
}

type DiffReport struct {
	SchemaVersion  string          `json:"schemaVersion"`
	Before         DiffIdentity    `json:"before"`
	After          DiffIdentity    `json:"after"`
	Summary        DiffSummary     `json:"summary"`
	Changes        []RecordChange  `json:"changes"`
	AffectedAgents []AffectedAgent `json:"affectedAgents"`
}

func (r DiffReport) HasChanges() bool {
	return len(r.Changes) > 0
}

type diffRecordKey struct {
	kind string
	id   string
}

type diffRecord struct {
	digest string
	value  any
}

type diffSnapshot struct {
	suite                Suite
	identity             DiffIdentity
	records              map[diffRecordKey]diffRecord
	recordPaths          map[string]bool
	agents               map[string]Agent
	compositions         map[string]Composition
	providerCompositions map[string]ToolProviderComposition
}

// DiffPaths compares two conformant extracted AgentSuites or local OCI layouts.
func DiffPaths(beforePath, afterPath string) (DiffReport, error) {
	before, err := loadDiffSnapshot(beforePath)
	if err != nil {
		return DiffReport{}, fmt.Errorf("before AgentSuite: %w", err)
	}
	after, err := loadDiffSnapshot(afterPath)
	if err != nil {
		return DiffReport{}, fmt.Errorf("after AgentSuite: %w", err)
	}

	report := DiffReport{
		SchemaVersion:  DiffReportSchemaVersion,
		Before:         before.identity,
		After:          after.identity,
		Changes:        []RecordChange{},
		AffectedAgents: []AffectedAgent{},
	}
	keys := make([]diffRecordKey, 0, len(before.records)+len(after.records))
	seen := map[diffRecordKey]bool{}
	for key := range before.records {
		seen[key] = true
		keys = append(keys, key)
	}
	for key := range after.records {
		if !seen[key] {
			keys = append(keys, key)
		}
	}
	slices.SortFunc(keys, func(a, b diffRecordKey) int {
		if order := strings.Compare(a.kind, b.kind); order != 0 {
			return order
		}
		return strings.Compare(a.id, b.id)
	})

	changed := map[diffRecordKey]string{}
	for _, key := range keys {
		oldRecord, oldOK := before.records[key]
		newRecord, newOK := after.records[key]
		if oldOK && newOK && oldRecord.digest != "" && oldRecord.digest == newRecord.digest {
			continue
		}
		if oldOK && newOK {
			same, err := sameDiffValue(oldRecord.value, newRecord.value)
			if err != nil {
				return DiffReport{}, fmt.Errorf("compare %s %s: %w", key.kind, key.id, err)
			}
			if same {
				continue
			}
		}
		change := RecordChange{
			Kind: key.kind,
			ID:   key.id,
		}
		switch {
		case !oldOK:
			change.Type = DiffAdded
			change.AfterDigest = newRecord.digest
			report.Summary.Added++
		case !newOK:
			change.Type = DiffRemoved
			change.BeforeDigest = oldRecord.digest
			report.Summary.Removed++
		default:
			change.Type = DiffModified
			change.BeforeDigest = oldRecord.digest
			change.AfterDigest = newRecord.digest
			report.Summary.Modified++
		}
		change.Fields, err = diffFields(key.kind, oldRecord.value, oldOK, newRecord.value, newOK)
		if err != nil {
			return DiffReport{}, fmt.Errorf("diff %s %s: %w", key.kind, key.id, err)
		}
		if len(report.Changes) >= maxDiffRecordChanges {
			return DiffReport{}, &DiffLimitError{Resource: "record-change", Limit: maxDiffRecordChanges}
		}
		if len(change.Fields) > maxDiffFieldChanges-report.Summary.FieldsChanged {
			return DiffReport{}, &DiffLimitError{Resource: "field-change", Limit: maxDiffFieldChanges}
		}
		report.Summary.FieldsChanged += len(change.Fields)
		report.Changes = append(report.Changes, change)
		changed[key] = change.Type
	}

	report.AffectedAgents = affectedAgents(before, after, changed)
	report.Summary.AgentsAffected = len(report.AffectedAgents)
	if len(report.Changes) == 0 {
		artifactFields := make([]FieldChange, 0, 2)
		if report.Before.ContentDigest != report.After.ContentDigest {
			beforeDigest, _ := json.Marshal(report.Before.ContentDigest)
			afterDigest, _ := json.Marshal(report.After.ContentDigest)
			artifactFields = append(artifactFields, FieldChange{
				Path: "contentDigest", Before: beforeDigest, After: afterDigest,
			})
		}
		if report.Before.ArtifactDigest != "" && report.After.ArtifactDigest != "" &&
			report.Before.ArtifactDigest != report.After.ArtifactDigest {
			beforeDigest, _ := json.Marshal(report.Before.ArtifactDigest)
			afterDigest, _ := json.Marshal(report.After.ArtifactDigest)
			artifactFields = append(artifactFields, FieldChange{
				Path: "artifactDigest", Before: beforeDigest, After: afterDigest,
			})
		}
		if len(artifactFields) > 0 {
			beforeDigest := report.Before.ContentDigest
			afterDigest := report.After.ContentDigest
			if beforeDigest == afterDigest {
				beforeDigest = report.Before.ArtifactDigest
				afterDigest = report.After.ArtifactDigest
			}
			report.Summary.Modified = 1
			report.Summary.FieldsChanged = len(artifactFields)
			report.Changes = append(report.Changes, RecordChange{
				Type: DiffModified, Kind: "artifact", ID: "content",
				BeforeDigest: beforeDigest,
				AfterDigest:  afterDigest,
				Fields:       artifactFields,
			})
		}
	}
	if err := validateDiffReportSize(report); err != nil {
		return DiffReport{}, err
	}
	return report, nil
}

func validateDiffReportSize(report DiffReport) error {
	encoded, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return fmt.Errorf("encode AgentSuite diff report: %w", err)
	}
	if len(encoded) > maxDiffReportBytes {
		return &DiffLimitError{Resource: "serialized-report-byte", Limit: maxDiffReportBytes}
	}
	return nil
}

func loadDiffSnapshot(value string) (diffSnapshot, error) {
	content, err := loadPathContent(value)
	if err != nil {
		return diffSnapshot{}, err
	}
	if _, err := validateContent(content); err != nil {
		return diffSnapshot{}, fmt.Errorf("AgentSuite is not conformant: %w", err)
	}
	var suite Suite
	if err := decodeStrictContent(content, "agentsuite.json", &suite); err != nil {
		return diffSnapshot{}, err
	}
	contentDigest, err := diffContentDigest(content)
	if err != nil {
		return diffSnapshot{}, err
	}
	snapshot := diffSnapshot{
		suite: suite,
		identity: DiffIdentity{
			Suite:          suite.Name,
			SourceType:     content.sourceType,
			ContentDigest:  contentDigest,
			ArtifactDigest: content.artifactDigest,
		},
		records:              map[diffRecordKey]diffRecord{},
		recordPaths:          map[string]bool{"agentsuite.json": true},
		agents:               map[string]Agent{},
		compositions:         map[string]Composition{},
		providerCompositions: map[string]ToolProviderComposition{},
	}
	rawSuiteValue := map[string]any{
		"name":         suite.Name,
		"capabilities": suite.Capabilities,
		"extensions":   suite.Extensions,
	}
	normalizedSuiteValue, err := normalizeDiffValue(rawSuiteValue)
	if err != nil {
		return diffSnapshot{}, fmt.Errorf("normalize suite metadata: %w", err)
	}
	suiteValue := normalizedSuiteValue.(map[string]any)
	normalizeDiffRecord("suite", suiteValue)
	snapshot.records[diffRecordKey{kind: "suite", id: "metadata"}] = diffRecord{
		digest: content.entries["agentsuite.json"].Digest,
		value:  suiteValue,
	}
	for _, ref := range suite.Agents {
		var agent Agent
		if err := snapshot.addRecord(content, "agent", ref.ID, ref.Path, ref.Digest, &agent); err != nil {
			return diffSnapshot{}, err
		}
		snapshot.agents[ref.ID] = agent
	}
	for _, ref := range suite.BuildProfiles {
		if err := snapshot.addRecord(content, "buildProfile", ref.ID, ref.Path, ref.Digest, nil); err != nil {
			return diffSnapshot{}, err
		}
	}
	if err := snapshot.addRecord(content, "toolProviderCatalog", suite.ToolProviderCatalog.ID,
		suite.ToolProviderCatalog.Path, suite.ToolProviderCatalog.Digest, nil); err != nil {
		return diffSnapshot{}, err
	}
	var catalog ToolProviderCatalog
	if err := decodeStrictContent(content, suite.ToolProviderCatalog.Path, &catalog); err != nil {
		return diffSnapshot{}, err
	}
	for _, ref := range catalog.ToolProviders {
		if err := snapshot.addRecord(content, "toolProvider", ref.ID+"@"+ref.Version, ref.Path, ref.Digest, nil); err != nil {
			return diffSnapshot{}, err
		}
	}
	for _, ref := range suite.Compositions {
		id := ref.Agent + "@" + ref.Platform.String()
		var composition Composition
		if err := snapshot.addRecord(content, "composition", id, ref.Path, ref.Digest, &composition); err != nil {
			return diffSnapshot{}, err
		}
		snapshot.compositions[id] = composition
	}
	for _, ref := range suite.ToolProviderCompositions {
		id := ref.ID + "@" + ref.Version + "@" + ref.Platform.String()
		var composition ToolProviderComposition
		if err := snapshot.addRecord(content, "toolProviderComposition", id, ref.Path, ref.Digest, &composition); err != nil {
			return diffSnapshot{}, err
		}
		snapshot.providerCompositions[id] = composition
	}
	for _, path := range slices.Sorted(maps.Keys(content.entries)) {
		entry := content.entries[path]
		canonical, include, err := canonicalDiffContentEntry(content, entry)
		if err != nil {
			return diffSnapshot{}, err
		}
		if !include || snapshot.recordPaths[path] {
			continue
		}
		value, digest, err := diffContentRecord(canonical)
		if err != nil {
			return diffSnapshot{}, fmt.Errorf("encode supplemental content %s: %w", path, err)
		}
		snapshot.records[diffRecordKey{kind: "content", id: path}] = diffRecord{
			digest: digest,
			value:  value,
		}
	}
	return snapshot, nil
}

func diffContentRecord(entry diffContentEntry) (any, string, error) {
	encoded, err := json.Marshal(map[string]any{
		"path":       entry.Path,
		"type":       entry.Type,
		"mode":       entry.Mode,
		"size":       entry.Size,
		"digest":     entry.Digest,
		"linkTarget": entry.LinkTarget,
	})
	if err != nil {
		return nil, "", err
	}
	value, err := decodeJSONValue(encoded)
	if err != nil {
		return nil, "", err
	}
	return value, digestBytes(encoded), nil
}

func (s *diffSnapshot) addRecord(content *contentSet, kind, id, path, digest string, typed any) error {
	data, err := content.data(path)
	if err != nil {
		return err
	}
	value, err := decodeJSONValue(data)
	if err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	if typed != nil {
		if err := decodeStrict(data, typed); err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
	}
	normalizeDiffRecord(kind, value)
	s.records[diffRecordKey{kind: kind, id: id}] = diffRecord{digest: digest, value: value}
	s.recordPaths[path] = true
	return nil
}

type diffContentEntry struct {
	Path       string `json:"path"`
	Type       string `json:"type"`
	Mode       uint32 `json:"mode"`
	Size       int64  `json:"size"`
	Digest     string `json:"digest,omitempty"`
	LinkTarget string `json:"linkTarget,omitempty"`
}

func diffContentDigest(content *contentSet) (string, error) {
	entries := make([]diffContentEntry, 0, len(content.entries))
	for _, name := range slices.Sorted(maps.Keys(content.entries)) {
		entry := content.entries[name]
		canonical, include, err := canonicalDiffContentEntry(content, entry)
		if err != nil {
			return "", err
		}
		if !include {
			continue
		}
		entries = append(entries, canonical)
	}
	encoded, err := json.Marshal(entries)
	if err != nil {
		return "", fmt.Errorf("encode AgentSuite content identity: %w", err)
	}
	return digestBytes(encoded), nil
}

func canonicalDiffContentEntry(content *contentSet, entry contentEntry) (diffContentEntry, bool, error) {
	if entry.Type == "directory" {
		return diffContentEntry{}, false, nil
	}
	if entry.Type == "hardlink" {
		target, ok := content.entries[entry.LinkTarget]
		if !ok || target.Type != "file" {
			return diffContentEntry{}, false, fmt.Errorf(
				"canonicalize AgentSuite content %s: hardlink target %s is not a regular file",
				entry.Path, entry.LinkTarget,
			)
		}
		entry.Type = target.Type
		entry.Mode = target.Mode
		entry.Size = target.Size
		entry.Digest = target.Digest
		entry.LinkTarget = ""
	}
	return diffContentEntry{
		Path: entry.Path, Type: entry.Type, Mode: entry.Mode,
		Size: entry.Size, Digest: entry.Digest, LinkTarget: entry.LinkTarget,
	}, true, nil
}

func decodeStrictContent(content *contentSet, path string, value any) error {
	data, err := content.data(path)
	if err != nil {
		return err
	}
	if err := decodeStrict(data, value); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	return nil
}

func sameDiffValue(before, after any) (bool, error) {
	normalizedBefore, err := normalizeDiffValue(before)
	if err != nil {
		return false, err
	}
	normalizedAfter, err := normalizeDiffValue(after)
	if err != nil {
		return false, err
	}
	return sameExactJSONValue(normalizedBefore, normalizedAfter), nil
}

func normalizeDiffValue(value any) (any, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	return decodeJSONValue(encoded)
}

var diffSetArrayKeys = map[string]map[string][]string{
	"suite": {
		"capabilities": {"$"},
		"extensions":   {"name"},
	},
	"agent": {
		"extensions":       {"name"},
		"invokes":          {"agent"},
		"model.secretRefs": {"$"},
		"toolProviders":    {"id", "version"},
	},
	"buildProfile": {
		"harness":     {"platform.os", "platform.architecture"},
		"runtimeBase": {"platform.os", "platform.architecture"},
	},
	"composition": {
		"toolProviders": {"id", "version"},
	},
	"toolProviderCatalog": {
		"toolProviders": {"id", "version"},
	},
	"toolProvider": {
		"extensions":                           {"name"},
		"remote.headers":                       {"name"},
		"remote.network":                       {"destinationRef"},
		"tools":                                {"name"},
		"tools[].effects":                      {"$"},
		"variants":                             {"platform.os", "platform.architecture"},
		"variants[].dependencies":              {"id", "version"},
		"variants[].environment":               {"name"},
		"variants[].files":                     {"path"},
		"variants[].network":                   {"scheme", "host", "port"},
		"variants[].runtime.requiredBasePaths": {"$"},
		"variants[].writablePaths":             {"$"},
	},
}

func normalizeDiffRecord(kind string, value any) {
	rules := diffSetArrayKeys[kind]
	var normalize func(any, string)
	normalize = func(current any, path string) {
		switch current := current.(type) {
		case map[string]any:
			for rulePath := range rules {
				parent, field := "", rulePath
				if last := strings.LastIndex(rulePath, "."); last >= 0 {
					parent, field = rulePath[:last], rulePath[last+1:]
				}
				if parent == path && current[field] == nil {
					current[field] = []any{}
				}
			}
			for key, child := range current {
				childPath := key
				if path != "" {
					childPath = path + "." + key
				}
				normalize(child, childPath)
			}
		case []any:
			if fields, ok := rules[path]; ok {
				slices.SortStableFunc(current, func(left, right any) int {
					return strings.Compare(diffArraySortKey(left, fields), diffArraySortKey(right, fields))
				})
			}
			for _, child := range current {
				normalize(child, path+"[]")
			}
		}
	}
	normalize(value, "")
}

func diffArraySortKey(value any, fields []string) string {
	if len(fields) == 1 && fields[0] == "$" {
		encoded, _ := json.Marshal(value)
		return string(encoded)
	}
	object, ok := value.(map[string]any)
	if !ok {
		encoded, _ := json.Marshal(value)
		return string(encoded)
	}
	var key strings.Builder
	for _, field := range fields {
		current := any(object)
		for _, part := range strings.Split(field, ".") {
			nested, ok := current.(map[string]any)
			if !ok {
				current = nil
				break
			}
			current = nested[part]
		}
		encoded, _ := json.Marshal(current)
		key.Write(encoded)
		key.WriteByte(0)
	}
	encoded, _ := json.Marshal(value)
	key.Write(encoded)
	return key.String()
}

func diffFields(kind string, before any, beforePresent bool, after any, afterPresent bool) ([]FieldChange, error) {
	walker := diffFieldWalker{rules: diffSetArrayKeys[kind]}
	if kind == "content" && (!beforePresent || !afterPresent) {
		if err := walker.add("", before, beforePresent, after, afterPresent); err != nil {
			return nil, err
		}
		return walker.fields, nil
	}
	if err := walker.compare("", "", before, beforePresent, after, afterPresent); err != nil {
		return nil, err
	}
	return walker.fields, nil
}

type diffFieldWalker struct {
	rules  map[string][]string
	fields []FieldChange
}

func (w *diffFieldWalker) compare(
	path, rulePath string,
	before any, beforePresent bool,
	after any, afterPresent bool,
) error {
	if beforePresent && afterPresent && sameExactJSONValue(before, after) {
		return nil
	}
	if !beforePresent {
		return w.emitValue(path, rulePath, nil, false, after, true)
	}
	if !afterPresent {
		return w.emitValue(path, rulePath, before, true, nil, false)
	}

	beforeObject, beforeIsObject := before.(map[string]any)
	afterObject, afterIsObject := after.(map[string]any)
	if beforeIsObject && afterIsObject {
		return w.compareObjects(path, rulePath, beforeObject, afterObject)
	}
	beforeArray, beforeIsArray := before.([]any)
	afterArray, afterIsArray := after.([]any)
	if beforeIsArray && afterIsArray {
		if keyFields, ok := w.rules[rulePath]; ok {
			return w.compareSetArrays(path, rulePath, keyFields, beforeArray, afterArray)
		}
		return w.compareOrderedArrays(path, rulePath, beforeArray, afterArray)
	}
	return w.add(path, before, true, after, true)
}

func (w *diffFieldWalker) compareObjects(
	path, rulePath string,
	before, after map[string]any,
) error {
	keys := make([]string, 0, len(before)+len(after))
	seen := map[string]bool{}
	for key := range before {
		seen[key] = true
		keys = append(keys, key)
	}
	for key := range after {
		if !seen[key] {
			keys = append(keys, key)
		}
	}
	slices.Sort(keys)
	for _, key := range keys {
		childPath := joinDiffPath(path, key)
		childRulePath := joinDiffPath(rulePath, key)
		beforeValue, beforeOK := before[key]
		afterValue, afterOK := after[key]
		if err := w.compare(childPath, childRulePath, beforeValue, beforeOK, afterValue, afterOK); err != nil {
			return err
		}
	}
	return nil
}

func (w *diffFieldWalker) compareOrderedArrays(
	path, rulePath string,
	before, after []any,
) error {
	length := max(len(before), len(after))
	for index := range length {
		beforeOK := index < len(before)
		afterOK := index < len(after)
		var beforeValue, afterValue any
		if beforeOK {
			beforeValue = before[index]
		}
		if afterOK {
			afterValue = after[index]
		}
		if err := w.compare(
			fmt.Sprintf("%s[%d]", path, index), rulePath+"[]",
			beforeValue, beforeOK, afterValue, afterOK,
		); err != nil {
			return err
		}
	}
	return nil
}

func (w *diffFieldWalker) compareSetArrays(
	path, rulePath string,
	keyFields []string,
	before, after []any,
) error {
	beforeValues := keyedDiffValues(before, keyFields)
	afterValues := keyedDiffValues(after, keyFields)
	keys := make([]string, 0, len(beforeValues)+len(afterValues))
	seen := map[string]bool{}
	for key := range beforeValues {
		seen[key] = true
		keys = append(keys, key)
	}
	for key := range afterValues {
		if !seen[key] {
			keys = append(keys, key)
		}
	}
	slices.Sort(keys)
	for _, key := range keys {
		beforeValue, beforeOK := beforeValues[key]
		afterValue, afterOK := afterValues[key]
		labelValue := beforeValue
		if !beforeOK {
			labelValue = afterValue
		}
		label := diffArrayLabel(labelValue, keyFields)
		elementPath := fmt.Sprintf("%s[%s]", path, label)
		if !beforeOK || !afterOK {
			emit := w.add
			if w.hasNestedSetRule(rulePath + "[]") {
				emit = func(path string, before any, beforePresent bool, after any, afterPresent bool) error {
					return w.emitValue(path, rulePath+"[]", before, beforePresent, after, afterPresent)
				}
			}
			if err := emit(elementPath, beforeValue, beforeOK, afterValue, afterOK); err != nil {
				return err
			}
			continue
		}
		if err := w.compare(
			elementPath, rulePath+"[]",
			beforeValue, beforeOK, afterValue, afterOK,
		); err != nil {
			return err
		}
	}
	return nil
}

func (w *diffFieldWalker) emitValue(
	path, rulePath string,
	before any, beforePresent bool,
	after any, afterPresent bool,
) error {
	value := before
	if !beforePresent {
		value = after
	}
	switch value := value.(type) {
	case map[string]any:
		if len(value) == 0 {
			return w.add(path, before, beforePresent, after, afterPresent)
		}
		keys := slices.Sorted(maps.Keys(value))
		for _, key := range keys {
			childBefore, childAfter := any(nil), any(nil)
			if beforePresent {
				childBefore = value[key]
			} else {
				childAfter = value[key]
			}
			if err := w.emitValue(
				joinDiffPath(path, key), joinDiffPath(rulePath, key),
				childBefore, beforePresent, childAfter, afterPresent,
			); err != nil {
				return err
			}
		}
		return nil
	case []any:
		if len(value) == 0 {
			return w.add(path, before, beforePresent, after, afterPresent)
		}
		if keyFields, ok := w.rules[rulePath]; ok {
			values := keyedDiffValues(value, keyFields)
			for _, key := range slices.Sorted(maps.Keys(values)) {
				item := values[key]
				itemBefore, itemAfter := any(nil), any(nil)
				if beforePresent {
					itemBefore = item
				} else {
					itemAfter = item
				}
				elementPath := fmt.Sprintf("%s[%s]", path, diffArrayLabel(item, keyFields))
				if w.hasNestedSetRule(rulePath + "[]") {
					if err := w.emitValue(
						elementPath, rulePath+"[]",
						itemBefore, beforePresent, itemAfter, afterPresent,
					); err != nil {
						return err
					}
					continue
				}
				if err := w.add(elementPath, itemBefore, beforePresent, itemAfter, afterPresent); err != nil {
					return err
				}
			}
			return nil
		}
		for index, item := range value {
			itemBefore, itemAfter := any(nil), any(nil)
			if beforePresent {
				itemBefore = item
			} else {
				itemAfter = item
			}
			if err := w.emitValue(
				fmt.Sprintf("%s[%d]", path, index), rulePath+"[]",
				itemBefore, beforePresent, itemAfter, afterPresent,
			); err != nil {
				return err
			}
		}
		return nil
	default:
		return w.add(path, before, beforePresent, after, afterPresent)
	}
}

func (w *diffFieldWalker) hasNestedSetRule(rulePath string) bool {
	prefix := rulePath + "."
	for candidate := range w.rules {
		if strings.HasPrefix(candidate, prefix) {
			return true
		}
	}
	return false
}

func (w *diffFieldWalker) add(
	path string,
	before any, beforePresent bool,
	after any, afterPresent bool,
) error {
	if len(w.fields) >= maxDiffFieldsPerRecord {
		return &DiffLimitError{Resource: "field-per-record", Limit: maxDiffFieldsPerRecord}
	}
	change := FieldChange{Path: diffPath(path)}
	var err error
	if beforePresent {
		change.Before, err = json.Marshal(before)
		if err != nil {
			return err
		}
	}
	if afterPresent {
		change.After, err = json.Marshal(after)
		if err != nil {
			return err
		}
	}
	w.fields = append(w.fields, change)
	return nil
}

func keyedDiffValues(values []any, fields []string) map[string]any {
	keyed := make(map[string]any, len(values))
	for _, value := range values {
		keyed[diffArrayIdentity(value, fields)] = value
	}
	return keyed
}

func diffArrayIdentity(value any, fields []string) string {
	if len(fields) == 1 && fields[0] == "$" {
		encoded, _ := json.Marshal(value)
		return string(encoded)
	}
	var key strings.Builder
	for _, field := range fields {
		encoded, _ := json.Marshal(diffNestedValue(value, field))
		key.Write(encoded)
		key.WriteByte(0)
	}
	return key.String()
}

func diffArrayLabel(value any, fields []string) string {
	if len(fields) == 1 && fields[0] == "$" {
		return fmt.Sprint(value)
	}
	parts := make([]string, 0, len(fields))
	for _, field := range fields {
		parts = append(parts, fmt.Sprint(diffNestedValue(value, field)))
	}
	separator := "@"
	if len(fields) == 2 && fields[0] == "platform.os" && fields[1] == "platform.architecture" {
		separator = "/"
	}
	return strings.Join(parts, separator)
}

func diffNestedValue(value any, field string) any {
	current := value
	for _, part := range strings.Split(field, ".") {
		nested, ok := current.(map[string]any)
		if !ok {
			return nil
		}
		current = nested[part]
	}
	return current
}

func joinDiffPath(prefix, field string) string {
	if prefix == "" {
		return field
	}
	return prefix + "." + field
}

func diffPath(path string) string {
	if path == "" {
		return "$"
	}
	return path
}

func affectedAgents(before, after diffSnapshot, changed map[diffRecordKey]string) []AffectedAgent {
	reasons := map[string]map[string]bool{}
	addReason := func(agent, reason string) {
		if reasons[agent] == nil {
			reasons[agent] = map[string]bool{}
		}
		reasons[agent][reason] = true
	}
	for key, changeType := range changed {
		if key.kind == "agent" {
			addReason(key.id, "agent definition "+changeType)
		}
	}
	for _, snapshot := range []diffSnapshot{before, after} {
		for id, composition := range snapshot.compositions {
			if changeType, ok := changed[diffRecordKey{kind: "composition", id: id}]; ok {
				addReason(composition.Agent, "composition "+id+" "+changeType)
			}
			if changeType, ok := changed[diffRecordKey{kind: "buildProfile", id: composition.BuildProfile}]; ok {
				addReason(composition.Agent, "build profile "+composition.BuildProfile+" "+changeType)
			}
			for _, provider := range composition.ToolProviders {
				providerID := provider.ID + "@" + provider.Version
				if changeType, ok := changed[diffRecordKey{kind: "toolProvider", id: providerID}]; ok {
					addReason(composition.Agent, "tool provider "+providerID+" "+changeType)
				}
			}
		}
	}
	propagateInvocationImpacts(before, after, changed, reasons, addReason)
	agents := make([]AffectedAgent, 0, len(reasons))
	for agent, values := range reasons {
		items := make([]string, 0, len(values))
		for reason := range values {
			items = append(items, reason)
		}
		slices.Sort(items)
		agents = append(agents, AffectedAgent{ID: agent, Reasons: items})
	}
	slices.SortFunc(agents, func(a, b AffectedAgent) int {
		return strings.Compare(a.ID, b.ID)
	})
	return agents
}

func propagateInvocationImpacts(
	before, after diffSnapshot,
	changed map[diffRecordKey]string,
	reasons map[string]map[string]bool,
	addReason func(string, string),
) {
	callers := map[string]map[string]bool{}
	for _, snapshot := range []diffSnapshot{before, after} {
		for caller, agent := range snapshot.agents {
			for _, invocation := range agent.Invokes {
				if callers[invocation.Agent] == nil {
					callers[invocation.Agent] = map[string]bool{}
				}
				callers[invocation.Agent][caller] = true
			}
		}
	}

	queue := make([]string, 0, len(reasons))
	queued := map[string]bool{}
	for agent := range reasons {
		queue = append(queue, agent)
		queued[agent] = true
	}
	slices.Sort(queue)
	for len(queue) > 0 {
		invoked := queue[0]
		queue = queue[1:]
		changeType := changed[diffRecordKey{kind: "agent", id: invoked}]
		if changeType == "" {
			changeType = "affected"
		}
		names := make([]string, 0, len(callers[invoked]))
		for caller := range callers[invoked] {
			names = append(names, caller)
		}
		slices.Sort(names)
		for _, caller := range names {
			addReason(caller, "invoked agent "+invoked+" "+changeType)
			if !queued[caller] {
				queue = append(queue, caller)
				queued[caller] = true
			}
		}
	}
}

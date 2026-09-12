package config

// The operator overlay adds model endpoints without changing the committed
// table that kmx plane reapplies. Fragments may carry only upstreams;
// custody fields and prices belong in the reviewed base table. A keyless
// entry does not establish that the endpoint is free: classification is
// still the operator's claim, and an unpriced metered pair is refused under
// a cents budget. Duplicate names and JSON keys are refused, not resolved
// by precedence. Parse remains the one validator of the merged table.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// DefaultConfigDir is where the overlay fragments are mounted. The
// volume is optional, so an absent directory is an empty overlay and
// never an error.
const DefaultConfigDir = "/etc/kaimahi/upstreams.d"

// mergeableBlocks are the top-level keys a fragment may carry.
var mergeableBlocks = []string{"upstreams"}

// modelCustodyFields cannot be set by an overlay. Extra headers can forge
// the identity a keyless server trusts; custody paths and hosted reach can
// exfiltrate plane secrets; prices decide what a cents budget measures.
// TestEveryUpstreamFieldIsClassifiedAsSafeOrDenied guards new fields.
var modelCustodyFields = []string{"credential_file", "credential_header", "internet", "ca_file", "extra_headers", "prices"}

// Fragment is one operator-added overlay file: its name (for error
// messages and ordering) and its bytes.
type Fragment struct {
	Name string
	Raw  []byte
}

// Read returns the base config bytes and the overlay fragments, sorted
// by name so a merge is deterministic. A missing directory is an empty
// overlay; a missing base file is an error, as it always was.
func Read(path, dir string) ([]byte, []Fragment, error) {
	base, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, err
	}
	if dir == "" {
		return base, nil, nil
	}
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return base, nil, nil
	}
	if err != nil {
		return nil, nil, fmt.Errorf("config: reading overlay %s: %w", dir, err)
	}
	var frags []Fragment
	for _, e := range entries {
		name := e.Name()
		// A ConfigMap volume plants ..data and ..2026_09_03_… symlinks
		// beside the keys; only the keys are ours to read.
		if e.IsDir() || !FragmentName(name) {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			return nil, nil, fmt.Errorf("config: reading overlay fragment %s: %w", name, err)
		}
		frags = append(frags, Fragment{Name: name, Raw: raw})
	}
	sort.Slice(frags, func(i, j int) bool { return frags[i].Name < frags[j].Name })
	return base, frags, nil
}

// FragmentName reports whether a ConfigMap key is one the boot path will
// actually read. It is exported because the admin validator has to apply
// the SAME rule: a key this returns false for is silently ignored at
// boot, and a validator that accepted one would validate an endpoint the
// plane will never see.
func FragmentName(name string) bool {
	return !strings.HasPrefix(name, ".") && strings.HasSuffix(name, ".json")
}

// Merge folds the fragments into the base table and returns the bytes
// Parse should read. It performs NO validation of its own beyond the
// structural rules above — that is Parse's job, so there is exactly one
// place that decides whether a table is well formed.
func Merge(base []byte, frags []Fragment) ([]byte, error) {
	if err := refuseDuplicateKeys(base, "config"); err != nil {
		return nil, err
	}
	var merged map[string]json.RawMessage
	if err := json.Unmarshal(base, &merged); err != nil {
		return nil, fmt.Errorf("config: base table is not a JSON object: %w", err)
	}
	// Where each merged name came from, so a collision can name both.
	origin := map[string]map[string]string{}
	for _, block := range mergeableBlocks {
		origin[block] = map[string]string{}
		names, err := namesIn(merged[block])
		if err != nil {
			return nil, fmt.Errorf("config: base table: %q: %w", block, err)
		}
		for name := range names {
			origin[block][name] = "the committed table"
		}
	}
	for _, f := range frags {
		if err := refuseDuplicateKeys(f.Raw, "overlay "+f.Name); err != nil {
			return nil, err
		}
		var frag map[string]json.RawMessage
		if err := json.Unmarshal(f.Raw, &frag); err != nil {
			return nil, fmt.Errorf("config: overlay %s is not a JSON object: %w", f.Name, err)
		}
		for key := range frag {
			if !mergeable(key) {
				return nil, fmt.Errorf("config: overlay %s carries %q, which an overlay may not set (allowed: %s)",
					f.Name, key, strings.Join(mergeableBlocks, ", "))
			}
		}
		for _, block := range mergeableBlocks {
			add, err := namesIn(frag[block])
			if err != nil {
				return nil, fmt.Errorf("config: overlay %s: %q: %w", f.Name, block, err)
			}
			if len(add) == 0 {
				continue
			}
			into, err := namesIn(merged[block])
			if err != nil {
				return nil, fmt.Errorf("config: %q: %w", block, err)
			}
			for name, raw := range add {
				if err := refuseModelCustodyFields(f.Name, name, raw); err != nil {
					return nil, err
				}
				if from, ok := origin[block][name]; ok {
					return nil, fmt.Errorf("config: overlay %s redefines %s %q, already defined by %s — refused rather than resolved by precedence",
						f.Name, strings.TrimSuffix(block, "s"), name, from)
				}
				into[name] = raw
				origin[block][name] = "overlay " + f.Name
			}
			out, err := marshalSorted(into)
			if err != nil {
				return nil, err
			}
			merged[block] = out
		}
	}
	return marshalSorted(merged)
}

// refuseModelCustodyFields checks keys case-insensitively and the decoded
// entry itself: encoding/json folds field names, so a case-sensitive
// denylist would let Credential_File exfiltrate a custody-held secret.
func refuseModelCustodyFields(fragment, upstream string, raw json.RawMessage) error {
	refuse := func(field string) error {
		return fmt.Errorf("config: overlay %s: upstream %q sets %q, which an overlay may not set. "+
			"An overlay describes an in-cluster, keyless model endpoint; %s decide what credential the "+
			"proxy reads, which host outside the cluster it may be sent to, and what a cents budget is "+
			"measured with — those belong in the committed table (k8s/plane/upstreams.yaml) where they "+
			"are reviewed as part of this repository",
			fragment, upstream, field, strings.Join(modelCustodyFields, ", "))
	}
	var keys map[string]json.RawMessage
	if err := json.Unmarshal(raw, &keys); err != nil {
		return fmt.Errorf("config: overlay %s: upstream %q: want an object, got %s",
			fragment, upstream, firstBytes(raw))
	}
	for key := range keys {
		for _, field := range modelCustodyFields {
			if strings.EqualFold(key, field) {
				return refuse(key)
			}
		}
	}
	var entry Upstream
	if err := json.Unmarshal(raw, &entry); err != nil {
		return fmt.Errorf("config: overlay %s: upstream %q: %w", fragment, upstream, err)
	}
	switch {
	case entry.CredentialFile != "":
		return refuse("credential_file")
	case entry.CredentialHeader != "":
		return refuse("credential_header")
	case entry.Internet:
		return refuse("internet")
	case entry.CAFile != "":
		return refuse("ca_file")
	case len(entry.ExtraHeaders) > 0:
		return refuse("extra_headers")
	case len(entry.Prices) > 0:
		return refuse("prices")
	}
	return nil
}

func mergeable(key string) bool {
	for _, b := range mergeableBlocks {
		if key == b {
			return true
		}
	}
	return false
}

// namesIn decodes one top-level block into name -> raw value. An absent
// or null block is an empty map — that is a table with nothing in that
// block, which is legal. Anything else that is not an object IS an
// error: a hand-edited fragment whose upstreams is a list or string must
// not merge nothing and report success for an endpoint that does not exist.
func namesIn(raw json.RawMessage) (map[string]json.RawMessage, error) {
	out := map[string]json.RawMessage{}
	if len(raw) == 0 || string(raw) == "null" {
		return out, nil
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("want an object of names, got %s", firstBytes(raw))
	}
	return out, nil
}

// firstBytes renders enough of a value to identify it in a message
// without pasting a whole table into a log line.
func firstBytes(raw json.RawMessage) string {
	const max = 40
	if len(raw) > max {
		return string(raw[:max]) + "…"
	}
	return string(raw)
}

// marshalSorted emits an object with its keys in sorted order, so the
// merged bytes are byte-stable for a given input and an error message
// quoting them is reproducible. encoding/json already sorts map keys;
// this exists so the intent is stated rather than relied upon.
func marshalSorted(m map[string]json.RawMessage) ([]byte, error) {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b bytes.Buffer
	b.WriteByte('{')
	for i, k := range keys {
		if i > 0 {
			b.WriteByte(',')
		}
		key, err := json.Marshal(k)
		if err != nil {
			return nil, err
		}
		b.Write(key)
		b.WriteByte(':')
		if len(m[k]) == 0 {
			b.WriteString("null")
			continue
		}
		b.Write(m[k])
	}
	b.WriteByte('}')
	return b.Bytes(), nil
}

// refuseDuplicateKeys walks a JSON document and refuses any object that
// names the same key twice, at any depth. Go's decoder takes the last
// occurrence; a reviewer reading the ConfigMap takes the first. A config
// where those disagree is a config nobody has actually reviewed.
func refuseDuplicateKeys(raw []byte, what string) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err := walkDuplicates(dec, what, 0); err != nil {
		return err
	}
	if _, err := dec.Token(); err != io.EOF {
		return fmt.Errorf("config: %s: trailing bytes after the JSON document", what)
	}
	return nil
}

// maxConfigDepth bounds the duplicate-key walk for untrusted config input.
const maxConfigDepth = 32

func walkDuplicates(dec *json.Decoder, what string, depth int) error {
	if depth > maxConfigDepth {
		return fmt.Errorf("config: %s: nested deeper than %d levels", what, maxConfigDepth)
	}
	tok, err := dec.Token()
	if err != nil {
		return fmt.Errorf("config: %s: %w", what, err)
	}
	delim, ok := tok.(json.Delim)
	if !ok {
		return nil // a scalar
	}
	switch delim {
	case '{':
		seen := map[string]bool{}
		for dec.More() {
			keyTok, err := dec.Token()
			if err != nil {
				return fmt.Errorf("config: %s: %w", what, err)
			}
			key, ok := keyTok.(string)
			if !ok {
				return fmt.Errorf("config: %s: non-string object key", what)
			}
			if seen[key] {
				return fmt.Errorf("config: %s: duplicate key %q — refused, not collapsed", what, key)
			}
			seen[key] = true
			if err := walkDuplicates(dec, what, depth+1); err != nil {
				return err
			}
		}
	case '[':
		for dec.More() {
			if err := walkDuplicates(dec, what, depth+1); err != nil {
				return err
			}
		}
	}
	if _, err := dec.Token(); err != nil { // the closing delimiter
		return fmt.Errorf("config: %s: %w", what, err)
	}
	return nil
}

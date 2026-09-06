package config

// The overlay. An adopter onboarding their OWN MCP server must not
// have to edit the committed table — k8s/plane/upstreams.yaml is this
// repo's four demo upstreams, and `kmx plane` re-applies it, so an entry
// added there is an entry the next deploy silently discards.
//
// So the proxy reads a base file AND a directory of operator fragments,
// merges them, and parses the result with the ONE parser everything else
// uses (Parse). The merge is deliberately narrow and fail-closed:
//
//   - a fragment may carry `tool_upstreams` and `standing_constraints`
//     and nothing else. The LLM upstreams, the inbound hooks and the
//     approval notifier are the plane's own seams, each entangled with a
//     credential mount or a signing secret; a generic onboarding path
//     that could rewrite them would be a much larger blast radius than
//     the one this exists for.
//   - and within `tool_upstreams`, an overlay entry may not carry the
//     CUSTODY fields. This is the same rule as the bullet above, applied
//     one level down, and it was missed once: `credential_file` names a
//     path the proxy reads and sends upstream, and `internet` plus
//     `ca_file` decide which host it may be sent to. Together they are a
//     complete exfiltration primitive — an entry naming
//     /etc/kaimahi/admin/token against an attacker's https host would
//     hand the plane's admin bearer to it on the first relayed call.
//     Excluding them costs the overlay exactly the two upstreams
//     docs/govern-your-agent.md already says it cannot express (a keyed
//     server and a hosted one), and both remain available by editing the
//     committed table, which is a deliberate act on a reviewed file.
//   - a name defined twice is REFUSED, naming both sources, rather than
//     resolved by precedence. Silent precedence is how an operator ends
//     up reviewing one entry and running another.
//   - a duplicated JSON key at any depth is refused, not collapsed —
//     the same argument the gateway makes about `arguments` (canon.go),
//     applied to config: Go reads last-wins, a reviewer reads first.
//
// Everything else — every URL shape, every hosted-vetting rule, every
// policy_fields and constraint rule — is Parse's job, unchanged, over
// the merged bytes. There is no second validator.

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
var mergeableBlocks = []string{"tool_upstreams", "standing_constraints"}

// custodyFields are the tool_upstreams keys an OVERLAY entry may not set.
// The list exists so a field added to ToolUpstream must be classified
// deliberately (TestEveryToolUpstreamFieldIsClassifiedAsSafeOrDenied) —
// a denial that drifts open is worse than none.
//
// extra_headers is DENIED, not safe. An overlay is a
// hand-edited ConfigMap; extra_headers decides what the proxy SENDS on a
// call it makes under a credential the proxy holds. On a keyed upstream
// that is credential-adjacent (Load already refuses a header naming the
// credential slot, but "safe" here means a field that says nothing about
// the proxy's custody or reach, and this one is entirely about what the
// proxy sends). On a keyless in-cluster one it would let an overlay
// forge whatever header that server trusts. Narrowing a hosted server is
// an operator decision that belongs in the committed table.
var custodyFields = []string{"credential_file", "credential_header", "internet", "ca_file", "extra_headers"}

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
// boot, and a validator that accepted one would tell an operator their
// standing constraint was fine when the plane will never see it.
func FragmentName(name string) bool {
	return !strings.HasPrefix(name, ".") && strings.HasSuffix(name, ".json")
}

// LoadDir is Load plus the overlay: read, merge, parse.
func LoadDir(path, dir string) (Config, error) {
	base, frags, err := Read(path, dir)
	if err != nil {
		return Config{}, err
	}
	merged, err := Merge(base, frags)
	if err != nil {
		return Config{}, err
	}
	return Parse(merged)
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
				if block == "tool_upstreams" {
					if err := refuseCustodyFields(f.Name, name, raw); err != nil {
						return nil, err
					}
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

// refuseCustodyFields rejects an overlay entry that tries to put the
// proxy's own custody, or its reach outside the cluster, under the
// control of a ConfigMap that exists to be edited.
//
// It checks the DECODED entry, not the key names, and that distinction
// is the whole correctness of this function. Go's decoder matches object
// keys to struct tags case-INSENSITIVELY, so a name-only denylist is
// bypassed by "Credential_File" or "INTERNET": the key misses the list,
// `DisallowUnknownFields` accepts it because it does match a field, and
// the proxy then reads the admin token and sends it wherever the entry
// says. (Confirmed against the decoder, not reasoned about.) Decoding
// through the same type the proxy uses cannot disagree with the proxy.
//
// The key scan is kept as a second gate — case-insensitively — so the
// message names the spelling the operator actually wrote.
func refuseCustodyFields(fragment, upstream string, raw json.RawMessage) error {
	refuse := func(field string) error {
		return fmt.Errorf("config: overlay %s: tool upstream %q sets %q, which an overlay may not set. "+
			"An overlay describes an in-cluster, keyless tool server; %s decide what credential the proxy "+
			"reads and which host outside the cluster it may be sent to, and belong in the committed table "+
			"(k8s/plane/upstreams.yaml) where they are reviewed as part of this repository",
			fragment, upstream, field, strings.Join(custodyFields, ", "))
	}
	var keys map[string]json.RawMessage
	if err := json.Unmarshal(raw, &keys); err != nil {
		return fmt.Errorf("config: overlay %s: tool upstream %q: want an object, got %s",
			fragment, upstream, firstBytes(raw))
	}
	for key := range keys {
		for _, field := range custodyFields {
			if strings.EqualFold(key, field) {
				return refuse(key)
			}
		}
	}
	// The authoritative check: whatever spelling got through above, this
	// is what the proxy would actually act on.
	var entry ToolUpstream
	if err := json.Unmarshal(raw, &entry); err != nil {
		return fmt.Errorf("config: overlay %s: tool upstream %q: %w", fragment, upstream, err)
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
// error: a hand-edited fragment whose `tool_upstreams` is a list or a
// string would otherwise merge nothing, reach Parse never, and leave the
// operator with a command that reported success and an upstream that
// does not exist. The overlay is meant to be hand-edited (standing
// constraints are added that way), so this is a real shape to refuse.
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

// maxConfigDepth bounds the walk. The table's own deepest value (a
// constraint literal inside a constraint object, inside a list, inside a
// tool, inside a credential, inside standing_constraints) is reached at
// depth 5; 32 is the same bound the gateway's canonicaliser uses.
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

// Package localtools defines bounded host-owned tool previews and explicit
// bindings, including the fixed read-only GitHub adapter. It reads frozen test
// states from the agent folder's kmx.yaml: Disabled answers stay local, while
// journaled settings distinguish live Enabled calls during zero-effect replay.
package localtools

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/secretshapes"
)

const Operation = "github_list_merged_prs"
const HarnessName = "kmx-local-tools-v1"
const endpointURL = "https://api.githubcopilot.com/mcp/readonly"
const maxBindingBytes = 16 * 1024

var errInvalidBinding = errors.New("local tools: select a valid bounded binding")
var refID = regexp.MustCompile("^[a-z][a-z0-9-]{0,62}$")
var envID = regexp.MustCompile("^[A-Za-z_][A-Za-z0-9_]{0,127}$")
var repositoryID = regexp.MustCompile("^[A-Za-z0-9]([A-Za-z0-9-]{0,37}[A-Za-z0-9])?/[A-Za-z0-9_][A-Za-z0-9_.-]{0,99}$")

// Binding freezes validated host configuration; it contains no credential value.
type Binding struct{ repository, endpoint, credentialEnv string }

func (b Binding) Repository() string    { return b.repository }
func (b Binding) Endpoint() string      { return b.endpoint }
func (b Binding) CredentialEnv() string { return b.credentialEnv }
func (b Binding) Matches(names []string) bool {
	return b.valid() && len(names) == 1 && names[0] == Operation
}
func (b Binding) valid() bool {
	return b.endpoint == endpointURL && repositoryID.MatchString(b.repository) && envID.MatchString(b.credentialEnv) && secretshapes.Match(b.repository+b.credentialEnv) == nil
}

// Load follows the agent-file reader's bounded regular-file policy: static
// symlink/reparse components refuse and the opened file must retain identity.
// It is not a hostile concurrent filesystem sandbox and looks up no credential.
func Load(path string) (Binding, error) {
	if path == "" || filepath.Clean(path) != path {
		return Binding{}, errInvalidBinding
	}
	for _, part := range strings.Split(filepath.ToSlash(path), "/") {
		if part == ".." {
			return Binding{}, errInvalidBinding
		}
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return Binding{}, errInvalidBinding
	}
	for p := absolute; ; p = filepath.Dir(p) {
		info, err := os.Lstat(p)
		if err != nil || info.Mode()&(os.ModeSymlink|os.ModeIrregular) != 0 {
			return Binding{}, errInvalidBinding
		}
		if p == filepath.Dir(p) {
			break
		}
	}
	before, err := os.Lstat(absolute)
	if err != nil || !before.Mode().IsRegular() || before.Size() > maxBindingBytes {
		return Binding{}, errInvalidBinding
	}
	file, err := os.Open(absolute)
	if err != nil {
		return Binding{}, errInvalidBinding
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || !os.SameFile(before, info) || info.Size() != before.Size() {
		return Binding{}, errInvalidBinding
	}
	data, err := io.ReadAll(io.LimitReader(file, maxBindingBytes+1))
	if err != nil || int64(len(data)) != info.Size() {
		return Binding{}, errInvalidBinding
	}
	return Parse(data)
}

// Parse accepts only the closed versioned binding shape, including exact keys.
func Parse(data []byte) (Binding, error) {
	if len(data) > maxBindingBytes || !utf8.Valid(data) || secretshapes.Match(string(data)) != nil {
		return Binding{}, errInvalidBinding
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	var root map[string]json.RawMessage
	if err := object(dec, &root); err != nil {
		return Binding{}, errInvalidBinding
	}
	if _, err := dec.Token(); err != io.EOF {
		return Binding{}, errInvalidBinding
	}
	if !keys(root, "schemaVersion", "bindings", "endpoints") {
		return Binding{}, errInvalidBinding
	}
	var version string
	var rows []json.RawMessage
	if json.Unmarshal(root["schemaVersion"], &version) != nil || version != "kmx.local-tools/v1" || json.Unmarshal(root["bindings"], &rows) != nil || len(rows) != 1 {
		return Binding{}, errInvalidBinding
	}
	var row, endpoints, endpoint map[string]json.RawMessage
	if object(json.NewDecoder(bytes.NewReader(rows[0])), &row) != nil || !keys(row, "name", "endpointRef", "repository") {
		return Binding{}, errInvalidBinding
	}
	var name, ref, repo, url, env string
	if !stringField(row, "name", &name) || name != Operation || !stringField(row, "endpointRef", &ref) || !refID.MatchString(ref) || !stringField(row, "repository", &repo) {
		return Binding{}, errInvalidBinding
	}
	if object(json.NewDecoder(bytes.NewReader(root["endpoints"])), &endpoints) != nil || !keys(endpoints, ref) {
		return Binding{}, errInvalidBinding
	}
	if object(json.NewDecoder(bytes.NewReader(endpoints[ref])), &endpoint) != nil || !keys(endpoint, "url", "credentialEnv") || !stringField(endpoint, "url", &url) || !stringField(endpoint, "credentialEnv", &env) {
		return Binding{}, errInvalidBinding
	}
	b := Binding{repo, url, env}
	if !b.valid() {
		return Binding{}, errInvalidBinding
	}
	return b, nil
}

// object preserves key identity and refuses duplicates before any map overwrite.
func object(dec *json.Decoder, out *map[string]json.RawMessage) error {
	token, err := dec.Token()
	if err != nil || token != json.Delim('{') {
		return errInvalidBinding
	}
	values := map[string]json.RawMessage{}
	for dec.More() {
		token, err = dec.Token()
		if err != nil {
			return errInvalidBinding
		}
		key, ok := token.(string)
		if !ok {
			return errInvalidBinding
		}
		if _, exists := values[key]; exists {
			return errInvalidBinding
		}
		var raw json.RawMessage
		if dec.Decode(&raw) != nil {
			return errInvalidBinding
		}
		values[key] = raw
	}
	if token, err = dec.Token(); err != nil || token != json.Delim('}') {
		return errInvalidBinding
	}
	*out = values
	return nil
}
func keys(values map[string]json.RawMessage, names ...string) bool {
	if len(values) != len(names) {
		return false
	}
	for _, name := range names {
		if _, ok := values[name]; !ok {
			return false
		}
	}
	return true
}
func stringField(values map[string]json.RawMessage, key string, out *string) bool {
	return json.Unmarshal(values[key], out) == nil && *out != "" && secretshapes.Match(*out) == nil
}

//go:build ignore

// Checkout-only CI fixture rendering; no CLI registration or cluster operations.
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/orkaschema"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/scaffold"
	"go.yaml.in/yaml/v3"
)

func reference(in io.Reader, out io.Writer) error {
	var spec struct {
		Name, Namespace, Description, ProviderType, Model, BaseURL, SecretName, SecretKey, Instructions string
		Tools                                                                                           []string
	}
	raw, err := io.ReadAll(io.LimitReader(in, 1<<20+1))
	if err != nil || len(raw) > 1<<20 {
		return fmt.Errorf("reference input unavailable or too large")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&spec); err != nil {
		return err
	}
	if decoder.Decode(new(any)) != io.EOF {
		return fmt.Errorf("trailing reference input")
	}
	bundle, err := scaffold.GenerateOrka(scaffold.OrkaSpec{
		Name: spec.Name, Namespace: spec.Namespace, Description: spec.Description,
		ProviderType: spec.ProviderType, Model: spec.Model, BaseURL: spec.BaseURL,
		SecretName: spec.SecretName, SecretKey: spec.SecretKey, Instructions: spec.Instructions, Tools: spec.Tools,
	})
	if err != nil {
		return err
	}
	validator, err := orkaschema.Offline("v0.2.0")
	if err != nil {
		return err
	}
	var documents [][]byte
	for _, doc := range bundle.Documents() {
		if doc["kind"] != "Secret" {
			if err := validator.Validate(doc); err != nil {
				return err
			}
		}
		raw, err := yaml.Marshal(doc)
		if err != nil {
			return err
		}
		documents = append(documents, raw)
	}
	return json.NewEncoder(out).Encode(documents)
}

func main() {
	if err := reference(os.Stdin, os.Stdout); err != nil {
		// Fixture inputs may contain private instructions. Only a closed error
		// leaves this helper; the qualifier keeps rendered documents private.
		fmt.Fprintln(os.Stderr, "native reference rendering refused")
		os.Exit(1)
	}
}

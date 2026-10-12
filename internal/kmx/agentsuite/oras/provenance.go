package oras

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"unicode"
	"unicode/utf8"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentsuite"
	godigest "github.com/opencontainers/go-digest"
	specs "github.com/opencontainers/image-spec/specs-go"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"oras.land/oras-go/v2/content"
	"oras.land/oras-go/v2/errdef"
	"oras.land/oras-go/v2/registry"
)

const (
	// InTotoMediaType is the referrer artifact type and its layer type.
	InTotoMediaType         = "application/vnd.in-toto+json"
	ProvenancePredicateType = "https://slsa.dev/provenance/v1"
	// ProvenanceBuildType links to the documented parameters.
	ProvenanceBuildType = "https://github.com/kaimahi-agents/kaimahi/blob/main/docs/kmx.md#suite-provenance"
	ProvenanceBuilderID = "https://github.com/kaimahi-agents/kaimahi/cmd/kmx"

	inTotoStatementType       = "https://in-toto.io/Statement/v1"
	inTotoPredicateAnnotation = "in-toto.io/predicate-type"

	// Referrers are untrusted registry content.
	maxProvenanceReferrers   = 64
	maxProvenanceObjectBytes = 1 << 20
)

var (
	errNotProvenance   = errors.New("not kmx provenance")
	errEnoughReferrers = errors.New("enough referrers")
	printableToken     = regexp.MustCompile(`^[\x21-\x7e]{1,128}$`)
	gitCommitPattern   = regexp.MustCompile(`^(?:[0-9a-f]{40}|[0-9a-f]{64})$`)
)

// SafeProvenanceText reports whether s is short, valid UTF-8 without
// control or format characters. Push and pull share it so kmx never
// records what its own pull rejects.
func SafeProvenanceText(s string) bool {
	if s == "" || len(s) > 512 || !utf8.ValidString(s) {
		return false
	}
	for _, r := range s {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return false
		}
	}
	return true
}

// ProvenanceSource is the commit a suite was packed from. Packing is
// deterministic, so the claim can be checked by repacking.
type ProvenanceSource struct {
	// URI is credential-free, or empty.
	URI    string
	Commit string
	// Path is relative to the repository root.
	Path string
}

// ProvenanceOptions configures provenance attached on push.
type ProvenanceOptions struct {
	BuilderVersion string
	// Source is nil unless every packed file matches it.
	Source *ProvenanceSource
	// Require fails the push, without tagging, if attaching fails.
	Require bool
}

// PushOptions configures a registry push.
type PushOptions struct {
	Provenance *ProvenanceOptions
}

// Provenance is one kmx provenance referrer: an unsigned claim.
type Provenance struct {
	Digest         godigest.Digest `json:"digest"`
	BuilderVersion string          `json:"builderVersion,omitempty"`
	SourceURI      string          `json:"sourceURI,omitempty"`
	SourceCommit   string          `json:"sourceCommit,omitempty"`
	SourcePath     string          `json:"sourcePath,omitempty"`
}

type inTotoStatement struct {
	Type          string          `json:"_type"`
	Subject       []inTotoSubject `json:"subject"`
	PredicateType string          `json:"predicateType"`
	Predicate     json.RawMessage `json:"predicate"`
}

type inTotoSubject struct {
	Name   string            `json:"name"`
	Digest map[string]string `json:"digest"`
}

type slsaProvenanceV1 struct {
	BuildDefinition slsaBuildDefinition `json:"buildDefinition"`
	RunDetails      slsaRunDetails      `json:"runDetails"`
}

type slsaBuildDefinition struct {
	BuildType string `json:"buildType"`
	// Raw so other producers' parameters still decode.
	ExternalParameters   json.RawMessage      `json:"externalParameters"`
	ResolvedDependencies []slsaResourceDigest `json:"resolvedDependencies,omitempty"`
}

type kmxExternalParameters struct {
	SourcePath string `json:"sourcePath,omitempty"`
}

type slsaResourceDigest struct {
	URI    string            `json:"uri,omitempty"`
	Digest map[string]string `json:"digest,omitempty"`
}

type slsaRunDetails struct {
	Builder slsaBuilder `json:"builder"`
}

type slsaBuilder struct {
	ID      string            `json:"id"`
	Version map[string]string `json:"version,omitempty"`
}

type provenanceArtifact struct {
	statement      ocispec.Descriptor
	statementBytes []byte
	manifest       ocispec.Descriptor
	manifestBytes  []byte
	summary        Provenance
}

// buildProvenance has no timestamps, so equal inputs give an equal referrer.
func buildProvenance(root ocispec.Descriptor, suiteName string, options ProvenanceOptions) (provenanceArtifact, error) {
	if root.Digest.Algorithm() != godigest.SHA256 {
		return provenanceArtifact{}, fmt.Errorf("AgentSuite provenance requires a sha256 manifest digest, got %s", root.Digest)
	}
	summary := Provenance{BuilderVersion: options.BuilderVersion}
	parameters := kmxExternalParameters{}
	var dependencies []slsaResourceDigest
	if source := options.Source; source != nil {
		if !gitCommitPattern.MatchString(source.Commit) {
			return provenanceArtifact{}, fmt.Errorf("AgentSuite provenance source commit %q is not a Git object ID", source.Commit)
		}
		if !SafeProvenanceText(source.Path) || (source.URI != "" && !SafeProvenanceText(source.URI)) {
			return provenanceArtifact{}, errors.New("AgentSuite provenance source path or URI is not printable text")
		}
		dependencies = append(dependencies, slsaResourceDigest{URI: source.URI, Digest: map[string]string{"gitCommit": source.Commit}})
		parameters.SourcePath = source.Path
		summary.SourceURI, summary.SourceCommit, summary.SourcePath = source.URI, source.Commit, source.Path
	}
	parametersBytes, err := json.Marshal(parameters)
	if err != nil {
		return provenanceArtifact{}, err
	}
	predicate := slsaProvenanceV1{
		BuildDefinition: slsaBuildDefinition{
			BuildType:            ProvenanceBuildType,
			ExternalParameters:   parametersBytes,
			ResolvedDependencies: dependencies,
		},
		RunDetails: slsaRunDetails{Builder: slsaBuilder{ID: ProvenanceBuilderID}},
	}
	if options.BuilderVersion != "" {
		predicate.RunDetails.Builder.Version = map[string]string{"kmx": options.BuilderVersion}
	}
	predicateBytes, err := json.Marshal(predicate)
	if err != nil {
		return provenanceArtifact{}, err
	}
	statementBytes, err := json.Marshal(inTotoStatement{
		Type:          inTotoStatementType,
		Subject:       []inTotoSubject{{Name: suiteName, Digest: map[string]string{"sha256": root.Digest.Encoded()}}},
		PredicateType: ProvenancePredicateType,
		Predicate:     predicateBytes,
	})
	if err != nil {
		return provenanceArtifact{}, fmt.Errorf("encode AgentSuite provenance: %w", err)
	}
	statement := ocispec.Descriptor{
		MediaType:   InTotoMediaType,
		Digest:      godigest.FromBytes(statementBytes),
		Size:        int64(len(statementBytes)),
		Annotations: map[string]string{inTotoPredicateAnnotation: ProvenancePredicateType},
	}
	subject := ocispec.Descriptor{MediaType: root.MediaType, Digest: root.Digest, Size: root.Size}
	manifestBytes, err := json.Marshal(ocispec.Manifest{
		Versioned:    specs.Versioned{SchemaVersion: 2},
		MediaType:    ocispec.MediaTypeImageManifest,
		ArtifactType: InTotoMediaType,
		Config:       ocispec.DescriptorEmptyJSON,
		Layers:       []ocispec.Descriptor{statement},
		Subject:      &subject,
	})
	if err != nil {
		return provenanceArtifact{}, fmt.Errorf("encode AgentSuite provenance manifest: %w", err)
	}
	manifest := ocispec.Descriptor{
		MediaType:    ocispec.MediaTypeImageManifest,
		ArtifactType: InTotoMediaType,
		Digest:       godigest.FromBytes(manifestBytes),
		Size:         int64(len(manifestBytes)),
	}
	summary.Digest = manifest.Digest
	return provenanceArtifact{
		statement:      statement,
		statementBytes: statementBytes,
		manifest:       manifest,
		manifestBytes:  manifestBytes,
		summary:        summary,
	}, nil
}

// pushProvenance always writes the manifest: without the referrers API,
// that is when the referrer gets indexed.
func pushProvenance(ctx context.Context, dst agentsuite.Storage, artifact provenanceArtifact) error {
	for _, blob := range []struct {
		descriptor ocispec.Descriptor
		data       []byte
	}{
		{ocispec.DescriptorEmptyJSON, ocispec.DescriptorEmptyJSON.Data},
		{artifact.statement, artifact.statementBytes},
	} {
		if err := pushIfMissing(ctx, dst, blob.descriptor, blob.data); err != nil {
			return fmt.Errorf("push AgentSuite provenance: %w", err)
		}
	}
	err := dst.Push(ctx, artifact.manifest, bytes.NewReader(artifact.manifestBytes))
	if err != nil && !isAlreadyExists(err) {
		return fmt.Errorf("push AgentSuite provenance: %w", err)
	}
	return nil
}

func pushIfMissing(ctx context.Context, dst agentsuite.Storage, descriptor ocispec.Descriptor, data []byte) error {
	exists, err := dst.Exists(ctx, descriptor)
	if err != nil {
		return err
	}
	if exists {
		return nil
	}
	// Losing a race to another pusher is fine: content is addressed by digest.
	if err := dst.Push(ctx, descriptor, bytes.NewReader(data)); err != nil && !isAlreadyExists(err) {
		return err
	}
	return nil
}

func isAlreadyExists(err error) bool {
	return errors.Is(err, agentsuite.ErrAlreadyExists) || errors.Is(err, errdef.ErrAlreadyExists)
}

// provenanceFor returns valid kmx provenance for root. Malformed kmx
// referrers become warnings; other producers' are skipped.
func provenanceFor(
	ctx context.Context,
	source agentsuite.ReadOnlyStorage,
	root ocispec.Descriptor,
) ([]Provenance, []string, error) {
	referrers, truncated, err := listInTotoReferrers(ctx, source, root)
	if err != nil {
		return nil, nil, err
	}
	var warnings []string
	if truncated {
		warnings = append(warnings, fmt.Sprintf("AgentSuite has more than %d in-toto referrers; checked only the first %d", maxProvenanceReferrers, maxProvenanceReferrers))
	}
	var found []Provenance
	for _, referrer := range referrers {
		if err := referrer.Digest.Validate(); err != nil {
			warnings = append(warnings, fmt.Sprintf("ignored AgentSuite referrer with invalid digest %q", string(referrer.Digest)))
			continue
		}
		provenance, err := verifyProvenance(ctx, source, root, referrer)
		if errors.Is(err, errNotProvenance) {
			continue
		}
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("ignored AgentSuite provenance %s: %v", referrer.Digest, err))
			continue
		}
		found = append(found, provenance)
	}
	return found, warnings, nil
}

// listInTotoReferrers stops at the cap instead of reading every page.
func listInTotoReferrers(
	ctx context.Context,
	source agentsuite.ReadOnlyStorage,
	root ocispec.Descriptor,
) ([]ocispec.Descriptor, bool, error) {
	var referrers []ocispec.Descriptor
	if lister, ok := source.(registry.ReferrerLister); ok {
		err := lister.Referrers(ctx, root, InTotoMediaType, func(page []ocispec.Descriptor) error {
			referrers = append(referrers, page...)
			if len(referrers) > maxProvenanceReferrers {
				return errEnoughReferrers
			}
			return nil
		})
		if err != nil && !errors.Is(err, errEnoughReferrers) {
			return nil, false, fmt.Errorf("list AgentSuite provenance referrers: %w", err)
		}
	} else if graph, ok := source.(content.ReadOnlyGraphStorage); ok {
		var err error
		if referrers, err = registry.Referrers(ctx, graph, root, InTotoMediaType); err != nil {
			return nil, false, fmt.Errorf("list AgentSuite provenance referrers: %w", err)
		}
	} else {
		return nil, false, errors.New("AgentSuite source cannot list referrers")
	}
	if len(referrers) > maxProvenanceReferrers {
		return referrers[:maxProvenanceReferrers], true, nil
	}
	return referrers, false, nil
}

func verifyProvenance(
	ctx context.Context,
	source agentsuite.Fetcher,
	root ocispec.Descriptor,
	referrer ocispec.Descriptor,
) (Provenance, error) {
	if referrer.MediaType != ocispec.MediaTypeImageManifest {
		return Provenance{}, errNotProvenance
	}
	manifestBytes, err := fetchBounded(ctx, source, referrer)
	if err != nil {
		return Provenance{}, fmt.Errorf("fetch manifest: %w", err)
	}
	var manifest ocispec.Manifest
	if err := json.Unmarshal(manifestBytes, &manifest); err != nil {
		return Provenance{}, fmt.Errorf("decode manifest: %w", err)
	}
	if manifest.ArtifactType != InTotoMediaType || len(manifest.Layers) != 1 || manifest.Layers[0].MediaType != InTotoMediaType {
		return Provenance{}, errNotProvenance
	}
	layer := manifest.Layers[0]
	if annotation := layer.Annotations[inTotoPredicateAnnotation]; annotation != "" && annotation != ProvenancePredicateType {
		return Provenance{}, errNotProvenance
	}
	if manifest.Subject == nil || manifest.Subject.Digest != root.Digest || manifest.Subject.Size != root.Size {
		return Provenance{}, errors.New("subject does not name the pulled AgentSuite manifest")
	}
	statementBytes, err := fetchBounded(ctx, source, layer)
	if err != nil {
		return Provenance{}, fmt.Errorf("fetch statement: %w", err)
	}
	var statement inTotoStatement
	if err := json.Unmarshal(statementBytes, &statement); err != nil {
		return Provenance{}, fmt.Errorf("decode statement: %w", err)
	}
	if annotation := layer.Annotations[inTotoPredicateAnnotation]; annotation != "" && annotation != statement.PredicateType {
		return Provenance{}, errors.New("statement predicate does not match its layer annotation")
	}
	if statement.PredicateType != ProvenancePredicateType {
		return Provenance{}, errNotProvenance
	}
	var predicate slsaProvenanceV1
	if err := json.Unmarshal(statement.Predicate, &predicate); err != nil {
		return Provenance{}, fmt.Errorf("decode predicate: %w", err)
	}
	// Other producers' provenance is theirs to verify.
	if predicate.BuildDefinition.BuildType != ProvenanceBuildType {
		return Provenance{}, errNotProvenance
	}
	if statement.Type != inTotoStatementType {
		return Provenance{}, fmt.Errorf("unsupported in-toto statement type %q", statement.Type)
	}
	if !statementNames(statement, root.Digest) {
		return Provenance{}, errors.New("statement subject does not name the pulled AgentSuite manifest")
	}
	if predicate.RunDetails.Builder.ID != ProvenanceBuilderID {
		return Provenance{}, fmt.Errorf("builder %q is not kmx", predicate.RunDetails.Builder.ID)
	}
	// Printed values must be safe text.
	summary := Provenance{Digest: referrer.Digest, BuilderVersion: predicate.RunDetails.Builder.Version["kmx"]}
	if summary.BuilderVersion != "" && !printableToken.MatchString(summary.BuilderVersion) {
		return Provenance{}, errors.New("builder version is not a short printable token")
	}
	var parameters kmxExternalParameters
	if len(predicate.BuildDefinition.ExternalParameters) != 0 {
		if err := json.Unmarshal(predicate.BuildDefinition.ExternalParameters, &parameters); err != nil {
			return Provenance{}, fmt.Errorf("decode external parameters: %w", err)
		}
	}
	switch len(predicate.BuildDefinition.ResolvedDependencies) {
	case 0:
		if parameters.SourcePath != "" {
			return Provenance{}, errors.New("source path recorded without a source commit")
		}
	case 1:
		dependency := predicate.BuildDefinition.ResolvedDependencies[0]
		commit := dependency.Digest["gitCommit"]
		if !gitCommitPattern.MatchString(commit) {
			return Provenance{}, errors.New("source dependency is not a Git commit")
		}
		if dependency.URI != "" && !SafeProvenanceText(dependency.URI) {
			return Provenance{}, errors.New("source URI is not printable")
		}
		if parameters.SourcePath != "" && !SafeProvenanceText(parameters.SourcePath) {
			return Provenance{}, errors.New("source path is not printable")
		}
		summary.SourceURI, summary.SourceCommit, summary.SourcePath = dependency.URI, commit, parameters.SourcePath
	default:
		return Provenance{}, errors.New("kmx provenance records at most one source dependency")
	}
	return summary, nil
}

func statementNames(statement inTotoStatement, digest godigest.Digest) bool {
	for _, subject := range statement.Subject {
		if subject.Digest[digest.Algorithm().String()] == digest.Encoded() {
			return true
		}
	}
	return false
}

// fetchBounded checks size before reading and digest after.
func fetchBounded(ctx context.Context, source agentsuite.Fetcher, descriptor ocispec.Descriptor) ([]byte, error) {
	if descriptor.Size < 0 || descriptor.Size > maxProvenanceObjectBytes {
		return nil, fmt.Errorf("size %d exceeds %d bytes", descriptor.Size, maxProvenanceObjectBytes)
	}
	reader, err := source.Fetch(ctx, descriptor)
	if err != nil {
		return nil, err
	}
	defer reader.Close()
	return content.ReadAll(io.LimitReader(reader, descriptor.Size+1), descriptor)
}

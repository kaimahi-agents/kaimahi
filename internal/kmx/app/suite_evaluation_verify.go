package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	suitecore "github.com/agent-primitives/suite/core"
	suiteevidence "github.com/agent-primitives/suite/evidence"
)

// SuiteEvaluationVerifyOptions selects only local public verification inputs.
// Expect must contain independently reviewed, complete ExpectedEvaluation values.
type SuiteEvaluationVerifyOptions struct {
	Suite, FromLayout, Evidence, Expect, Output string
}

const maxSuiteEvaluationVerifyBytes = 4 * 1024 * 1024

// suiteEvaluationVerifyRefusal keeps the shared typed refusal available without
// including file paths, parser details, or untrusted claims in diagnostics.
type suiteEvaluationVerifyRefusal struct {
	refusal *suiteevidence.EvidenceRefusal
}

func (e *suiteEvaluationVerifyRefusal) Unwrap() error { return e.refusal }
func (e *suiteEvaluationVerifyRefusal) Error() string {
	var message string
	switch e.refusal.Code {
	case "missing":
		message = "required evaluation inputs or identities are missing"
	case "malformed":
		message = "evaluation inputs must be complete, valid regular files"
	case "unsupported":
		message = "the evaluation format or policy is unsupported"
	case "limit":
		message = "evaluation inputs exceed verification limits"
	case "artifact":
		message = "a valid single-manifest local OCI layout is required"
	case "subject":
		message = "the artifact or execution does not match the reviewed expectations"
	case "coverage":
		message = "evidence does not cover the complete authored evaluation"
	case "policy":
		message = "evidence does not match the reviewed evaluation policy"
	case "unknown":
		message = "the evaluation contains unknown or incomplete outcomes"
	case "not-passing":
		message = "the complete evaluation is not passing"
	default:
		return "evaluation: not verified: verification refused"
	}
	return "evaluation: not verified [" + e.refusal.Code + "]: " + message
}

func suiteEvaluationRefuse(code string) error {
	return &suiteEvaluationVerifyRefusal{refusal: &suiteevidence.EvidenceRefusal{Code: code}}
}

// VerifySuiteEvaluation checks unsigned consistency using only explicit local
// bytes. It deliberately does not initialize App configuration, private state,
// a workspace, runtime, registry, or any operational dependency.
func (a *App) VerifySuiteEvaluation(ctx context.Context, opt SuiteEvaluationVerifyOptions) error {
	if ctx.Err() != nil {
		return errors.New("evaluation: verification canceled")
	}
	if strings.TrimSpace(opt.Suite) == "" || strings.TrimSpace(opt.Evidence) == "" || strings.TrimSpace(opt.Expect) == "" {
		return errors.New("evaluation: suite, evidence, and expect inputs are required")
	}
	if opt.Output == "" {
		opt.Output = "text"
	}
	if opt.Output != "text" && opt.Output != "json" {
		return errors.New("evaluation: output must be text or json")
	}
	layout := opt.Suite
	digest := suitecore.IsValidDigest(opt.Suite)
	if digest {
		if strings.TrimSpace(opt.FromLayout) == "" {
			return errors.New("evaluation: a digest requires from-layout")
		}
		layout = opt.FromLayout
	} else {
		// References and URLs never trigger lookup, even if a similarly named
		// local directory exists. The shared verifier validates the layout itself.
		if strings.ContainsAny(opt.Suite, ":@") || strings.HasPrefix(opt.Suite, "//") {
			return suiteEvaluationRefuse("artifact")
		}
		if opt.FromLayout != "" {
			return errors.New("evaluation: from-layout requires a suite digest")
		}
	}
	raw, err := readSuiteEvaluationVerifyFile(opt.Expect)
	if err != nil {
		return err
	}
	var expected suiteevidence.ExpectedEvaluation
	if suitecore.Unmarshal(raw, &expected) != nil || expected.Judges == nil {
		return suiteEvaluationRefuse("malformed")
	}
	// Shared strict decoding rejects unknown/duplicate/null fields. Comparing
	// canonical typed and untyped documents additionally requires every field,
	// including an explicitly empty platform.variant and a non-null judges [].
	var value any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if decoder.Decode(&value) != nil {
		return suiteEvaluationRefuse("malformed")
	}
	original, originalErr := suitecore.Marshal(value)
	typed, typedErr := suitecore.Marshal(expected)
	if originalErr != nil || typedErr != nil || !bytes.Equal(original, typed) {
		return suiteEvaluationRefuse("malformed")
	}
	// A digest is an extra independent expectation, never a source of bytes.
	// Check it before opening evidence (which may itself be hostile).
	if digest && opt.Suite != expected.Subject.Manifest.Digest {
		return suiteEvaluationRefuse("subject")
	}
	evidence, err := readSuiteEvaluationVerifyFile(opt.Evidence)
	if err != nil {
		return err
	}
	if ctx.Err() != nil {
		return errors.New("evaluation: verification canceled")
	}
	verified, err := suiteevidence.VerifyEvaluation(layout, expected, bytes.NewReader(evidence))
	if ctx.Err() != nil {
		return errors.New("evaluation: verification canceled")
	}
	if err != nil {
		var refusal *suiteevidence.EvidenceRefusal
		if errors.As(err, &refusal) {
			return &suiteEvaluationVerifyRefusal{refusal: refusal}
		}
		return errors.New("evaluation: verification failed")
	}
	var output []byte
	if opt.Output == "json" {
		output, err = suitecore.Marshal(verified)
		if err != nil {
			return errors.New("evaluation: could not encode verified summary")
		}
		output = append(output, '\n')
	} else {
		output = []byte(fmt.Sprintf("evaluation: verified (unsigned consistency only)\n%d cases, %d runs\ncompletedAt (unsigned claim): %s\n", verified.Cases, verified.Runs, verified.CompletedAt))
	}
	if a.Out == nil {
		return errors.New("evaluation: could not write verified summary")
	}
	n, err := a.Out.Write(output)
	if err != nil || n != len(output) {
		return errors.New("evaluation: could not write verified summary")
	}
	return nil
}

func readSuiteEvaluationVerifyOpened(file *os.File) ([]byte, error) {
	before, err := file.Stat()
	if err != nil || !before.Mode().IsRegular() {
		return nil, suiteEvaluationRefuse("malformed")
	}
	if before.Size() > maxSuiteEvaluationVerifyBytes {
		return nil, suiteEvaluationRefuse("limit")
	}
	raw, err := io.ReadAll(io.LimitReader(file, maxSuiteEvaluationVerifyBytes+1))
	if err != nil {
		return nil, suiteEvaluationRefuse("malformed")
	}
	if len(raw) > maxSuiteEvaluationVerifyBytes {
		return nil, suiteEvaluationRefuse("limit")
	}
	after, err := file.Stat()
	if err != nil || int64(len(raw)) != before.Size() || after.Size() != before.Size() || after.Mode() != before.Mode() || !after.ModTime().Equal(before.ModTime()) {
		return nil, suiteEvaluationRefuse("malformed")
	}
	if len(raw) == 0 {
		return nil, suiteEvaluationRefuse("missing")
	}
	return raw, nil
}

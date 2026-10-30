package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/app"
)

var errFolderDiscovery = errors.New("cannot read the current folder or .github/agents")

// Discovery selects names, not read authority. Keep unsafe matching entries for
// the checked pair loader to refuse; never resolve a symlink into an allowed file.
func discoverAgentFiles(ctx context.Context, root string) ([]string, error) {
	dirs := []string{root}
	parent := root
	for _, name := range []string{".github", "agents"} {
		parent = filepath.Join(parent, name)
		info, err := os.Lstat(parent)
		if os.IsNotExist(err) {
			break
		}
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return nil, errFolderDiscovery
		}
		if name == "agents" {
			dirs = append(dirs, parent)
		}
	}
	files := []string{}
	for _, dir := range dirs {
		entries, err := os.ReadDir(dir)
		if err != nil {
			return nil, errFolderDiscovery
		}
		for _, entry := range entries {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			if !strings.HasSuffix(entry.Name(), ".agent.md") {
				continue
			}
			files = append(files, filepath.Join(dir, entry.Name()))
		}
	}
	sort.Strings(files)
	return files, nil
}

type folderFileResult struct {
	Index     int                    `json:"index"`
	Directory string                 `json:"directory"`
	File      string                 `json:"file"`
	Status    string                 `json:"status"`
	Result    *app.FilePreviewResult `json:"result,omitempty"`
}

type folderTestCounts struct {
	Total       int `json:"total"`
	Passed      int `json:"passed"`
	Failed      int `json:"failed"`
	Unknown     int `json:"unknown"`
	CouldNotRun int `json:"couldNotRun"`
	NotRun      int `json:"notRun"`
}

func (c *folderTestCounts) add(status string) {
	switch status {
	case "passed":
		c.Passed++
	case "failed":
		c.Failed++
	case "unknown":
		c.Unknown++
	case "could_not_run":
		c.CouldNotRun++
	case "not_run":
		c.NotRun++
	}
}

// Status and continuation are deliberately separate: a completed inconclusive
// judge remains exit 2, while all unknown execution/route failures abort sends.
func folderFileOutcome(r app.FilePreviewResult) (status string, proceed bool) {
	if r.State == "complete" {
		switch r.ReasonCode {
		case "checks_passed":
			return "passed", true
		case "assertion_failed":
			return "failed", true
		case "judge_unknown":
			return "unknown", true
		}
	}
	switch r.ReasonCode {
	case "invalid_file", "no_tests", "invalid_options":
		return "could_not_run", true
	}
	return "could_not_run", false
}

func runFolderTest(cmd *cobra.Command, state *commandState, f fileCommandOptions) error {
	ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	previous := cmd.Context()
	cmd.SetContext(ctx)
	defer cmd.SetContext(previous)
	root, err := os.Getwd()
	files, discoveryErr := []string{}, errFolderDiscovery
	if err == nil {
		files, discoveryErr = discoverAgentFiles(ctx, root)
	}
	var sink io.WriteCloser
	if f.GitHubSummary {
		sink, err = state.deps.openGitHubSummary()
		if err != nil {
			return fileUsageError(err)
		}
		defer func() {
			if sink != nil {
				_ = sink.Close()
			}
		}()
	}
	out := cmd.OutOrStdout()
	jsonOutput := f.Output == "json"
	if jsonOutput {
		_, err = io.WriteString(out, "{\"schemaVersion\":1,\"kind\":\"kmx.agent-folder-test\",\"files\":[")
	} else {
		_, err = fmt.Fprintf(out, "Testing %d agent file(s) in this folder and .github/agents\n", len(files))
	}
	if err != nil {
		return fmt.Errorf("cannot write folder Test output")
	}
	summary := folderSummaryWriter{out: sink}
	if err := summary.start(); err != nil {
		return err
	}
	counts := folderTestCounts{Total: len(files)}
	aborted, code, reason := false, 0, "completed"
	var cause error
	if discoveryErr != nil {
		code, reason, cause = 2, "discovery_failed", errFolderDiscovery
	} else if len(files) == 0 {
		code, reason, cause = 2, "no_files", errors.New("no agent files found in this folder or .github/agents")
	}
	for index, file := range files {
		if ctx.Err() != nil {
			aborted, code, reason, cause = true, 130, "aborted", context.Canceled
		}
		// Unexecuted filenames have not passed the runner's credential checks.
		row := folderFileResult{Index: index + 1, Directory: ".", File: "[agent file]", Status: "not_run"}
		if filepath.Dir(file) != root {
			row.Directory = ".github/agents"
		}
		if !aborted {
			result, fileErr := previewFileCommand(cmd, state, file, f, "", "", nil)
			// Intake refusals may precede resolved-credential checks too. Only
			// completed results can name discovered sources in folder output.
			if result.State != "complete" {
				result.File.Path = "[agent file]"
				if result.Tests != nil {
					result.Tests.Path = "[tests file]"
				}
				if result.RecordingTarget != nil {
					result.RecordingTarget.Path = "[recording source]"
				}
			}
			var proceed bool
			row.Status, proceed = folderFileOutcome(result)
			aborted = !proceed
			if result.SchemaVersion != 0 {
				row.Result, row.File = &result, result.File.Path
			}
			if fileErr != nil {
				cause = fileErr
			}
			if aborted {
				reason = "aborted"
				if exitCode(fileErr) == 130 {
					code = 130
				}
			}
			// Diagnostics are output failures, not an inconclusive evaluation.
			if result.SchemaVersion == 0 || result.ReasonCode == "diagnostics_failed" {
				return fmt.Errorf("cannot write local preview diagnostics")
			}
		}
		counts.add(row.Status)
		if jsonOutput {
			if index > 0 {
				_, err = io.WriteString(out, ",")
			}
			if err == nil {
				err = json.NewEncoder(out).Encode(row)
			}
		} else {
			_, err = fmt.Fprintf(out, "\nFile %d: %s/%s — %s\n", row.Index, row.Directory, row.File, row.Status)
			if err == nil && row.Result != nil {
				err = app.WriteFilePreviewDetails(out, *row.Result)
			}
		}
		if err != nil {
			return fmt.Errorf("cannot write folder Test output")
		}
		if err := summary.file(row); err != nil {
			return err
		}
	}
	if ctx.Err() != nil {
		code, reason, cause = 130, "aborted", context.Canceled
	}
	if code == 0 {
		if counts.CouldNotRun+counts.Unknown+counts.NotRun > 0 {
			code = 2
		} else if counts.Failed > 0 {
			code = 1
		}
	}
	if err := summary.finish(counts, code, reason); err != nil {
		return err
	}
	if sink != nil {
		err = sink.Close()
		sink = nil
		if err != nil {
			return fmt.Errorf("cannot write GitHub Actions summary")
		}
	}
	if jsonOutput {
		_, err = io.WriteString(out, "],")
		if err == nil {
			tail := struct {
				Summary    folderTestCounts `json:"summary"`
				ExitCode   int              `json:"exitCode"`
				ReasonCode string           `json:"reasonCode"`
			}{counts, code, reason}
			var encoded []byte
			encoded, err = json.Marshal(tail)
			if err == nil {
				_, err = fmt.Fprintf(out, "%s\n", encoded[1:])
			}
		}
	} else {
		_, err = fmt.Fprintf(out, "\nTest summary: %d passed, %d failed, %d unknown, %d could not run, %d not run (%d total); exit %d [%s]\n", counts.Passed, counts.Failed, counts.Unknown, counts.CouldNotRun, counts.NotRun, counts.Total, code, reason)
	}
	if err != nil {
		return fmt.Errorf("cannot write folder Test output")
	}
	if code != 0 {
		if cause == nil {
			cause = errors.New("folder Test did not pass")
		}
		return &fileCommandError{cause: cause, exit: code}
	}
	return nil
}

const folderSummaryMaxBytes = 1 << 20

// Summary sections are independently bounded by the existing file renderer;
// retain only one section, and always leave space for honest final counts.
type folderSummaryWriter struct {
	out              io.Writer
	written, omitted int
}

func (s *folderSummaryWriter) start() error {
	if s.out == nil {
		return nil
	}
	n, err := io.WriteString(s.out, "## kmx folder test\n\n")
	s.written += n
	if err != nil {
		return fmt.Errorf("cannot write GitHub Actions summary")
	}
	return nil
}

func (s *folderSummaryWriter) file(row folderFileResult) error {
	if s.out == nil {
		return nil
	}
	var section bytes.Buffer
	if row.Result != nil {
		if err := app.WriteFilePreviewGitHubSection(&section, *row.Result, row.Index); err != nil {
			return err
		}
	} else {
		fmt.Fprintf(&section, "### File %d: NOT RUN\n\n", row.Index)
	}
	if s.written+section.Len() > folderSummaryMaxBytes-1024 {
		s.omitted++
		return nil
	}
	n, err := s.out.Write(section.Bytes())
	s.written += n
	if err != nil {
		return fmt.Errorf("cannot write GitHub Actions summary")
	}
	return nil
}

func (s *folderSummaryWriter) finish(c folderTestCounts, code int, reason string) error {
	if s.out == nil {
		return nil
	}
	_, err := fmt.Fprintf(s.out, "\n### Summary\n\n%d passed, %d failed, %d unknown, %d could not run, %d not run (%d total). Exit %d; `%s`.\n\n%d additional file section(s) are available in the JSON report.\n\n> Local preview report only. It is not suite evidence, publication authority, or deployment authority.\n", c.Passed, c.Failed, c.Unknown, c.CouldNotRun, c.NotRun, c.Total, code, reason, s.omitted)
	if err != nil {
		return fmt.Errorf("cannot write GitHub Actions summary")
	}
	return nil
}

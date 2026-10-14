package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"oras.land/oras-go/v2/registry"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentsuite/workspace"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/config"
	"github.com/spf13/cobra"
)

// resolveDestinations reads only the workspace descriptor and operational config,
// never member source, inference/binding contents, application state or tools.
func resolveDestinations(override string) (string, *config.Destinations, error) {
	start, err := os.Getwd()
	if err != nil {
		return "", nil, errors.New("cannot resolve workspace")
	}
	root, err := workspace.Resolve(start, override)
	if err != nil {
		if errors.Is(err, workspace.ErrNotFound) {
			return "", nil, fmt.Errorf("no workspace found; run from a valid workspace directory: %w", err)
		}
		return "", nil, fmt.Errorf("cannot resolve workspace: %w; run from a valid workspace directory", err)
	}
	doc, err := config.LoadDestinations(root)
	return root, doc, err
}

type destinationRow struct {
	Name string `json:"name"`
	config.Destination
	Repository     string `json:"repository,omitempty"`
	PlainHTTP      bool   `json:"plainHTTP"`
	ContextCheck   string `json:"contextCheck"`
	RegistryCheck  string `json:"registryCheck"`
	RuntimeCheck   string `json:"runtimeCheck"`
	InferenceCheck string `json:"inferenceCheck"`
	BindingsCheck  string `json:"bindingsCheck"`
}

func newDestinationsCommand(state *commandState) *cobra.Command {
	var root, output string
	var check bool
	cmd := &cobra.Command{
		Use: "ctx", Short: "List workspace named destinations without selecting or switching",
		Long: "List workspace named destinations alphabetically, without reading application settings, selecting a context or changing state. Runtime, inference and bindings are declarations only, not checked.",
		Args: cobra.NoArgs,
		PreRunE: func(cmd *cobra.Command, _ []string) error {
			if state.contextFlag != "" || cmd.Flags().Changed("context") {
				return errors.New("ctx does not select a context")
			}
			if output != "table" && output != "json" {
				return errors.New("ctx output must be table or json")
			}
			if cmd.Flags().Changed("workspace") && root == "" {
				return errors.New("ctx requires non-empty --workspace")
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, _ []string) error {
			_, doc, err := resolveDestinations(root)
			if err != nil {
				return err
			}
			rows := make([]destinationRow, 0)
			if doc != nil {
				for name, target := range doc.Targets {
					rows = append(rows, destinationRow{Name: name, Destination: target, Repository: doc.PublishRepository, PlainHTTP: doc.RegistryHTTP(doc.PublishRepository), ContextCheck: "not checked", RegistryCheck: "not checked", RuntimeCheck: "not checked", InferenceCheck: "not checked", BindingsCheck: "not checked"})
				}
			}
			sort.Slice(rows, func(i, j int) bool { return rows[i].Name < rows[j].Name })
			var checkErr error
			if check {
				checkErr = checkDestinations(cmd.Context(), rows)
			}
			if output == "json" {
				return errors.Join(checkErr, json.NewEncoder(cmd.OutOrStdout()).Encode(rows))
			}
			w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 4, 2, ' ', 0)
			if _, err := fmt.Fprintln(w, "NAME\tCONTEXT\tNAMESPACE\tRUNTIME (DECLARED)\tINFERENCE / BINDINGS\tREPOSITORY\tHTTP\tCONTEXT CHECK\tREGISTRY CHECK"); err != nil {
				return err
			}
			for _, row := range rows {
				runtime := row.Runtime
				if runtime == "" {
					runtime = "undeclared"
				}
				binding := row.Inference
				if row.Bindings != "" {
					binding = row.Bindings
				}
				if _, err := fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%t\t%s\t%s\n", row.Name, row.Context, row.Namespace, runtime, binding, row.Repository, row.PlainHTTP, row.ContextCheck, row.RegistryCheck); err != nil {
					return err
				}
			}
			return errors.Join(checkErr, w.Flush())
		},
	}
	cmd.Flags().BoolVar(&check, "check", false, "bounded local context-name and anonymous registry /v2/ checks; no runtime, inference, bindings or authorization check")
	cmd.Flags().StringVar(&root, "workspace", "", "select an exact workspace root instead of nearest-ancestor discovery")
	cmd.Flags().StringVarP(&output, "output", "o", "table", "output: table|json")
	_ = cmd.MarkFlagDirname("workspace")
	_ = cmd.RegisterFlagCompletionFunc("output", staticCompletion([]string{"table", "json"}))
	return cmd
}

const destinationCheckBytes = 64 << 10
const destinationCheckTimeout = 3 * time.Second

// Returning an error on excess output bounds subprocess capture, including its
// diagnostics. None of those diagnostics are displayed to the operator.
type destinationCheckBuffer struct{ data []byte }

func (b *destinationCheckBuffer) Write(p []byte) (int, error) {
	if len(b.data)+len(p) > destinationCheckBytes {
		return 0, errors.New("check output exceeded bound")
	}
	b.data = append(b.data, p...)
	return len(p), nil
}
func destinationContexts(ctx context.Context) (map[string]bool, error) {
	ctx, cancel := context.WithTimeout(ctx, destinationCheckTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "kubectl", "config", "view", "-o", `jsonpath={range .contexts[*]}{.name}{"\n"}{end}`)
	var out, diagnostics destinationCheckBuffer
	cmd.Stdout = &out
	cmd.Stderr = &diagnostics
	cmd.WaitDelay = 100 * time.Millisecond
	if cmd.Run() != nil {
		return nil, errors.New("local context names unknown")
	}
	names := map[string]bool{}
	for _, name := range strings.Split(string(out.data), "\n") {
		if name != "" {
			names[name] = true
		}
	}
	return names, nil
}
func checkDestinationRegistry(ctx context.Context, reference string, plainHTTP bool) string {
	ref, err := registry.ParseReference(reference)
	if err != nil {
		return "unknown"
	}
	scheme := "https"
	if plainHTTP {
		scheme = "http"
	}
	ctx, cancel := context.WithTimeout(ctx, destinationCheckTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, scheme+"://"+ref.Registry+"/v2/", nil)
	if err != nil {
		return "unknown"
	}
	// Do not use ambient proxies, redirect handling, credential stores or helpers.
	transport := &http.Transport{Proxy: nil, DisableKeepAlives: true, MaxResponseHeaderBytes: destinationCheckBytes}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: destinationCheckTimeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	response, err := client.Do(req)
	if err != nil {
		return "unknown"
	}
	defer response.Body.Close()
	n, err := io.Copy(io.Discard, io.LimitReader(response.Body, destinationCheckBytes+1))
	if err != nil || n > destinationCheckBytes {
		return "unknown"
	}
	switch response.StatusCode {
	case http.StatusOK:
		return "reachable"
	case http.StatusUnauthorized:
		return "auth required"
	default:
		return "refused"
	}
}
func checkDestinations(ctx context.Context, rows []destinationRow) error {
	if len(rows) == 0 {
		return nil
	}
	names, err := destinationContexts(ctx)
	registries := map[string]string{}
	failed := false
	for i := range rows {
		row := &rows[i]
		row.ContextCheck = "unknown"
		if err == nil {
			row.ContextCheck = "absent"
			if names[row.Context] {
				row.ContextCheck = "present"
			}
		}
		if row.ContextCheck != "present" {
			failed = true
		}
		if row.Repository != "" {
			status, ok := registries[row.Repository]
			if !ok {
				status = checkDestinationRegistry(ctx, row.Repository, row.PlainHTTP)
				registries[row.Repository] = status
			}
			row.RegistryCheck = status
			if status != "reachable" {
				failed = true
			}
		}
	}
	if failed {
		return errors.New("destination checks failed or unknown; runtime, inference, bindings and repository authorization not checked")
	}
	return nil
}
